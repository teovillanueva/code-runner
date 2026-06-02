package session

import (
	"context"
	"time"

	"github.com/teovillanueva/code-runner/internal/runner"
)

// cpuPollInterval is how often the CPU poller reads cgroup usage and compares
// it against CpuMs. A short interval keeps the kill latency low; a long
// interval reduces stat-read overhead. 100ms is a good balance.
const cpuPollInterval = 100 * time.Millisecond

// runWallClock starts the wall-clock timer. When it fires, it calls terminate
// with reasonWall. The wall clock is unconditional — it fires regardless of
// any activity and cannot be reset.
//
// The wall clock guards against any terminal path (including a hung Wait or
// a process that appears idle but is burning CPU) escaping the session budget.
func (s *session) runWallClock(ctx context.Context) {
	timer := time.NewTimer(time.Duration(s.limits.WallTimeMs) * time.Millisecond)
	defer timer.Stop()

	select {
	case <-timer.C:
		s.terminate(ctx, runner.Result{}, reasonWall)
	case <-s.done:
		// Another terminal event fired first; stop.
	}
}

// runIdleClock starts the idle clock. It fires if no activity signal is
// received on activityCh for IdleMs milliseconds. Receiving an activity signal
// resets the timer.
//
// Activity signals are sent by the Pump goroutines on every forwarded output
// chunk (stdout/stderr). The idle clock therefore measures time since the last
// output — not since the process started.
func (s *session) runIdleClock(ctx context.Context, activityCh <-chan struct{}) {
	timer := time.NewTimer(time.Duration(s.limits.IdleMs) * time.Millisecond)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			s.terminate(ctx, runner.Result{}, reasonIdle)
			return
		case <-activityCh:
			// Activity received — reset idle timer.
			if !timer.Stop() {
				// Drain the channel if the timer already fired.
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(time.Duration(s.limits.IdleMs) * time.Millisecond)
		case <-s.done:
			// Another terminal event fired first; stop.
			return
		}
	}
}

// runCPUClock polls the CPU usage source every cpuPollInterval and calls
// terminate with reasonCPU when cumulative usage exceeds CpuMs.
//
// The CPU clock is the primary guard against compute hidden behind interactive
// reads (Pitfall 1). It reads actual cgroup accounting, NOT elapsed wall time,
// so code that spins a CPU loop while reading one byte of stdin is caught.
func (s *session) runCPUClock(ctx context.Context) {
	ticker := time.NewTicker(cpuPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			usage, err := s.cpuUsage(ctx)
			if err != nil {
				// Stat unavailable — skip this poll rather than killing spuriously.
				continue
			}
			if usage > s.limits.CpuMs {
				s.terminate(ctx, runner.Result{}, reasonCPU)
				return
			}
		case <-s.done:
			// Another terminal event fired first; stop.
			return
		}
	}
}
