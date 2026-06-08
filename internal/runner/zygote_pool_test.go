package runner

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teovillanueva/code-runner/internal/config"
)

// fakeBackend is an in-memory poolBackend for unit-testing pool routing /
// get-or-create / reap / respawn without Docker.
type fakeBackend struct {
	mu sync.Mutex

	launchCalls  int
	removeCalls  int
	launchErr    error
	healthyState map[poolKey]bool // key -> reported health (default true)

	// per-launch IP suffix so each launched parent has a distinct address.
	nextIP int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{healthyState: map[poolKey]bool{}}
}

func (f *fakeBackend) launchParent(_ context.Context, key poolKey, _ string, relayPort int) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.launchErr != nil {
		return "", "", f.launchErr
	}
	f.launchCalls++
	f.nextIP++
	id := key.String() + "-c" + itoa(f.launchCalls)
	ip := "10.0.0." + itoa(f.nextIP)
	f.healthyState[key] = true
	return id, ip, nil
}

func (f *fakeBackend) healthy(_ context.Context, p *poolParent) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.healthyState[p.key]
	return !ok || h
}

func (f *fakeBackend) removeParent(_ context.Context, _ *poolParent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	return nil
}

func (f *fakeBackend) setHealthy(key poolKey, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthyState[key] = v
}

func (f *fakeBackend) counts() (launch, remove int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.launchCalls, f.removeCalls
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// fakeDialer returns a net.Conn (one end of net.Pipe) and parks the agent end so
// the conn is "connected" for routing tests. failAddrs makes the listed addrs
// fail (dead-parent simulation).
type fakeDialer struct {
	mu        sync.Mutex
	failAddrs map[string]bool
	dialed    []string
	parked    []net.Conn // keep agent ends alive
}

func newFakeDialer() *fakeDialer { return &fakeDialer{failAddrs: map[string]bool{}} }

func (d *fakeDialer) dial(addr string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialed = append(d.dialed, addr)
	if d.failAddrs[addr] {
		return nil, errors.New("dial refused (dead parent)")
	}
	w, a := net.Pipe()
	d.parked = append(d.parked, a)
	return w, nil
}

func (d *fakeDialer) closeAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.parked {
		_ = c.Close()
	}
}

func testCfg() config.Config {
	cfg := config.Default()
	cfg.ZygoteEnabled = true
	cfg.ZygoteRelayPort = 7000
	cfg.ZygotePoolIdleMs = 60000
	return cfg
}

// TestPoolGetOrCreateLazyAndReuse: first dial launches a parent; subsequent
// dials for the same key reuse it (no extra launch).
func TestPoolGetOrCreateLazyAndReuse(t *testing.T) {
	be := newFakeBackend()
	dl := newFakeDialer()
	pm := newPoolManager(testCfg(), be, dl.dial)
	defer pm.close()
	defer dl.closeAll()

	key := poolKey{language: "python", version: "3.12"}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		rc, release, err := pm.dial(ctx, key, "executor/python:3.12")
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = rc.close()
		release()
	}

	launch, _ := be.counts()
	if launch != 1 {
		t.Errorf("launchCalls = %d, want 1 (lazy create + reuse)", launch)
	}
}

// TestPoolPerKeyParents: distinct (language,version) keys get distinct parents.
func TestPoolPerKeyParents(t *testing.T) {
	be := newFakeBackend()
	dl := newFakeDialer()
	pm := newPoolManager(testCfg(), be, dl.dial)
	defer pm.close()
	defer dl.closeAll()

	ctx := context.Background()
	keys := []poolKey{
		{"python", "3.12"},
		{"r", "4.4"},
		{"python", "3.13"},
	}
	for _, k := range keys {
		rc, release, err := pm.dial(ctx, k, "img")
		if err != nil {
			t.Fatalf("dial %s: %v", k, err)
		}
		_ = rc.close()
		release()
	}
	launch, _ := be.counts()
	if launch != 3 {
		t.Errorf("launchCalls = %d, want 3 (one parent per key)", launch)
	}
}

// TestPoolDeadParentRespawn: an unhealthy parent is removed and a new one
// launched on the next request (POOL-04).
func TestPoolDeadParentRespawn(t *testing.T) {
	be := newFakeBackend()
	dl := newFakeDialer()
	pm := newPoolManager(testCfg(), be, dl.dial)
	defer pm.close()
	defer dl.closeAll()

	ctx := context.Background()
	key := poolKey{language: "python", version: "3.12"}

	rc, release, err := pm.dial(ctx, key, "img")
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	_ = rc.close()
	release()

	// Mark the parent unhealthy → next getOrCreate must drop + respawn.
	be.setHealthy(key, false)

	rc, release, err = pm.dial(ctx, key, "img")
	if err != nil {
		t.Fatalf("respawn dial: %v", err)
	}
	_ = rc.close()
	release()

	launch, remove := be.counts()
	if launch != 2 {
		t.Errorf("launchCalls = %d, want 2 (initial + respawn)", launch)
	}
	if remove < 1 {
		t.Errorf("removeCalls = %d, want >= 1 (dead parent torn down)", remove)
	}
}

