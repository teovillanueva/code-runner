// Package runner defines the Runner and Sandbox interfaces that abstract the
// sandbox backend. Concrete implementations (DockerSocketRunner, gVisorRunner,
// FlyMachinesRunner) swap in behind these interfaces without touching any core
// worker logic.
//
// Phase 1 ships interface skeletons + a no-op stub. The DockerSocketRunner that
// speaks the moby/Docker SDK is implemented in Phase 2.
package runner

import (
	"context"
	"io"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// CompileResult is the terminal outcome of a compile pre-step.
// It carries the exit code and elapsed wall time of the compile command.
// The caller inspects ExitCode to decide whether to proceed to the run step.
type CompileResult struct {
	// ExitCode is the exit status of the compile command.
	// A value of 0 means success; any non-zero value means failure.
	ExitCode int

	// DurationMs is the wall-clock time taken by the compile step in
	// milliseconds (from exec start to exec completion).
	DurationMs int
}

// Result is the terminal outcome of a sandbox execution. It mirrors the shape
// of wire.ResultEvent so the worker can publish it directly to soketi.
type Result struct {
	// ExitCode is the exit status of the process. Nil when the process was
	// killed by a signal before it exited normally.
	ExitCode *int

	// Signal is the name of the signal (e.g. "SIGKILL") that terminated the
	// process, or nil when the process exited normally.
	Signal *string

	// TimedOut is true when the wall-clock or CPU clock expired and the
	// sandbox was killed unconditionally.
	TimedOut bool

	// IdleTimedOut is true when the idle clock expired (no stdout and no
	// stdin for IdleMs milliseconds).
	IdleTimedOut bool

	// Truncated is true when combined stdout+stderr exceeded the outputKb cap
	// and output was cut short.
	Truncated bool

	// DurationMs is the total wall-clock lifetime of the sandbox in
	// milliseconds (from container start to terminal event).
	DurationMs int
}

// Runner creates and manages sandboxes. Each call to Create produces one
// isolated Sandbox whose lifetime is governed by the Limits embedded in spec.
//
// Phase 2 contract for implementors:
//   - The sandbox must be launched with hardening flags:
//     cap-drop ALL, no-new-privileges, read-only root, tmpfs workspace,
//     NetworkMode=none, seccomp profile, PidsLimit, memory=memory-swap
//     (no swap), NanoCPUs derived from spec.Limits.CpuMs.
//   - The runtime field (spec.Limits is the enforcement boundary; the
//     container runtime is selected via DockerHostConfig.Runtime, e.g.
//     "runsc" for gVisor) must be applied transparently by the impl.
//   - Create must NOT start the process; the caller starts it by writing to
//     Stdin() and invoking Wait().
type Runner interface {
	// Create allocates a hardened sandbox from the fully-resolved spec.
	// The returned Sandbox is ready for pipe attachment; the process inside
	// has not started yet.
	Create(ctx context.Context, spec wire.JobSpec) (Sandbox, error)
}

// Sandbox is the live handle to a single running (or starting) sandbox. It
// exposes the three I/O pipes, a blocking Wait, and the two terminal lifecycle
// methods Kill and Cleanup.
//
// Ownership model: the caller owns one Sandbox per Create call. Kill and
// Cleanup must both be called on every exit path (deferred Cleanup is the
// idiomatic pattern).
type Sandbox interface {
	// Stdin returns the write end of the sandbox's stdin pipe. The caller
	// writes job input here; the Phase 2 impl routes it through the
	// container's hijacked attach connection.
	Stdin() io.WriteCloser

	// Stdout returns the read end of the sandbox's stdout pipe. The Phase 2
	// impl demultiplexes Docker's stdcopy framing so callers read plain bytes.
	Stdout() io.Reader

	// Stderr returns the read end of the sandbox's stderr pipe. See Stdout.
	Stderr() io.Reader

	// Wait blocks until the sandbox terminates (normal exit, signal, or any
	// of the three clocks). It returns a Result and the first non-context
	// error encountered.
	//
	// Three-clock contract (Phase 2/3):
	//   - Wall clock: absolute deadline; kills unconditionally at WallTimeMs.
	//   - CPU clock: cgroup cpu.max; kills when accumulated CPU time exceeds CpuMs.
	//   - Idle clock: application-layer timer reset on any stdout or stdin byte;
	//     kills at IdleMs of silence.
	Wait(ctx context.Context) (Result, error)

	// Kill sends SIGKILL to the entire process tree inside the sandbox.
	//
	// Phase 2 contract: must kill the container (ContainerKill), not just the
	// PID-1 process, to ensure no child processes escape. Safe to call after
	// Wait returns.
	Kill(ctx context.Context) error

	// Cleanup releases all resources held by the sandbox (container removal,
	// pipe closure, slot release). It is idempotent — safe to call multiple
	// times and from multiple code paths; only the first call has effect.
	//
	// The caller MUST defer Cleanup() after Create() returns successfully so
	// no slot leaks on any exit path (normal, error, panic).
	Cleanup() error

	// Compile executes a compile command INSIDE the same hardened sandbox that
	// was created by Runner.Create. The argv comes exclusively from the
	// manifest compile field — no language-name branching in callers.
	//
	// The compile command runs under the same sandbox hardening as the run
	// step (network=none, cap-drop ALL, non-root, read-only rootfs, writable
	// /workspace). Any artifact produced in /workspace (e.g. /workspace/prog)
	// persists in the container for the subsequent run step.
	//
	// The stderr callback is called synchronously for each stderr chunk from
	// the compile command. Callers forward these to the publisher so the
	// client sees compiler diagnostics.
	//
	// Returns a CompileResult with the exit code and duration. A non-zero
	// ExitCode means compilation failed; the caller MUST NOT proceed to the
	// run step. An error return indicates an infrastructure failure (Docker
	// exec failed, context cancelled, etc.) — treat it as a failed compile.
	Compile(ctx context.Context, argv []string, stderr func([]byte)) (CompileResult, error)
}
