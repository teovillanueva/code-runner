//go:build redis_integration

// Package blobindex integration tests against a real Redis. They prove the
// monotonic touch-on-use (a shorter touch never shrinks the TTL) and the lease
// add/remove lifecycle.
//
// Two-gate guard (mirrors internal/worker/integration_test.go):
//   - Build tag `redis_integration` excludes these from `go test ./...`.
//   - A runtime skip when TEST_REDIS_URL is unset or Redis is unreachable.
//
// Run via:
//
//	docker run -d -p 6380:6379 redis:7
//	TEST_REDIS_URL=redis://localhost:6380 go test -tags=redis_integration ./internal/blobindex/... -v
package blobindex

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/teovillanueva/code-runner/internal/keys"
)

func requireRedis(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("redis_integration: TEST_REDIS_URL unset — run: docker run -d -p 6380:6379 redis:7")
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Skipf("redis_integration: cannot parse TEST_REDIS_URL %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		t.Skipf("redis_integration: Redis unreachable at %q: %v — run: docker run -d -p 6380:6379 redis:7", rawURL, err)
	}
	return cli
}

// cleanup removes every blob key this test touched so reruns start clean.
func cleanup(ctx context.Context, t *testing.T, cli *redis.Client, hash string) {
	t.Helper()
	cli.Del(ctx, keys.BlobMetaKey(hash), keys.BlobLeaseKey(hash)) //nolint:errcheck
	cli.SRem(ctx, keys.BlobIndex, hash)                           //nolint:errcheck
}

func testHash(suffix string) string {
	// 64 hex chars — pad the suffix.
	h := suffix
	for len(h) < 64 {
		h += "0"
	}
	return "sha256:" + h[:64]
}

// TestTouchMonotonic proves a SHORTER touch does not shrink an existing longer
// TTL, while a LONGER touch extends it.
func TestTouchMonotonic(t *testing.T) {
	cli := requireRedis(t)
	ix := New(cli)
	ctx := context.Background()
	hash := testHash("aaaa")
	cleanup(ctx, t, cli, hash)
	defer cleanup(ctx, t, cli, hash)

	// First touch: 100s TTL.
	if err := ix.Touch(ctx, hash, 123, 100*time.Second); err != nil {
		t.Fatalf("Touch (100s): %v", err)
	}
	// PTTL().Result() returns a time.Duration. Compare against durations.
	pttl1, err := cli.PTTL(ctx, keys.BlobMetaKey(hash)).Result()
	if err != nil {
		t.Fatalf("PTTL after first touch: %v", err)
	}
	if pttl1 <= 0 || pttl1 > 100*time.Second {
		t.Fatalf("PTTL after 100s touch = %v; want ~100s", pttl1)
	}

	// Metadata recorded.
	size, err := cli.HGet(ctx, keys.BlobMetaKey(hash), "size").Result()
	if err != nil || size != "123" {
		t.Fatalf("meta size = %q, err=%v; want 123", size, err)
	}

	// Shorter touch: 10s. Must NOT shrink the TTL (still ~100s).
	if err := ix.Touch(ctx, hash, 999, 10*time.Second); err != nil {
		t.Fatalf("Touch (10s): %v", err)
	}
	pttl2, err := cli.PTTL(ctx, keys.BlobMetaKey(hash)).Result()
	if err != nil {
		t.Fatalf("PTTL after shorter touch: %v", err)
	}
	if pttl2 < 90*time.Second {
		t.Fatalf("shorter touch SHRANK the TTL to %v; monotonic invariant violated", pttl2)
	}

	// size must NOT have been rewritten by the second touch (HSETNX).
	size2, err := cli.HGet(ctx, keys.BlobMetaKey(hash), "size").Result()
	if err != nil || size2 != "123" {
		t.Fatalf("meta size after re-touch = %q; want unchanged 123 (HSETNX)", size2)
	}

	// Longer touch: 300s. Must EXTEND.
	if err := ix.Touch(ctx, hash, 123, 300*time.Second); err != nil {
		t.Fatalf("Touch (300s): %v", err)
	}
	pttl3, err := cli.PTTL(ctx, keys.BlobMetaKey(hash)).Result()
	if err != nil {
		t.Fatalf("PTTL after longer touch: %v", err)
	}
	if pttl3 < 200*time.Second {
		t.Fatalf("longer touch did not extend TTL: %v; want ~300s", pttl3)
	}

	// Hash is in the index.
	member, err := cli.SIsMember(ctx, keys.BlobIndex, hash).Result()
	if err != nil || !member {
		t.Fatalf("hash not in blobs:index after touch (member=%v, err=%v)", member, err)
	}

	// Exists reports true.
	ex, err := ix.Exists(ctx, hash)
	if err != nil || !ex {
		t.Fatalf("Exists = %v, err=%v; want true", ex, err)
	}
}

// TestLeaseLifecycle proves Lease pins (Leased=true) and Release unpins
// (Leased=false), and that Release is idempotent.
func TestLeaseLifecycle(t *testing.T) {
	cli := requireRedis(t)
	ix := New(cli)
	ctx := context.Background()
	hash := testHash("bbbb")
	cleanup(ctx, t, cli, hash)
	defer cleanup(ctx, t, cli, hash)

	leased, err := ix.Leased(ctx, hash)
	if err != nil || leased {
		t.Fatalf("Leased (initial) = %v, err=%v; want false", leased, err)
	}

	if err := ix.Lease(ctx, hash, "job-1", 10, 60*time.Second); err != nil {
		t.Fatalf("Lease job-1: %v", err)
	}
	if err := ix.Lease(ctx, hash, "job-2", 10, 60*time.Second); err != nil {
		t.Fatalf("Lease job-2: %v", err)
	}
	leased, err = ix.Leased(ctx, hash)
	if err != nil || !leased {
		t.Fatalf("Leased (after 2 leases) = %v, err=%v; want true", leased, err)
	}

	// Lease also touches liveness — meta must exist with a TTL.
	pttl, err := cli.PTTL(ctx, keys.BlobMetaKey(hash)).Result()
	if err != nil || pttl <= 0 {
		t.Fatalf("lease did not touch liveness: PTTL=%v err=%v", pttl, err)
	}

	// Release one — still leased by the other.
	if err := ix.Release(ctx, hash, "job-1"); err != nil {
		t.Fatalf("Release job-1: %v", err)
	}
	leased, err = ix.Leased(ctx, hash)
	if err != nil || !leased {
		t.Fatalf("Leased (after releasing 1 of 2) = %v; want true", leased)
	}

	// Release the second — now unleased.
	if err := ix.Release(ctx, hash, "job-2"); err != nil {
		t.Fatalf("Release job-2: %v", err)
	}
	// Idempotent re-release of an already-released job.
	if err := ix.Release(ctx, hash, "job-2"); err != nil {
		t.Fatalf("Release job-2 (idempotent): %v", err)
	}
	leased, err = ix.Leased(ctx, hash)
	if err != nil || leased {
		t.Fatalf("Leased (after releasing both) = %v; want false", leased)
	}
}