// TestPoolDialFailureRemovesParent: a dial failure (agent unreachable mid-life)
// drops the parent so the next request respawns, and the in-flight reservation
// is rolled back (no slot leak).
func TestPoolDialFailureRemovesParent(t *testing.T) {
	be := newFakeBackend()
	dl := newFakeDialer()
	pm := newPoolManager(testCfg(), be, dl.dial)
	defer pm.close()
	defer dl.closeAll()

	ctx := context.Background()
	key := poolKey{language: "python", version: "3.12"}

	// Make the FIRST launched parent's address fail to dial.
	// The fake backend assigns 10.0.0.1:7000 to the first launch.
	dl.mu.Lock()
	dl.failAddrs["10.0.0.1:7000"] = true
	dl.mu.Unlock()

	_, _, err := pm.dial(ctx, key, "img")
	if err == nil {
		t.Fatal("expected dial failure error, got nil")
	}

	// Parent must have been dropped — verify in-flight is back to 0 and a respawn
	// happens next (with a fresh IP that does NOT fail).
	rc, release, err := pm.dial(ctx, key, "img")
	if err != nil {
		t.Fatalf("respawn dial after failure: %v", err)
	}
	_ = rc.close()
	release()

	launch, remove := be.counts()
	if launch != 2 {
		t.Errorf("launchCalls = %d, want 2 (failed parent + respawn)", launch)
	}
	if remove < 1 {
		t.Errorf("removeCalls = %d, want >= 1 (failed parent removed)", remove)
	}
}

// TestPoolIdleReap: a parent with no in-flight jobs is reaped after the idle
// window (POOL-03).
func TestPoolIdleReap(t *testing.T) {
	cfg := testCfg()
	cfg.ZygotePoolIdleMs = 40 // tiny idle window for the test
	be := newFakeBackend()
	dl := newFakeDialer()
	pm := newPoolManager(cfg, be, dl.dial)
	defer pm.close()
	defer dl.closeAll()

	ctx := context.Background()
	key := poolKey{language: "python", version: "3.12"}
	rc, release, err := pm.dial(ctx, key, "img")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = rc.close()
	release() // job done → in-flight 0, eligible for reap after idle window

	// Wait for the reaper to fire (tick is bounded to >= 1s? No — tick = idle/4
	// floored to 1s). The reaper minimum tick is 1s, so wait > 1s + idle.
	deadline := time.Now().Add(3 * time.Second)
	var remove int
	for time.Now().Before(deadline) {
		_, remove = be.counts()
		if remove >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if remove < 1 {
		t.Errorf("idle reaper did not remove the parent (removeCalls=%d)", remove)
	}

	// After reap, a new dial relaunches.
	rc, release, err = pm.dial(ctx, key, "img")
	if err != nil {
		t.Fatalf("dial after reap: %v", err)
	}
	_ = rc.close()
	release()
	launch, _ := be.counts()
	if launch != 2 {
		t.Errorf("launchCalls = %d, want 2 (initial + post-reap relaunch)", launch)
	}
}

// TestPoolInFlightNotReaped: a parent with a live job is NOT reaped even past
// the idle window.
func TestPoolInFlightNotReaped(t *testing.T) {
	cfg := testCfg()
	cfg.ZygotePoolIdleMs = 40
	be := newFakeBackend()
	dl := newFakeDialer()
	pm := newPoolManager(cfg, be, dl.dial)
	defer pm.close()
	defer dl.closeAll()

	ctx := context.Background()
	key := poolKey{language: "python", version: "3.12"}
	rc, release, err := pm.dial(ctx, key, "img")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Do NOT release — the job is "in flight".
	defer func() { _ = rc.close(); release() }()

	time.Sleep(2 * time.Second) // longer than idle + a reaper tick
	_, remove := be.counts()
	if remove != 0 {
		t.Errorf("removeCalls = %d, want 0 (in-flight parent must not be reaped)", remove)
	}
}

// TestPoolConcurrentDialSingleLaunch: many concurrent first-dials for the same
// key launch the parent exactly once.
func TestPoolConcurrentDialSingleLaunch(t *testing.T) {
	be := newFakeBackend()
	dl := newFakeDialer()
	pm := newPoolManager(testCfg(), be, dl.dial)
	defer pm.close()
	defer dl.closeAll()

	ctx := context.Background()
	key := poolKey{language: "python", version: "3.12"}

	var wg sync.WaitGroup
	var errCount atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, release, err := pm.dial(ctx, key, "img")
			if err != nil {
				errCount.Add(1)
				return
			}
			_ = rc.close()
			release()
		}()
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Errorf("%d concurrent dials errored", errCount.Load())
	}
	launch, _ := be.counts()
	if launch != 1 {
		t.Errorf("launchCalls = %d, want 1 (single-winner launch under concurrency)", launch)
	}
}

// TestPoolClose tears down all parents and stops the reaper.
func TestPoolClose(t *testing.T) {
	be := newFakeBackend()
	dl := newFakeDialer()
	pm := newPoolManager(testCfg(), be, dl.dial)
	defer dl.closeAll()

	ctx := context.Background()
	for _, k := range []poolKey{{"python", "3.12"}, {"r", "4.4"}} {
		rc, release, err := pm.dial(ctx, k, "img")
		if err != nil {
			t.Fatalf("dial %s: %v", k, err)
		}
		_ = rc.close()
		release()
	}
	if err := pm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, remove := be.counts()
	if remove != 2 {
		t.Errorf("removeCalls after close = %d, want 2", remove)
	}
	// Second close is a no-op (idempotent reaperStop).
	if err := pm.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
