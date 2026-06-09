// Package blobindex is the Redis-backed liveness + lease layer for the
// content-addressed blob store (Phase 16, BLOB-07/08). Blob BYTES live in S3
// (internal/blobstore); this package owns the LIVENESS metadata that decides
// when a blob may be garbage-collected:
//
//   - blob:meta:<hash>     — small hash {size, createdAtMs} with an idle TTL.
//     Touch-on-use extends the TTL MONOTONICALLY (only ever lengthens, never
//     shrinks). Its EXISTENCE is the liveness signal: an expired meta makes the
//     blob a GC candidate.
//   - blobs:index          — SET of all known hashes (GC enumeration source).
//   - blob:lease:<hash>    — SET of active jobIds referencing the blob. Non-empty
//     ⇒ pinned; GC must never delete a leased blob.
//
// The `hash` passed to every method is the full "sha256:<64hex>" ref string, so
// the Redis key, the FileInput.ref, and the S3 object key are the same token.
package blobindex

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/teovillanueva/code-runner/internal/keys"
)

// touchScript is the FIRST Lua in this codebase. It performs the monotonic
// touch-on-use atomically so a concurrent shorter touch can never shrink the
// TTL:
//
//	KEYS[1] = blob:meta:<hash>     (the liveness hash)
//	KEYS[2] = blobs:index          (the known-hashes set)
//	ARGV[1] = hash                 (member added to the index set)
//	ARGV[2] = size                 (bytes; recorded once via HSETNX)
//	ARGV[3] = createdAtMs          (unix ms; recorded once via HSETNX)
//	ARGV[4] = requestedTTLms       (the idle TTL to extend toward)
//
// Steps:
//  1. SADD the hash to the index (idempotent).
//  2. HSETNX size/createdAtMs — only sets them the FIRST time, so re-touching a
//     known blob never rewrites its original metadata.
//  3. Read the current PTTL. If the requested TTL is LONGER (or the key has no
//     TTL / -1, or does not yet exist / -2), PEXPIRE to the requested value.
//     Otherwise leave the longer existing TTL untouched — this is the monotonic
//     guarantee: a shorter touch is a no-op on the TTL.
//
// Returns the resulting PTTL in ms (for tests/observability).
const touchScript = `
local meta  = KEYS[1]
local index = KEYS[2]
local hash  = ARGV[1]
local size  = ARGV[2]
local created = ARGV[3]
local reqTtl = tonumber(ARGV[4])

redis.call('SADD', index, hash)
redis.call('HSETNX', meta, 'size', size)
redis.call('HSETNX', meta, 'createdAtMs', created)

local cur = redis.call('PTTL', meta)
-- PTTL returns -2 (no key) or -1 (no expiry). In both cases, and whenever the
-- requested TTL is strictly longer than the current one, extend to reqTtl.
-- A shorter requested TTL leaves the existing (longer) TTL untouched: MONOTONIC.
if cur < 0 or reqTtl > cur then
  redis.call('PEXPIRE', meta, reqTtl)
  return reqTtl
end
return cur
`

// Index is the Redis-backed blob liveness + lease store. Safe for concurrent use
// (go-redis clients are goroutine-safe; the Lua touch is atomic).
type Index struct {
	client *redis.Client
	touch  *redis.Script
}

// New returns an Index backed by client. The touch script is registered via
// redis.NewScript so calls use EVALSHA (falling back to EVAL on NOSCRIPT) — the
// script is loaded into Redis's cache on first use automatically.
func New(client *redis.Client) *Index {
	return &Index{
		client: client,
		touch:  redis.NewScript(touchScript),
	}
}

// Touch records/refreshes liveness for a blob: it adds the hash to the index,
// records size+createdAt the first time, and MONOTONICALLY extends the idle TTL
// toward ttl. A shorter ttl than the current remaining TTL is a no-op on the
// expiry (it never shrinks). size is bytes; it is recorded only on first sight.
func (ix *Index) Touch(ctx context.Context, hash string, size int64, ttl time.Duration) error {
	ttlMs := ttl.Milliseconds()
	if ttlMs <= 0 {
		return fmt.Errorf("blobindex: Touch ttl must be > 0, got %s", ttl)
	}
	createdAtMs := time.Now().UnixMilli()
	err := ix.touch.Run(ctx, ix.client,
		[]string{keys.BlobMetaKey(hash), keys.BlobIndex},
		hash, size, createdAtMs, ttlMs,
	).Err()
	if err != nil {
		return fmt.Errorf("blobindex: touch %q: %w", hash, err)
	}
	return nil
}

// Exists reports whether the blob's liveness meta key is present (i.e. it has not
// idled out). This is the existence check the API's /blobs/check uses and the GC
// uses to decide collectability.
func (ix *Index) Exists(ctx context.Context, hash string) (bool, error) {
	n, err := ix.client.Exists(ctx, keys.BlobMetaKey(hash)).Result()
	if err != nil {
		return false, fmt.Errorf("blobindex: exists %q: %w", hash, err)
	}
	return n > 0, nil
}

// Lease pins the blob for jobID: SADD jobID to blob:lease:<hash> and touch the
// liveness TTL (a leased blob is in active use, so its idle clock is refreshed).
// Idempotent — re-leasing the same job is a no-op on the set.
func (ix *Index) Lease(ctx context.Context, hash, jobID string, size int64, ttl time.Duration) error {
	if err := ix.client.SAdd(ctx, keys.BlobLeaseKey(hash), jobID).Err(); err != nil {
		return fmt.Errorf("blobindex: lease %q for %q: %w", hash, jobID, err)
	}
	// Touch on lease so an in-use blob never idles out mid-run. A failure here is
	// non-fatal to the lease itself (the SADD already pinned it), but surface it.
	if err := ix.Touch(ctx, hash, size, ttl); err != nil {
		return fmt.Errorf("blobindex: lease-touch %q: %w", hash, err)
	}
	return nil
}

// Release removes jobID from the blob's lease set (SREM). Idempotent: releasing a
// job that is not in the set (or a blob with no lease set) is a no-op, so it is
// safe to call on every terminal path including the once-only cleanup.
func (ix *Index) Release(ctx context.Context, hash, jobID string) error {
	if err := ix.client.SRem(ctx, keys.BlobLeaseKey(hash), jobID).Err(); err != nil {
		return fmt.Errorf("blobindex: release %q for %q: %w", hash, jobID, err)
	}
	return nil
}

// Leased reports whether the blob has any active lease (SCARD > 0). A leased blob
// is pinned: GC must never delete it regardless of TTL.
func (ix *Index) Leased(ctx context.Context, hash string) (bool, error) {
	n, err := ix.client.SCard(ctx, keys.BlobLeaseKey(hash)).Result()
	if err != nil {
		return false, fmt.Errorf("blobindex: leased %q: %w", hash, err)
	}
	return n > 0, nil
}
