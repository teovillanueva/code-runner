//go:build worker_integration

// Package worker_test — scale_test.go proves the acquire-before-claim slot
// semaphore caps concurrent live sandboxes at MaxSandboxes (SCALE-02).
//
// Prerequisites (same as integration_test.go):
//
//	docker pull redis:7
//	docker build -t executor/python:3.12 languages/python-3.12/
//	docker run -d -p 6384:6379 redis:7  (or set TEST_SCALE_REDIS_URL)
//
// Run with:
//
//	go test -tags=worker_integration -timeout 300s ./internal/worker/... -run TestConcurrencyCap -v
package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/publisher"
	runnerPkg "github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/stdintransport"
	"github.com/teovillanueva/code-runner/internal/worker"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// ─────────────────────────────────────────────────────────────────────────────
// countingRunner wraps a real runner.Runner and tracks peak concurrent sandbox
// count. It increments an atomic counter on Create and decrements on the
// sandbox's Cleanup — using a wrapping sandbox that intercepts Cleanup.
// ─────────────────────────────────────────────────────────────────────────────

type countingRunner struct {
	inner    runnerPkg.Runner
	active   atomic.Int64
	peak     atomic.Int64
	peakMu   sync.Mutex // protects peak update
}

func (cr *countingRunner) Create(ctx context.Context, spec wire.JobSpec) (runnerPkg.Sandbox, error) {
	sb, err := cr.inner.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	// Increment active count and update peak.
	newActive := cr.active.Add(1)
	cr.peakMu.Lock()
	if newActive > cr.peak.Load() {
		cr.peak.Store(newActive)
	}
	cr.peakMu.Unlock()

	return &countingSandbox{inner: sb, cr: cr}, nil
}

// countingSandbox wraps runner.Sandbox and decrements the active count in
// Cleanup (exactly once — using atomic bool for idempotency).
type countingSandbox struct {
	inner   runnerPkg.Sandbox
	cr      *countingRunner
	cleaned atomic.Bool
}

var _ runnerPkg.Sandbox = (*countingSandbox)(nil)

func (cs *countingSandbox) Stdin() io.WriteCloser  { return cs.inner.Stdin() }
func (cs *countingSandbox) Stdout() io.Reader      { return cs.inner.Stdout() }
func (cs *countingSandbox) Stderr() io.Reader      { return cs.inner.Stderr() }
func (cs *countingSandbox) Kill(ctx context.Context) error { return cs.inner.Kill(ctx) }
func (cs *countingSandbox) Wait(ctx context.Context) (runnerPkg.Result, error) {
	return cs.inner.Wait(ctx)
}

func (cs *countingSandbox) Cleanup() error {
	if cs.cleaned.CompareAndSwap(false, true) {
		cs.cr.active.Add(-1)
	}
	return cs.inner.Cleanup()
}

// countingSandbox must also satisfy the worker.DockerSandbox type assertion
// (CPUReader + Limits), because the inner sandbox implements it. We delegate
// through the inner sandbox's interface. Since these are not on runner.Sandbox,
// we use a type assertion to probe and forward.
func (cs *countingSandbox) CPUReader() runnerPkg.CPUUsageFunc {
	type cpuReader interface {
		CPUReader() runnerPkg.CPUUsageFunc
	}
	if dr, ok := cs.inner.(cpuReader); ok {
		return dr.CPUReader()
	}
	return func(_ context.Context) (int, error) { return 0, nil }
}

func (cs *countingSandbox) Limits() wire.Limits {
	type limiter interface {
		Limits() wire.Limits
	}
	if l, ok := cs.inner.(limiter); ok {
		return l.Limits()
	}
	return wire.Limits{}
}

// ─────────────────────────────────────────────────────────────────────────────
// Infrastructure helpers (scale-test-specific)
// ─────────────────────────────────────────────────────────────────────────────

// scaleSeccompProfilePath returns the absolute path to the seccomp profile.
func scaleSeccompProfilePath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(filepath.Join(projectRoot, "profiles", "seccomp", "runner.json"))
	if err != nil {
		return ""
	}
	return abs
}

// dialScaleTestRedis returns a live *redis.Client on a port separate from the
// other integration tests (6384 vs 6381) so they can run concurrently.
func dialScaleTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("TEST_SCALE_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6384"
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Skipf("scale_test: cannot parse TEST_SCALE_REDIS_URL %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("scale_test: Redis unreachable at %q: %v — run: docker run -d -p 6384:6379 redis:7", rawURL, err)
	}
	return cli
}

// ─────────────────────────────────────────────────────────────────────────────
// TestConcurrencyCap proves the slot semaphore bounds live sandboxes at
// MaxSandboxes even when more jobs are enqueued.
//
// Design:
//   - MaxSandboxes = 2, enqueue 6 short-sleep Python jobs
//   - Jobs sleep 3 s so several run concurrently before any finishes
//   - A goroutine watches for "queued" stage events and dispatches "start"
//   - The counting runner wraps DockerSocketRunner and records peak active count
//   - Assertions: peak == MaxSandboxes; active == 0 after drain
//   - After all jobs drain, OwnedJobs set is empty for the worker's workerID
// ─────────────────────────────────────────────────────────────────────────────

