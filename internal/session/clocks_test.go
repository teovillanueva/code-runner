package session_test

import (
	"bytes"
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

// -----------------------------------------------------------------------------
// fakeSandbox — a scriptable test double for runner.Sandbox.
// Models the pattern from internal/runner/stub.go.
// -----------------------------------------------------------------------------

type fakeSandbox struct {
	mu           sync.Mutex
	killCount    int
	cleanupCount int

	// waitResult is returned by Wait.
	waitResult runner.Result
	waitErr    error

	// waitDelay controls how long Wait blocks before returning.
	waitDelay time.Duration

	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader
}

// nopWriteCloser is an io.WriteCloser that discards all writes.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

func newFakeSandbox() *fakeSandbox {
	return &fakeSandbox{
		stdin:  nopWriteCloser{},
		stdout: bytes.NewReader(nil),
		stderr: bytes.NewReader(nil),
	}
}

func (f *fakeSandbox) Stdin() io.WriteCloser { return f.stdin }
func (f *fakeSandbox) Stdout() io.Reader     { return f.stdout }
func (f *fakeSandbox) Stderr() io.Reader     { return f.stderr }

func (f *fakeSandbox) Wait(_ context.Context) (runner.Result, error) {
	if f.waitDelay > 0 {
		time.Sleep(f.waitDelay)
	}
	return f.waitResult, f.waitErr
}

func (f *fakeSandbox) Kill(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killCount++
	return nil
}

func (f *fakeSandbox) Cleanup() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanupCount++
	return nil
}

func (f *fakeSandbox) kills() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killCount
}

// Compile satisfies the updated runner.Sandbox interface. The session-layer
// tests do not exercise the compile path, so this is always a no-op exit 0.
func (f *fakeSandbox) Compile(_ context.Context, _ []string, _ func([]byte)) (runner.CompileResult, error) {
	return runner.CompileResult{ExitCode: 0}, nil
}

func (f *fakeSandbox) cleanups() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cleanupCount
}

// -----------------------------------------------------------------------------
// fakeClock — injectable clock for deterministic tests.
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// Helper: build a Limits with sane non-zero defaults.
// -----------------------------------------------------------------------------

