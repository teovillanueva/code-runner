package session

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// Sinks holds the output callbacks for RunInteractive. Each callback is called
// for every forwarded chunk from the sandbox's stdout or stderr pipe. The
// callbacks must not block; callers that need async processing must buffer
// internally.
//
// Stdin ownership: the caller (worker) owns the sandbox's stdin pipe — it
// writes chunks and closes it (exactly once) to deliver EOF. The session does
// NOT touch stdin.
type Sinks struct {
	// Stdout is called for each forwarded stdout chunk. Must not be nil.
	Stdout func([]byte)
	// Stderr is called for each forwarded stderr chunk. Must not be nil.
	Stderr func([]byte)
}

// RunInteractive supervises sb like Run, but publishes each stdout/stderr chunk
// via the supplied Sinks callbacks rather than discarding them. It lets the
// caller feed stdin through sb.Stdin() — the session does NOT read or close
// sb.Stdin(); the caller owns it and must close it exactly once to deliver EOF
// (STDIN-02 / 02-04 pump/pipe race fix).
//
// The three clocks (wall, idle, CPU) and the single sync.Once teardown are
// identical to Run. Sinks are not called after teardown.
//
// On normal exit, wall timeout, idle timeout, CPU timeout, or context
// cancellation, RunInteractive returns the assembled runner.Result and a nil
// error (matching Run's contract).
func RunInteractive(ctx context.Context, sb runner.Sandbox, limits wire.Limits, cpuUsage CPUUsageFunc, sinks Sinks) (runner.Result, error) {
	truncated := &atomic.Bool{}
	return runInteractiveWithTruncated(ctx, sb, limits, cpuUsage, sinks, truncated)
}

// runInteractiveWithTruncated is the internal implementation, accepting a
// caller-owned truncated flag for test injection.
func runInteractiveWithTruncated(ctx context.Context, sb runner.Sandbox, limits wire.Limits, cpuUsage CPUUsageFunc, sinks Sinks, truncated *atomic.Bool) (runner.Result, error) {
	s := &session{
		sb:        sb,
		limits:    limits,
		cpuUsage:  cpuUsage,
		truncated: truncated,
		startTime: time.Now(),
		done:      make(chan struct{}),
		resultCh:  make(chan runner.Result, 1),
	}
	s.superviseInteractive(ctx, sinks)
	result := <-s.resultCh
	return result, nil
}

// superviseInteractive is like supervise but wires the caller's sinks into the
// two Pump calls instead of the no-op closures. Stdin is explicitly NOT touched
// here — the caller (worker) owns the write side and the single Close.
func (s *session) superviseInteractive(ctx context.Context, sinks Sinks) {
	// Activity channel: Pump goroutines send a signal for every forwarded
	// output chunk; the idle clock receives this to reset its timer.
	activityCh := make(chan struct{}, 64)

	// Start output pumps (stdout + stderr share one combined budget).
	budget := &atomic.Int64{}
	budget.Store(int64(s.limits.OutputKb) * 1024)

	go func() {
		p := NewPump(s.sb.Stdout(), budget, s.truncated, sinks.Stdout, activityCh)
		p.Run() //nolint:errcheck
	}()
	go func() {
		p := NewPump(s.sb.Stderr(), budget, s.truncated, sinks.Stderr, activityCh)
		p.Run() //nolint:errcheck
	}()

	// Wait goroutine: normal exit path.
	go func() {
		result, _ := s.sb.Wait(ctx)
		s.terminate(ctx, result, reasonNormal)
	}()

	// Wall clock: unconditional hard ceiling.
	go s.runWallClock(ctx)

	// Idle clock: fires after IdleMs of silence.
	go s.runIdleClock(ctx, activityCh)

	// CPU poller: fires when cumulative CPU exceeds CpuMs.
	go s.runCPUClock(ctx)
}