func TestConcurrencyCap(t *testing.T) {
	const (
		MaxSandboxes = 2
		TotalJobs    = 6
	)

	redisClient := dialScaleTestRedis(t)
	defer redisClient.Close() //nolint:errcheck

	dockerCli := requireDockerAndImage(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Flush queue and owned-jobs keys to avoid cross-test pollution.
	redisClient.Del(ctx, "jobs:queue") //nolint:errcheck

	// Build the stack.
	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	cfg := config.Default()
	cfg.RedisURL = os.Getenv("TEST_SCALE_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6384"
	}

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, scaleSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	// Wrap with counting runner to instrument peak concurrency.
	cr := &countingRunner{inner: dockerRunner}

	it := newIntegrationTriggerer()
	pub := publisher.NewForTest(it)

	workerCfg := worker.Config{
		MaxSandboxes:        MaxSandboxes,
		WarmupMs:            15000,
		ClaimTimeout:        2 * time.Second,
		HeartbeatIntervalMs: 2000,
		HeartbeatTTLMs:      10000,
	}

	// Use NewWithTransport to inject the counting runner.
	w := worker.NewWithTransport(store, transport, cr, pub, workerCfg)

	// Enqueue TotalJobs Python jobs that each sleep 3 s so they overlap.
	// Using python -c to avoid file copy (read-only rootfs).
	jobIDs := make([]string, TotalJobs)
	for i := 0; i < TotalJobs; i++ {
		jobID := fmt.Sprintf("scale-cap-%d-%d", time.Now().UnixNano(), i)
		jobIDs[i] = jobID
		spec := wire.JobSpec{
			JobId:       jobID,
			Language:    "python",
			Version:     "3.12",
			Image:       "executor/python:3.12",
			Run:         []string{"python", "-c", "import time; time.sleep(3); print('done')"},
			Channel:     fmt.Sprintf("private-run-%s", jobID),
			Interactive: false,
			Files:       nil,
			Limits: wire.Limits{
				WallTimeMs: 15000,
				IdleMs:     10000,
				CpuMs:      10000,
				MemoryMb:   128,
				Pids:       64,
				OutputKb:   512,
			},
		}
		require.NoError(t, store.WriteSpec(ctx, spec))
		require.NoError(t, store.Enqueue(ctx, jobID))
	}

	t.Logf("Enqueued %d jobs, MaxSandboxes=%d", TotalJobs, MaxSandboxes)

	// Dispatch "start" messages as soon as each job reaches "queued".
	// We use a goroutine that polls the event log and sends start once per job.
	startedJobs := &sync.Map{}
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, ev := range it.allEvents() {
					if ev.event != "stage" {
						continue
					}
					var se wire.StageEvent
					if json.Unmarshal(ev.data, &se) != nil || se.Phase != wire.StagePhaseQueued {
						continue
					}
					// Extract jobID from channel name "private-run-<id>".
					const prefix = "private-run-"
					if len(ev.channel) <= len(prefix) {
						continue
					}
					jobID := ev.channel[len(prefix):]
					if _, already := startedJobs.LoadOrStore(jobID, true); already {
						continue
					}
					// Brief pause for pub/sub subscription establishment.
					time.Sleep(100 * time.Millisecond)
					startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
					if err := redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), startPayload).Err(); err != nil {
						t.Logf("scale_test: failed to publish start for %s: %v", jobID, err)
					} else {
						t.Logf("scale_test: sent start for %s", jobID)
					}
				}
			}
		}
	}()

	// Run the worker loop in a goroutine.
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.Run(ctx)
	}()

	// Wait until all TotalJobs have published a result event (all jobs done).
	var finalResultCount atomic.Int64
	ok := it.waitFor(120*time.Second, func(evs []integrationEvent) bool {
		count := 0
		for _, ev := range evs {
			if ev.event == "result" {
				count++
			}
		}
		finalResultCount.Store(int64(count))
		return count >= TotalJobs
	})

	t.Logf("scale_test: %d/%d jobs produced result events", finalResultCount.Load(), TotalJobs)
	t.Logf("scale_test: peak concurrent sandboxes = %d (MaxSandboxes = %d)", cr.peak.Load(), MaxSandboxes)
	t.Logf("scale_test: current active sandboxes after drain = %d", cr.active.Load())

	if !ok {
		t.Errorf("timed out: only %d/%d jobs completed within timeout", finalResultCount.Load(), TotalJobs)
	}

	// PRIMARY ASSERTION: peak concurrency == MaxSandboxes (cap is reached and
	// never exceeded — proving the acquire-before-claim semaphore works).
	peak := cr.peak.Load()
	assert.LessOrEqual(t, peak, int64(MaxSandboxes),
		"concurrency MUST NEVER exceed MaxSandboxes=%d (peak was %d)", MaxSandboxes, peak)
	assert.Equal(t, int64(MaxSandboxes), peak,
		"peak must reach MaxSandboxes=%d (slots are actually used, not under-utilised)", MaxSandboxes)

	// All sandboxes cleaned up.
	assert.Equal(t, int64(0), cr.active.Load(),
		"all sandboxes must be cleaned up after all jobs drain")

	// Cancel the worker and wait for it to stop.
	cancel()
	select {
	case <-workerDone:
	case <-time.After(15 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	// Assert OwnedJobs set is empty for this worker's ID.
	workerID := w.WorkerIDForTest()
	bgCtx := context.Background()
	remainingJobs, err := store.OwnedJobs(bgCtx, workerID)
	require.NoError(t, err, "OwnedJobs query must not fail")
	assert.Empty(t, remainingJobs,
		"worker owned-jobs Redis set must be empty after drain; remaining: %v", remainingJobs)

	// Log all events for debugging.
	t.Logf("scale_test: workerID = %s", workerID)
	allEvs := it.allEvents()
	t.Logf("scale_test: total events = %d", len(allEvs))
}
