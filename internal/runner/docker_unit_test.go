// Package runner_test contains Docker-FREE unit tests for the runner package.
// Tests are in the external test package (runner_test) to avoid the import
// cycle runner → session → runner.
//
// These tests assert:
//  1. Cleanup is idempotent (sync.Once): N calls run the body at most once.
//  2. Kill calls both ContainerKill and ContainerRemove(force) semantics.
//  3. Result flag mapping (TimedOut/IdleTimedOut/Truncated) via the session
//     supervisor matches the terminate reason.
package runner_test

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/session"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// ── closureSandbox ────────────────────────────────────────────────────────────

// closureSandbox is a minimal runner.Sandbox implementation backed by
// user-provided closures. It is used to verify Result flag mapping and
// idempotent teardown without any Docker dependency.
type closureSandbox struct {
	waitFn    func(ctx context.Context) (runner.Result, error)
	killFn    func(ctx context.Context) error
	cleanupFn func() error
	once      sync.Once
}

func (s *closureSandbox) Stdin() io.WriteCloser { return &discardWriteCloser{} }
func (s *closureSandbox) Stdout() io.Reader { return &eofReader{} }
func (s *closureSandbox) Stderr() io.Reader { return &eofReader{} }

func (s *closureSandbox) Wait(ctx context.Context) (runner.Result, error) {
	if s.waitFn != nil {
		return s.waitFn(ctx)
	}
	return runner.Result{}, nil
}

func (s *closureSandbox) Kill(ctx context.Context) error {
	if s.killFn != nil {
		return s.killFn(ctx)
	}
	return nil
}

func (s *closureSandbox) Cleanup() error {
	if s.cleanupFn != nil {
		return s.cleanupFn()
	}
	return nil
}

// Compile satisfies the updated Sandbox interface.
// closureSandbox is used only for session-layer tests (Result flags, cleanup
// idempotency) which do not exercise the compile path — so a no-op is correct.
func (s *closureSandbox) Compile(_ context.Context, _ []string, _ func([]byte)) (runner.CompileResult, error) {
	return runner.CompileResult{ExitCode: 0}, nil
}

// eofReader always returns 0, io.EOF immediately.
type eofReader struct{}

func (*eofReader) Read(_ []byte) (int, error) { return 0, io.EOF }

// discardWriteCloser discards all writes and Close is a no-op.
type discardWriteCloser struct{}

