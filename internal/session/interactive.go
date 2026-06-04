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
	// BeforeCleanup, if non-nil, is called inside the single sync.Once teardown
	// AFTER the process has terminated but BEFORE the sandbox is killed and
	// removed. It is the only window in which a caller can read the sandbox's
	// filesystem (e.g. artifact capture via CopyFromContainer) — once Kill +
	// Cleanup run, the container and its /workspace volume are gone. It is called
	// at most once and must not block indefinitely; the supervised ctx is passed.
	BeforeCleanup func(ctx context.Context)
	// StdinActivity, if non-nil, is signalled by the caller (worker) on each
	// stdin chunk written to the sandbox. The session fans these signals into
	// the idle clock so that interactive INPUT counts as activity — a process
	// blocked on input() produces no output, so without this the idle clock
	// would kill it mid-prompt while the user is typing (the headline
	// interactive-stdin use case). The session NEVER reads or writes the stdin
	// pipe; it only observes this out-of-band signal. May be nil (the plain Run
	// path and non-interactive callers leave it unset).
	StdinActivity <-chan struct{}
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
		sb:            sb,
		limits:        limits,
		cpuUsage:      cpuUsage,
		truncated:     truncated,
		startTime:     time.Now(),
		done:          make(chan struct{}),
		resultCh:      make(chan runner.Result, 1),
		beforeCleanup: sinks.BeforeCleanup,
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

	// Stdin activity (interactive input) also resets the idle clock. The worker
	// signals sinks.StdinActivity on each chunk it writes to the sandbox's stdin;
	// we fan it into the same activityCh the output pumps use. Without this, a
	// process blocked on input() emits no output and the idle clock would kill it
	// while the user is still typing (STDIN interactive use case). The forwarder
	// exits with the session via s.done. Non-blocking send: a full buffer already
	// implies pending activity, so dropping an extra signal is harmless.
	if sinks.StdinActivity != nil {
		go func() {
			for {
				select {
				case <-sinks.StdinActivity:
					select {
					case activityCh <- struct{}{}:
					default:
					}
				case <-s.done:
					return
				}
			}
		}()
	}

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
