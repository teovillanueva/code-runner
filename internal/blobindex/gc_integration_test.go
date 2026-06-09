//go:build redis_integration

// Package blobindex GC integration tests against a real Redis with an in-memory
// fake BlobRemover. They prove: a candidate within grace is NOT deleted; past
// grace it IS deleted; a recovered (re-touched) blob is dropped from candidates
// and survives; and a leased expired blob is never deleted.
//
// Run via:
//
//	docker run -d -p 6380:6379 redis:7
//	TEST_REDIS_URL=redis://localhost:6380 go test -tags=redis_integration ./internal/blobindex/... -v
package blobindex

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/teovillanueva/code-runner/internal/keys"
)

// fakeRemover records which hashes were removed and reports a fixed size.
type fakeRemover struct {
	mu      sync.Mutex
	removed map[string]bool
	sizes   map[string]int64
}

func newFakeRemover() *fakeRemover {
	return &fakeRemover{removed: map[string]bool{}, sizes: map[string]int64{}}
}

func (f *fakeRemover) Remove(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed[hash] = true
	return nil
}

func (f *fakeRemover) Stat(_ context.Context, hash string) (bool, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sizes[hash]; ok {
		return true, s, nil
	}
	return true, 0, nil
}

func (f *fakeRemover) wasRemoved(hash string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removed[hash]
}

// seedCandidate puts a hash into the index with a given candidate first-seen
// score (unix ms) and NO meta key (expired) and NO lease (unleased) — i.e. it is
// collectable, first-seen at scoreMs.
func seedCandidate(ctx context.Context, cli *redis.Client, hash string, scoreMs int64) {
	cli.SAdd(ctx, keys.BlobIndex, hash)                                                  //nolint:errcheck
	cli.ZAdd(ctx, keys.BlobGCCandidates, redis.Z{Score: float64(scoreMs), Member: hash}) //nolint:errcheck
}

func cleanGC(ctx context.Context, cli *redis.Client, hashes ...string) {
	for _, h := range hashes {
		cli.SRem(ctx, keys.BlobIndex, h)        //nolint:errcheck
		cli.ZRem(ctx, keys.BlobGCCandidates, h) //nolint:errcheck
		cli.Del(ctx, keys.BlobMetaKey(h))       //nolint:errcheck
		cli.Del(ctx, keys.BlobLeaseKey(h))      //nolint:errcheck
	}
	cli.Del(ctx, keys.BlobGCLock) //nolint:errcheck
}

// TestGCGraceWindow: within grace → kept; past grace → deleted.
func TestGCGraceWindow(t *testing.T) {
	cli := requireRedis(t)
	ctx := context.Background()
	rem := newFakeRemover()
	grace := 30 * time.Minute
	gc := NewGC(cli, rem, grace, time.Minute)

	within := testHash("c111") // first-seen 5 min ago → within 30m grace
	past := testHash("c222")   // first-seen 60 min ago → past 30m grace
	cleanGC(ctx, cli, within, past)
	defer cleanGC(ctx, cli, within, past)

	now := time.Now().UnixMilli()
	seedCandidate(ctx, cli, within, now-5*60*1000)
	seedCandidate(ctx, cli, past, now-60*60*1000)
	rem.sizes[past] = 4096

	reclaimed, bytes, err := gc.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if reclaimed != 1 {
		t.Fatalf("reclaimed = %d; want 1 (only the past-grace blob)", reclaimed)
	}
	if bytes != 4096 {
		t.Fatalf("bytesFreed = %d; want 4096", bytes)
	}
	if rem.wasRemoved(within) {
		t.Error("within-grace blob was deleted (should be kept)")
	}
	if !rem.wasRemoved(past) {
		t.Error("past-grace blob was NOT deleted (should be reclaimed)")
	}
	// Past-grace blob scrubbed from index + candidates.
	if n, _ := cli.SIsMember(ctx, keys.BlobIndex, past).Result(); n {
		t.Error("past-grace blob still in index after reclaim")
	}
	// Within-grace blob still tracked.
	if n, _ := cli.SIsMember(ctx, keys.BlobIndex, within).Result(); !n {
		t.Error("within-grace blob dropped from index prematurely")
	}
}