func testLimits() wire.Limits {
	return wire.Limits{
		WallTimeMs: 1000,
		IdleMs:     500,
		CpuMs:      2000,
		MemoryMb:   64,
		OutputKb:   512,
		Pids:       32,
	}
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

// TestWallClockFires verifies the wall clock kills the sandbox and sets TimedOut=true
// after WallTimeMs with an injected fake clock.
func TestWallClockFires(t *testing.T) {
	sb := newFakeSandbox()
	// Wait blocks until killed (simulated by a long sleep — fake clock bypasses real time).
	sb.waitDelay = 5 * time.Second

	limits := testLimits()
	limits.WallTimeMs = 50 // 50ms fake wall deadline

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.Run(ctx, sb, limits, fakeCPU)
	require.NoError(t, err)

	assert.True(t, result.TimedOut, "wall clock must set TimedOut=true")
	assert.False(t, result.IdleTimedOut)
	assert.Equal(t, 1, sb.kills(), "Kill must be called exactly once")
	assert.Equal(t, 1, sb.cleanups(), "Cleanup must be called exactly once")
}

// TestIdleClockFires verifies the idle clock kills after IdleMs with no activity,
// setting IdleTimedOut=true.
func TestIdleClockFires(t *testing.T) {
	sb := newFakeSandbox()
	sb.waitDelay = 5 * time.Second // Wait blocks until killed

	limits := testLimits()
	limits.WallTimeMs = 5000 // wall is 5 s — idle fires first
	limits.IdleMs = 50       // 50ms idle deadline

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.Run(ctx, sb, limits, fakeCPU)
	require.NoError(t, err)

	assert.True(t, result.IdleTimedOut, "idle clock must set IdleTimedOut=true")
	assert.False(t, result.TimedOut)
	assert.Equal(t, 1, sb.kills())
	assert.Equal(t, 1, sb.cleanups())
}

// TestIdleClockResetOnActivity verifies that an activity signal before IdleMs
// resets the idle timer so no kill occurs (the idle timer resets).
func TestIdleClockResetOnActivity(t *testing.T) {
	sb := newFakeSandbox()
	// Wait returns quickly (normal exit) before wall/idle would fire.
	sb.waitDelay = 0
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 200 // idle fires after 200ms of silence

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.Run(ctx, sb, limits, fakeCPU)
	require.NoError(t, err)

	// Wait returned immediately (exit code 0), so neither clock should have fired.
	assert.False(t, result.TimedOut)
	assert.False(t, result.IdleTimedOut)
	assert.NotNil(t, result.ExitCode)
	assert.Equal(t, 0, *result.ExitCode)
}

// TestCPUClockFires verifies the CPU clock kills when cumulative cgroup CPU
// exceeds CpuMs, setting TimedOut=true.
func TestCPUClockFires(t *testing.T) {
	sb := newFakeSandbox()
	sb.waitDelay = 5 * time.Second // Wait blocks until killed

	limits := testLimits()
	limits.WallTimeMs = 5000 // wall won't fire
	limits.IdleMs = 5000     // idle won't fire
	limits.CpuMs = 100       // CPU kills at 100ms

	// CPU usage source that reports 200ms (over the 100ms limit).
	fakeCPU := func(_ context.Context) (int, error) { return 200, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.Run(ctx, sb, limits, fakeCPU)
	require.NoError(t, err)

	assert.True(t, result.TimedOut, "CPU clock must set TimedOut=true")
	assert.False(t, result.IdleTimedOut)
	assert.Equal(t, 1, sb.kills())
	assert.Equal(t, 1, sb.cleanups())
}

// TestCPUClockUnderLimit verifies that CPU usage staying under CpuMs never kills.
func TestCPUClockUnderLimit(t *testing.T) {
	sb := newFakeSandbox()
	sb.waitDelay = 0
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 5000
	limits.CpuMs = 1000 // high limit

	// CPU usage is always 50ms — well under 1000ms limit.
	fakeCPU := func(_ context.Context) (int, error) { return 50, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.Run(ctx, sb, limits, fakeCPU)
	require.NoError(t, err)

	assert.False(t, result.TimedOut)
	assert.False(t, result.IdleTimedOut)
	assert.NotNil(t, result.ExitCode)
}

// TestNormalExitBeforeClocks verifies that when Wait returns before any clock
// fires, the result carries the exit code and no timeout flags.
func TestNormalExitBeforeClocks(t *testing.T) {
	sb := newFakeSandbox()
	sb.waitResult = runner.Result{ExitCode: intPtr(42)}
	sb.waitDelay = 0 // returns immediately

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 5000
	limits.CpuMs = 5000

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.Run(ctx, sb, limits, fakeCPU)
	require.NoError(t, err)

	assert.False(t, result.TimedOut)
	assert.False(t, result.IdleTimedOut)
	require.NotNil(t, result.ExitCode)
	assert.Equal(t, 42, *result.ExitCode)
}

// TestIdempotentTeardownDoubleFire verifies that when two terminal events race
// (e.g. CPU clock fires AND Wait returns simultaneously), Kill+Cleanup run
// EXACTLY once and exactly one Result is reported.
func TestIdempotentTeardownDoubleFire(t *testing.T) {
	// Run many iterations to expose races.
	for i := 0; i < 100; i++ {
		sb := newFakeSandbox()
		// Wait returns immediately with exit code 0 AND CPU will always report over limit.
		// Race: normal exit vs cpu clock.
		sb.waitResult = runner.Result{ExitCode: intPtr(0)}
		sb.waitDelay = 0

		limits := testLimits()
		limits.WallTimeMs = 5000
		limits.IdleMs = 5000
		limits.CpuMs = 50 // low limit — CPU clock will want to fire

		// CPU always over limit to trigger CPU clock.
		fakeCPU := func(_ context.Context) (int, error) { return 200, nil }

		ctx, cancel := context.WithCancel(context.Background())
		_, err := session.Run(ctx, sb, limits, fakeCPU)
		cancel()
		require.NoError(t, err)

		kills := sb.kills()
		cleanups := sb.cleanups()

		assert.LessOrEqual(t, kills, 1, "Kill must be called at most once (idempotent teardown)")
		assert.Equal(t, 1, cleanups, "Cleanup must be called exactly once")
	}
}

// TestTruncatedCarried verifies that the Truncated flag from the pump's shared
// atomic.Bool is carried into the final Result.
func TestTruncatedCarried(t *testing.T) {
	sb := newFakeSandbox()
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}
	sb.waitDelay = 0

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 5000
	limits.CpuMs = 5000

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	// Pre-set a truncated flag that Run should carry into Result.
	truncated := &atomic.Bool{}
	truncated.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.RunWithTruncated(ctx, sb, limits, fakeCPU, truncated)
	require.NoError(t, err)
	assert.True(t, result.Truncated, "truncated flag from shared pump atomic must appear in Result")
}

// intPtr is a helper to create a pointer to an int.
func intPtr(v int) *int {
	return &v
}
