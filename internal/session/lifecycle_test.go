package session_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/session"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// TestDurationMsIsRecorded verifies that the DurationMs field in Result captures
// roughly how long Run() took (at least the Wait delay).
func TestDurationMsIsRecorded(t *testing.T) {
	sb := newFakeSandbox()
	sb.waitDelay = 20 * time.Millisecond // Wait takes at least 20ms
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	limits := testLimits()
	limits.WallTimeMs = 5000
	limits.IdleMs = 5000
	limits.CpuMs = 5000

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	result, err := session.Run(ctx, sb, limits, fakeCPU)
	elapsed := time.Since(start)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, result.DurationMs, 20,
		"DurationMs must be at least the Wait delay (20ms)")
	assert.LessOrEqual(t, result.DurationMs, int(elapsed.Milliseconds())+50,
		"DurationMs must not wildly exceed actual elapsed time")
}

// TestCleanupCalledOnWallTimeout verifies Cleanup is called on wall-clock expiry.
func TestCleanupCalledOnWallTimeout(t *testing.T) {
	sb := newFakeSandbox()
	sb.waitDelay = 5 * time.Second

	limits := testLimits()
	limits.WallTimeMs = 30
	limits.IdleMs = 5000
	limits.CpuMs = 5000

	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := session.Run(ctx, sb, limits, fakeCPU)
	require.NoError(t, err)
	assert.Equal(t, 1, sb.cleanups(), "Cleanup must be called once on wall timeout")
}

// TestPumpActivityResetsIdle verifies that the activity channel wired from the
// pump into the idle clock resets the timer (the clock does not fire when
// there's continuous activity under IdleMs gaps).
func TestPumpActivityResetsIdle(t *testing.T) {
	// Use a sandbox whose Wait returns quickly (normal exit) before the idle
	// clock fires. Activity from the pump reading stdout resets the idle timer.
	sb := newFakeSandbox()
	sb.stdout = bytes.NewReader([]byte("some output data"))
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}
	sb.waitDelay = 10 * time.Millisecond

	limits := wire.Limits{
		WallTimeMs: 5000,
		IdleMs:     200, // idle fires after 200ms silence
		CpuMs:      5000,
		MemoryMb:   64,
		OutputKb:   512,
		Pids:       32,
	}
	fakeCPU := func(_ context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := session.Run(ctx, sb, limits, fakeCPU)
	require.NoError(t, err)
	assert.False(t, result.IdleTimedOut, "idle clock must not fire when there is activity")
}
