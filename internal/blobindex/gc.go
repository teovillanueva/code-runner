package blobindex

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/teovillanueva/code-runner/internal/keys"
)

// BlobRemover is the subset of blobstore.BlobStore the GC needs to delete bytes.
// *blobstore.S3Store satisfies it. Declared here so the GC depends only on the
// method it uses (and tests can inject a fake remover).
type BlobRemover interface {
	Remove(ctx context.Context, hash string) error
	// Stat lets the GC report reclaimed bytes; a Stat error is non-fatal (the
	// delete still proceeds, bytes reported as 0).
	Stat(ctx context.Context, hash string) (exists bool, size int64, err error)
}

// GC reclaims blobs whose liveness has expired AND that are unleased, respecting
// a grace window so a blob about to be re-used is not deleted out from under it.
// Exactly one worker replica runs a sweep at a time (blobs:gc:lock, SET NX PX).
//
// Collectability state machine (per hash in blobs:index):
//   - meta EXISTS or leased  → NOT collectable. Remove from the candidates ZSET
//     if present (it recovered).
//   - meta GONE and unleased → collectable. If not yet a candidate, add it to the
//     ZSET scored by now (first-seen-collectable). If already a candidate and
//     now - score > grace → delete the S3 object + scrub all keys. Otherwise wait.
type GC struct {
	client *redis.Client
	store  BlobRemover
	grace  time.Duration
	// lockTTL bounds how long the GC lock is held if a sweep crashes mid-run.
	lockTTL time.Duration
}

// NewGC builds a GC. grace is the window a blob must stay collectable before
// deletion. The lock TTL is derived from grace/interval by the caller via the
// sweep cadence; here we use a conservative fixed lock TTL passed by the worker.
func NewGC(client *redis.Client, store BlobRemover, grace, lockTTL time.Duration) *GC {
	if lockTTL <= 0 {
		lockTTL = 5 * time.Minute
	}
	return &GC{client: client, store: store, grace: grace, lockTTL: lockTTL}
}

// Run ticks every interval, running one Sweep per tick until ctx is cancelled.
// It is the long-lived goroutine started in apps/worker/main.go when a blob
// store is configured.
func (g *GC) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reclaimed, bytes, err := g.Sweep(ctx)
			if err != nil {
				slog.Warn("blob GC: sweep error", "err", err)
				continue
			}
			if reclaimed > 0 {
				// No silent reclaim: always log what was deleted.
				slog.Info("blob GC: reclaimed blobs", "count", reclaimed, "bytes", bytes)
			}
		}
	}
}

// Sweep runs exactly one lock-guarded GC pass and returns the number of blobs
// reclaimed and the total bytes freed. If the lock is held by another replica it
// returns (0, 0, nil) — a skipped sweep is not an error.
func (g *GC) Sweep(ctx context.Context) (reclaimed int, bytesFreed int64, err error) {
	// Acquire the singleton sweep lock (SET NX PX). Skip if another replica holds it.
	ok, err := g.client.SetNX(ctx, keys.BlobGCLock, "1", g.lockTTL).Result()
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, nil // another replica is sweeping
	}
	defer g.client.Del(ctx, keys.BlobGCLock) //nolint:errcheck

	hashes, err := g.client.SMembers(ctx, keys.BlobIndex).Result()
	if err != nil {
		return 0, 0, err
	}

	nowMs := time.Now().UnixMilli()
	graceMs := g.grace.Milliseconds()

	for _, hash := range hashes {
		// EXISTS meta? (liveness not expired)
		metaN, mErr := g.client.Exists(ctx, keys.BlobMetaKey(hash)).Result()
		if mErr != nil {
			slog.Warn("blob GC: EXISTS meta failed", "hash", hash, "err", mErr)
			continue
		}
		// Leased? (SCARD > 0 ⇒ pinned)
		leaseN, lErr := g.client.SCard(ctx, keys.BlobLeaseKey(hash)).Result()
		if lErr != nil {
			slog.Warn("blob GC: SCARD lease failed", "hash", hash, "err", lErr)
			continue
		}

		collectable := metaN == 0 && leaseN == 0
		if !collectable {
			// Recovered (re-touched or re-leased): drop any stale candidacy.
			g.client.ZRem(ctx, keys.BlobGCCandidates, hash) //nolint:errcheck
			continue
		}

		// Collectable. Has it been collectable long enough?
		score, zErr := g.client.ZScore(ctx, keys.BlobGCCandidates, hash).Result()
		switch {
		case zErr == redis.Nil:
			// First time seen collectable — record first-seen timestamp, wait for grace.
			g.client.ZAdd(ctx, keys.BlobGCCandidates, redis.Z{Score: float64(nowMs), Member: hash}) //nolint:errcheck
		case zErr != nil:
			slog.Warn("blob GC: ZScore failed", "hash", hash, "err", zErr)
		default:
			if nowMs-int64(score) > graceMs {
				// Past the grace window — reclaim. Report bytes best-effort first.
				var freed int64
				if exists, size, sErr := g.store.Stat(ctx, hash); sErr == nil && exists {
					freed = size
				}
				if rErr := g.store.Remove(ctx, hash); rErr != nil {
					slog.Warn("blob GC: remove object failed", "hash", hash, "err", rErr)
					continue // leave it as a candidate; retry next sweep
				}
				// Scrub all Redis state for the hash (idempotent).
				g.client.SRem(ctx, keys.BlobIndex, hash)        //nolint:errcheck
				g.client.ZRem(ctx, keys.BlobGCCandidates, hash) //nolint:errcheck
				g.client.Del(ctx, keys.BlobLeaseKey(hash))      //nolint:errcheck
				g.client.Del(ctx, keys.BlobMetaKey(hash))       //nolint:errcheck
				reclaimed++
				bytesFreed += freed
			}
		}
	}
	return reclaimed, bytesFreed, nil
}
