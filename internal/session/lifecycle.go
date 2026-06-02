package session

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// CPUUsageFunc returns the cumulative CPU usage in milliseconds for the sandbox
// being supervised. The real implementation reads cgroup cpu.stat (Plan 02-03);
// tests inject a fake that returns a scripted value. An error means the stat
// source is unavailable — the CPU clock is skipped for that poll.
type CPUUsageFunc func(ctx context.Context) (cpuMs int, err error)

// terminateReason describes which terminal event fired first.
type terminateReason int

const (
	reasonNormal   terminateReason = iota // Sandbox.Wait returned normally
	reasonWall                            // wall clock expired
	reasonIdle                            // idle clock expired
	reasonCPU                             // CPU usage exceeded CpuMs
	reasonCanceled                        // parent context was canceled
)

// session is the per-execution supervisor. All fields are set once before any
// goroutine starts; the only mutable state is the sync.Once, the done channel,
// and the result channel.
type session struct {
	sb        runner.Sandbox
	limits    wire.Limits
	cpuUsage  CPUUsageFunc
	truncated *atomic.Bool // shared with Pump instances
	startTime time.Time

	once     sync.Once
	done     chan struct{}      // closed by terminate() so all clock goroutines can exit
	resultCh chan runner.Result // buffered(1); written once by terminate()
}

// Run is the main entry point. It supervises sb with the given limits,
// starting three independent clocks, two output pumps, and one Wait goroutine.
// It returns the assembled Result once teardown is complete.
//
// Run does NOT import or reference any Docker SDK — use RunWithTruncated if the
// caller owns a shared output-budget atomic.Bool.
func Run(ctx context.Context, sb runner.Sandbox, limits wire.Limits, cpuUsage CPUUsageFunc) (runner.Result, error) {
	truncated := &atomic.Bool{}
	return RunWithTruncated(ctx, sb, limits, cpuUsage, truncated)
}

// RunWithTruncated is like Run but accepts a caller-owned truncated flag (e.g.
// the shared atomic.Bool used by the output pumps). Use this when the pump
// budget is managed externally.
func RunWithTruncated(ctx context.Context, sb runner.Sandbox, limits wire.Limits, cpuUsage CPUUsageFunc, truncated *atomic.Bool) (runner.Result, error) {
	s := &session{
		sb:        sb,
		limits:    limits,
		cpuUsage:  cpuUsage,
		truncated: truncated,
		startTime: time.Now(),
		done:      make(chan struct{}),
		resultCh:  make(chan runner.Result, 1),
	}
	s.supervise(ctx)
	result := <-s.resultCh
	return result, nil
}

// supervise launches all goroutines and wires the terminal paths. It returns
// immediately; the result is delivered on s.resultCh.
func (s *session) supervise(ctx context.Context) {
	// Activity channel: Pump goroutines send a signal for every forwarded
	// output chunk; the idle clock receives this to reset its timer.
	activityCh := make(chan struct{}, 64)

	// Start output pumps (stdout + stderr share one budget).
	budget := &atomic.Int64{}
	budget.Store(int64(s.limits.OutputKb) * 1024)

	go func() {
		p := NewPump(s.sb.Stdout(), budget, s.truncated, func(_ []byte) {}, activityCh)
		p.Run() //nolint:errcheck
	}()
	go func() {
		p := NewPump(s.sb.Stderr(), budget, s.truncated, func(_ []byte) {}, activityCh)
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

// terminate is called by every terminal goroutine. The sync.Once guarantees
// only the first caller actually runs teardown; subsequent calls are no-ops.
// This is the single idempotent teardown (Pitfall 9).
//
// After teardown, done is closed so all clock goroutines wake up and exit,
// and the assembled Result is sent on resultCh for the supervisor to return.
func (s *session) terminate(ctx context.Context, waitResult runner.Result, reason terminateReason) {
	s.once.Do(func() {
		durationMs := int(time.Since(s.startTime).Milliseconds())

		// Kill the entire container (not just the PID) then clean up.
		// Errors are intentionally ignored — we are already on the terminal path.
		s.sb.Kill(ctx) //nolint:errcheck
		s.sb.Cleanup() //nolint:errcheck

		result := runner.Result{
			ExitCode:     waitResult.ExitCode,
			Signal:       waitResult.Signal,
			TimedOut:     reason == reasonWall || reason == reasonCPU,
			IdleTimedOut: reason == reasonIdle,
			Truncated:    s.truncated.Load(),
			DurationMs:   durationMs,
		}
		s.resultCh <- result

		// Signal all clock goroutines to stop. Must come AFTER resultCh send
		// so the supervisor can receive the result before goroutines exit.
		close(s.done)
	})
}
