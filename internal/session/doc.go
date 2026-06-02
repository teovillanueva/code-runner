// Package session provides the testable, Docker-free safety logic for a single
// sandboxed execution session:
//
//   - Clocks: three independent timers (wall, idle, CPU) that each call Kill
//     on the sandbox when their limit is exceeded.
//   - Pump: a byte-capped output pump that forwards stdout/stderr to a sink
//     and continues draining past the cap so the source never blocks.
//   - Lifecycle: a sync.Once-guarded idempotent teardown that every terminal
//     path (wall clock, idle clock, CPU clock, normal exit, context cancel)
//     funnels into — ensuring Kill and Cleanup each run exactly once.
//
// # Design contract
//
// The package's primary entry point is [Run] (and [RunWithTruncated] for callers
// that own the shared output budget). Given a [runner.Sandbox], a [wire.Limits],
// and an injectable CPU-usage source, Run:
//
//  1. Starts the wall clock (fires after WallTimeMs unconditionally).
//  2. Starts the idle clock (fires after IdleMs with no stdout/stderr activity;
//     resets on every output chunk).
//  3. Starts the CPU poller (polls every cpuPollInterval; fires when cumulative
//     CPU usage exceeds CpuMs).
//  4. Drains Stdout and Stderr via two Pump goroutines wired to the shared
//     activity channel.
//  5. Waits for the sandbox (Sandbox.Wait) in a separate goroutine.
//  6. The first of these goroutines to complete calls terminate() once via
//     sync.Once; every other goroutine's call is a no-op.
//  7. Returns the assembled runner.Result once cleanup is complete.
//
// # No Docker dependency
//
// This package MUST NOT import github.com/docker/docker. The CPU usage source
// is injected as a plain func(ctx) (int, error) so the real cgroup stats poller
// (plan 02-03) can plug in without creating a Docker coupling here. Unit tests
// supply a fake Sandbox and a fake CPU function — no container runtime required.
//
// # Pitfall mitigations embedded here
//
//   - Pitfall 1 (CPU escaping wall-clock): three independent clocks; CPU poller
//     reads actual cgroup usage, not elapsed time.
//   - Pitfall 5 (output flooding): Pump caps combined output, truncation flag set.
//   - Pitfall 6 (pipe deadlock): Pump always drains to EOF even past the cap.
//   - Pitfall 9 (cleanup leaks): single sync.Once teardown; every path calls it.
package session