// TestGCFirstSightAddsCandidate: a collectable blob NOT yet a candidate is added
// to the candidates ZSET (scored now) and NOT deleted on this sweep.
func TestGCFirstSightAddsCandidate(t *testing.T) {
	cli := requireRedis(t)
	ctx := context.Background()
	rem := newFakeRemover()
	gc := NewGC(cli, rem, 30*time.Minute, time.Minute)

	h := testHash("c333")
	cleanGC(ctx, cli, h)
	defer cleanGC(ctx, cli, h)
	// In index, meta expired, unleased, but NOT yet a candidate.
	cli.SAdd(ctx, keys.BlobIndex, h) //nolint:errcheck

	reclaimed, _, err := gc.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if reclaimed != 0 {
		t.Fatalf("reclaimed = %d; want 0 (first sight only records candidacy)", reclaimed)
	}
	if rem.wasRemoved(h) {
		t.Error("blob deleted on first sight (must wait for grace)")
	}
	if _, err := cli.ZScore(ctx, keys.BlobGCCandidates, h).Result(); err != nil {
		t.Errorf("blob not recorded as a candidate on first sight: %v", err)
	}
}

// TestGCRecoveredBlobSurvives: a candidate that is re-touched (meta exists again)
// is removed from candidates and never deleted.
func TestGCRecoveredBlobSurvives(t *testing.T) {
	cli := requireRedis(t)
	ctx := context.Background()
	rem := newFakeRemover()
	gc := NewGC(cli, rem, 30*time.Minute, time.Minute)

	h := testHash("c444")
	cleanGC(ctx, cli, h)
	defer cleanGC(ctx, cli, h)

	now := time.Now().UnixMilli()
	// Was a past-grace candidate...
	seedCandidate(ctx, cli, h, now-60*60*1000)
	// ...but it RECOVERED: meta exists again (re-touched).
	cli.HSet(ctx, keys.BlobMetaKey(h), "size", "1") //nolint:errcheck
	cli.Expire(ctx, keys.BlobMetaKey(h), time.Hour) //nolint:errcheck

	reclaimed, _, err := gc.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if reclaimed != 0 {
		t.Fatalf("reclaimed = %d; want 0 (recovered blob must survive)", reclaimed)
	}
	if rem.wasRemoved(h) {
		t.Error("recovered blob was deleted")
	}
	// Candidacy dropped (it recovered).
	if _, err := cli.ZScore(ctx, keys.BlobGCCandidates, h).Result(); err != redis.Nil {
		t.Errorf("recovered blob still a candidate (want dropped): %v", err)
	}
}

// TestGCLeasedExpiredNeverDeleted: an expired (meta gone) but LEASED blob is
// pinned — never deleted even past grace.
func TestGCLeasedExpiredNeverDeleted(t *testing.T) {
	cli := requireRedis(t)
	ctx := context.Background()
	rem := newFakeRemover()
	gc := NewGC(cli, rem, 30*time.Minute, time.Minute)

	h := testHash("c555")
	cleanGC(ctx, cli, h)
	defer cleanGC(ctx, cli, h)

	now := time.Now().UnixMilli()
	// Past-grace candidate, meta gone... but a job holds a lease.
	seedCandidate(ctx, cli, h, now-60*60*1000)
	cli.SAdd(ctx, keys.BlobLeaseKey(h), "job-running") //nolint:errcheck

	reclaimed, _, err := gc.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if reclaimed != 0 {
		t.Fatalf("reclaimed = %d; want 0 (leased blob is pinned)", reclaimed)
	}
	if rem.wasRemoved(h) {
		t.Error("LEASED expired blob was deleted — lease pin violated")
	}
	// Leased blob is dropped from candidates (it is not collectable while pinned).
	if _, err := cli.ZScore(ctx, keys.BlobGCCandidates, h).Result(); err != redis.Nil {
		t.Errorf("leased blob still a candidate (want dropped): %v", err)
	}
}

// TestGCLockHeldSkips: when the lock is already held, Sweep returns (0,0,nil).
func TestGCLockHeldSkips(t *testing.T) {
	cli := requireRedis(t)
	ctx := context.Background()
	gc := NewGC(cli, newFakeRemover(), 30*time.Minute, time.Minute)

	// Pre-hold the lock as if another replica is sweeping.
	cli.Set(ctx, keys.BlobGCLock, "1", time.Minute) //nolint:errcheck
	defer cli.Del(ctx, keys.BlobGCLock)             //nolint:errcheck

	reclaimed, bytes, err := gc.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if reclaimed != 0 || bytes != 0 {
		t.Fatalf("Sweep with held lock = (%d,%d); want (0,0)", reclaimed, bytes)
	}
}
