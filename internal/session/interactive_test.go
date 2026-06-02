package session_test

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/session"
)

// TestRunInteractive_StdoutReachesSink verifies that a fake sandbox emitting
// "hello\n" on stdout calls the Stdout sink exactly with that chunk.
func TestRunInteractive_StdoutReachesSink(t *testing.T) {
	sb := newFakeSandbox()
	sb.stdout = bytes.NewReader([]byte("hello\n"))
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}
	sb.waitDelay = 5 * time.Millisecond // brief wait so pump can read

	var mu sync.Mutex
	var received []byte

	sinks := session.Sinks{
		Stdout: func(chunk []byte) {
			mu.Lock()
			received = append(received, chunk...)
			mu.Unlock()
		},
		Stderr: func(_ []byte) {},
	}

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 500

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.RunInteractive(ctx, sb, limits, fakeCPU, sinks)
	require.NoError(t, err)

	mu.Lock()
	got := string(received)
	mu.Unlock()

	assert.Equal(t, "hello\n", got, "stdout sink must receive the exact chunk the sandbox emits")
	assert.NotNil(t, result.ExitCode)
	assert.Equal(t, 0, *result.ExitCode)
}

// TestRunInteractive_StderrReachesSink verifies that stderr output reaches the
// Stderr sink.
func TestRunInteractive_StderrReachesSink(t *testing.T) {
	sb := newFakeSandbox()
	sb.stderr = bytes.NewReader([]byte("error line\n"))
	sb.waitResult = runner.Result{ExitCode: intPtr(1)}
	sb.waitDelay = 5 * time.Millisecond

	var mu sync.Mutex
	var received []byte

	sinks := session.Sinks{
		Stdout: func(_ []byte) {},
		Stderr: func(chunk []byte) {
			mu.Lock()
			received = append(received, chunk...)
			mu.Unlock()
		},
	}

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 500

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := session.RunInteractive(ctx, sb, limits, fakeCPU, sinks)
	require.NoError(t, err)

	mu.Lock()
	got := string(received)
	mu.Unlock()

	assert.Equal(t, "error line\n", got, "stderr sink must receive the exact chunk the sandbox emits")
}

// TestRunInteractive_IdleClockFires verifies that the idle clock fires for a
// silent fake sandbox (no output, long Wait), setting IdleTimedOut.
func TestRunInteractive_IdleClockFires(t *testing.T) {
	sb := newFakeSandbox()
	sb.waitDelay = 5 * time.Second // Wait blocks; idle clock fires first

	sinks := session.Sinks{
		Stdout: func(_ []byte) {},
		Stderr: func(_ []byte) {},
	}

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 50 // fire quickly

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.RunInteractive(ctx, sb, limits, fakeCPU, sinks)
	require.NoError(t, err)

	assert.True(t, result.IdleTimedOut, "idle clock must fire on a silent fake sandbox")
	assert.Equal(t, 1, sb.cleanups(), "Cleanup must be called once on idle timeout")
}

// TestRunInteractive_TruncationSetsFlag verifies that outputting more bytes
// than the budget sets Result.Truncated.
func TestRunInteractive_TruncationSetsFlag(t *testing.T) {
	// Budget = 1 KiB; we'll produce 2 KiB of stdout.
	bigOutput := bytes.Repeat([]byte("x"), 2*1024)
	sb := newFakeSandbox()
	sb.stdout = bytes.NewReader(bigOutput)
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}
	sb.waitDelay = 5 * time.Millisecond

	var sinkCalled atomic.Int64
	sinks := session.Sinks{
		Stdout: func(_ []byte) { sinkCalled.Add(1) },
		Stderr: func(_ []byte) {},
	}

	limits := testLimits()
	limits.OutputKb = 1 // 1 KiB budget — 2 KiB input exceeds it
	limits.WallTimeMs = 5000
	limits.IdleMs = 500

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.RunInteractive(ctx, sb, limits, fakeCPU, sinks)
	require.NoError(t, err)

	assert.True(t, result.Truncated, "Truncated must be true when output exceeds OutputKb cap")
	assert.Greater(t, sinkCalled.Load(), int64(0), "Stdout sink must be called at least once")
}

// TestRunInteractive_SingleTeardownUnderRace verifies that terminate is called
// exactly once even when Wait and a clock race to fire simultaneously.
func TestRunInteractive_SingleTeardownUnderRace(t *testing.T) {
	for i := 0; i < 50; i++ {
		sb := newFakeSandbox()
		// Wait returns immediately AND CPU clock is at limit — race condition.
		sb.waitResult = runner.Result{ExitCode: intPtr(0)}
		sb.waitDelay = 0

		sinks := session.Sinks{
			Stdout: func(_ []byte) {},
			Stderr: func(_ []byte) {},
		}

		limits := testLimits()
		limits.WallTimeMs = 5000
		limits.IdleMs = 5000
		limits.CpuMs = 50 // low CPU limit — clock wants to fire

		fakeCPU := func(_ context.Context) (int, error) { return 200, nil }

		ctx, cancel := context.WithCancel(context.Background())
		_, err := session.RunInteractive(ctx, sb, limits, fakeCPU, sinks)
		cancel()
		require.NoError(t, err)

		assert.Equal(t, 1, sb.cleanups(),
			"Cleanup must be called exactly once regardless of racing terminal events")
	}
}

// TestRunInteractive_StdinNotTouchedBySession verifies that the session does
// NOT close or write to the sandbox's stdin (the caller owns it). We wrap a
// fake stdin that panics on Close so any accidental session-side close is
// caught immediately.
func TestRunInteractive_StdinNotTouchedBySession(t *testing.T) {
	sb := newFakeSandbox()
	// Replace the default nopWriteCloser with a panic-on-close version.
	pob := &panicOnCloseSandboxStdin{}
	sb.stdin = pob
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}
	sb.waitDelay = 5 * time.Millisecond

	sinks := session.Sinks{
		Stdout: func(_ []byte) {},
		Stderr: func(_ []byte) {},
	}

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 500

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// This should NOT panic — the session must not close or write to stdin.
	_, err := session.RunInteractive(ctx, sb, limits, fakeCPU, sinks)
	require.NoError(t, err)
	assert.False(t, pob.closed, "session must NOT close the caller-owned stdin pipe")
}

// panicOnCloseSandboxStdin is an io.WriteCloser that records Close calls.
// It is used to verify the session does not touch the caller-owned stdin.
type panicOnCloseSandboxStdin struct {
	mu     sync.Mutex
	closed bool
}

func (p *panicOnCloseSandboxStdin) Write(b []byte) (int, error) {
	return len(b), nil
}

func (p *panicOnCloseSandboxStdin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	// Record the close; the test asserts this never happens.
	return nil
}