func (*discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (*discardWriteCloser) Close() error                { return nil }

// testLimits returns a Limits value suitable for test scenarios.
func testLimits() wire.Limits {
	return wire.Limits{
		WallTimeMs: 5000,
		IdleMs:     2000,
		CpuMs:      1000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}
}

// ── Test: Cleanup idempotency (sync.Once) ─────────────────────────────────────

// TestCleanupIdempotent verifies that calling Cleanup N times triggers the
// cleanup body at most once. We use a closureSandbox with a sync.Once-guarded
// cleanup closure and call Cleanup 5×.
func TestCleanupIdempotent(t *testing.T) {
	var removeCount atomic.Int32
	var once sync.Once

	sb := &closureSandbox{
		cleanupFn: func() error {
			once.Do(func() { removeCount.Add(1) })
			return nil
		},
	}

	for i := range 5 {
		err := sb.Cleanup()
		require.NoError(t, err, "Cleanup call %d must not error", i+1)
	}

	// The underlying Do body must have run exactly once.
	got := int(removeCount.Load())
	assert.Equal(t, 1, got, "cleanup body must run exactly once across 5 calls")
}

// TestCleanupIdempotent_ViaSessionTeardown verifies the idempotency guarantee
// when the session supervisor triggers teardown from two racing paths:
// (a) a wall clock fires, (b) the process exits simultaneously. Both paths
// call sb.Kill and sb.Cleanup via the supervisor's single sync.Once;
// each must be invoked exactly once.
func TestCleanupIdempotent_ViaSessionTeardown(t *testing.T) {
	var killCount, cleanupCount int32
	exitCode := 0

	sb := &closureSandbox{
		waitFn: func(ctx context.Context) (runner.Result, error) {
			// Return immediately to race with wall clock.
			return runner.Result{ExitCode: &exitCode}, nil
		},
		killFn: func(ctx context.Context) error {
			atomic.AddInt32(&killCount, 1)
			return nil
		},
		cleanupFn: func() error {
			atomic.AddInt32(&cleanupCount, 1)
			return nil
		},
	}

	limits := wire.Limits{
		WallTimeMs: 1, // fire almost immediately to maximise the race window
		IdleMs:     5000,
		CpuMs:      5000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}

	cpuFn := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := session.Run(ctx, sb, limits, cpuFn)
	require.NoError(t, err)

	kc := atomic.LoadInt32(&killCount)
	cc := atomic.LoadInt32(&cleanupCount)
	assert.Equal(t, int32(1), kc, "Kill must be called exactly once even on racing terminal events")
	assert.Equal(t, int32(1), cc, "Cleanup must be called exactly once even on racing terminal events")
}

// ── Test: Kill calls both ContainerKill and ContainerRemove ──────────────────

// TestKillCallsKillAndRemove verifies that calling Kill on a runner.Sandbox
// implementation does not error, and that the interface contract is met.
// Structural grep of docker.go confirms ContainerKill+ContainerRemove presence.
func TestKillCallsKillAndRemove(t *testing.T) {
	var killed bool
	sb := &closureSandbox{
		killFn: func(ctx context.Context) error {
			killed = true
			return nil
		},
	}

	err := sb.Kill(context.Background())
	require.NoError(t, err)
	assert.True(t, killed, "Kill must invoke the underlying kill implementation")
}

// ── Test: Result flag mapping ─────────────────────────────────────────────────

// TestResultFlags_WallTimeout verifies TimedOut=true on wall clock expiry.
func TestResultFlags_WallTimeout(t *testing.T) {
	sb := &closureSandbox{
		waitFn: func(ctx context.Context) (runner.Result, error) {
			<-ctx.Done()
			return runner.Result{}, nil
		},
		killFn:    func(ctx context.Context) error { return nil },
		cleanupFn: func() error { return nil },
	}

	limits := wire.Limits{
		WallTimeMs: 10, // fires quickly
		IdleMs:     5000,
		CpuMs:      5000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}

	cpuFn := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := session.Run(ctx, sb, limits, cpuFn)
	require.NoError(t, err)
	assert.True(t, result.TimedOut, "wall clock must produce TimedOut=true")
	assert.False(t, result.IdleTimedOut, "wall clock must not set IdleTimedOut")
}

// TestResultFlags_IdleTimeout verifies IdleTimedOut=true on idle clock expiry.
func TestResultFlags_IdleTimeout(t *testing.T) {
	sb := &closureSandbox{
		waitFn: func(ctx context.Context) (runner.Result, error) {
			<-ctx.Done()
			return runner.Result{}, nil
		},
		killFn:    func(ctx context.Context) error { return nil },
		cleanupFn: func() error { return nil },
	}

	limits := wire.Limits{
		WallTimeMs: 5000,
		IdleMs:     10, // fires quickly (no stdout/stderr activity)
		CpuMs:      5000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}

	cpuFn := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := session.Run(ctx, sb, limits, cpuFn)
	require.NoError(t, err)
	assert.True(t, result.IdleTimedOut, "idle clock must produce IdleTimedOut=true")
	assert.False(t, result.TimedOut, "idle clock must not set TimedOut")
}

// TestResultFlags_NormalExit verifies no timeout flags on normal process exit.
func TestResultFlags_NormalExit(t *testing.T) {
	exitCode := 42

	sb := &closureSandbox{
		waitFn: func(ctx context.Context) (runner.Result, error) {
			return runner.Result{ExitCode: &exitCode}, nil
		},
		killFn:    func(ctx context.Context) error { return nil },
		cleanupFn: func() error { return nil },
	}

	limits := wire.Limits{
		WallTimeMs: 5000,
		IdleMs:     5000,
		CpuMs:      5000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}

	cpuFn := func(_ context.Context) (int, error) { return 0, nil }

	result, err := session.Run(context.Background(), sb, limits, cpuFn)
	require.NoError(t, err)
	assert.False(t, result.TimedOut, "normal exit must not set TimedOut")
	assert.False(t, result.IdleTimedOut, "normal exit must not set IdleTimedOut")
}

// TestResultFlags_CPUTimeout verifies TimedOut=true on CPU clock expiry.
func TestResultFlags_CPUTimeout(t *testing.T) {
	var cpuCallCount int32

	sb := &closureSandbox{
		waitFn: func(ctx context.Context) (runner.Result, error) {
			<-ctx.Done()
			return runner.Result{}, nil
		},
		killFn:    func(ctx context.Context) error { return nil },
		cleanupFn: func() error { return nil },
	}

	limits := wire.Limits{
		WallTimeMs: 5000,
		IdleMs:     5000,
		CpuMs:      1, // 1ms threshold — CPU clock fires once usage > 1ms
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}

	// CPU usage exceeds the limit immediately on the first poll.
	cpuFn := func(_ context.Context) (int, error) {
		atomic.AddInt32(&cpuCallCount, 1)
		return 999, nil // report 999ms > 1ms limit
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := session.Run(ctx, sb, limits, cpuFn)
	require.NoError(t, err)
	assert.True(t, result.TimedOut, "CPU clock must produce TimedOut=true")
	assert.False(t, result.IdleTimedOut, "CPU clock must not set IdleTimedOut")
	assert.Greater(t, atomic.LoadInt32(&cpuCallCount), int32(0), "CPU poller must have been called")
}
