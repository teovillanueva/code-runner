// Package runner — ZygoteRunner.
//
// The ZygoteRunner satisfies the same Runner/Sandbox interface as
// DockerSocketRunner but, instead of one hardened container per job, it routes
// each job to a long-lived, privileged, per-(language,version) "warm parent"
// pool container running the language's zygote agent. Each job is one TCP
// connection to that agent (the relay protocol in zygote_relay.go); the agent
// double-forks + hardens one child per job, sharing pre-imported pages
// copy-on-write (spike 005: 2.7× density).
//
// This is gated behind config.ZygoteEnabled (default OFF) and is only safe on
// Fly where the Firecracker microVM is the real host boundary — the pool
// container runs privileged with host cgroups. See ZYGOTE-PRODUCTION-DESIGN.md.
//
// Phase 12 scope: this package is standalone and satisfies the interface. The
// worker's runner selection (TieredRunner) is wired in Phase 13; this file does
// NOT touch apps/worker.
package runner

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/docker/docker/client"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// Compile-time assertions: ZygoteRunner must implement Runner; zygoteSandbox
// must implement Sandbox. (ZYG-01.)
var _ Runner = (*ZygoteRunner)(nil)
var _ Sandbox = (*zygoteSandbox)(nil)

// defaultZygoteTmpfsBytes is the per-child /tmp tmpfs size when the job carries
// no explicit output budget to derive one from.
const defaultZygoteTmpfsBytes = 64 * 1024 * 1024

// ZygoteRunner creates sandboxes by routing jobs to warm parent pool
// containers. It mirrors NewDockerSocketRunner's shape (Docker SDK client +
// config) and owns a poolManager keyed by (language, version).
type ZygoteRunner struct {
	cli  *client.Client
	cfg  config.Config
	pool *poolManager
}

// NewZygoteRunner constructs a ZygoteRunner. It mirrors NewDockerSocketRunner:
// it connects to the Docker daemon via the environment (DOCKER_HOST), optionally
// overridden by cfg.DockerHost. The relay port, idle window, UID base and pool
// memory are read from cfg (config.Default supplies sane values).
func NewZygoteRunner(cfg config.Config) (*ZygoteRunner, error) {
	opts := []client.Opt{
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	}
	if cfg.DockerHost != "" {
		opts = append(opts, client.WithHost(cfg.DockerHost))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("zygote: create docker client: %w", err)
	}
	return newZygoteRunnerWithClient(cfg, cli), nil
}

// newZygoteRunnerWithClient is the internal constructor used by tests to inject
// a Docker client (or a fake docker-control behaviour via the pool manager).
func newZygoteRunnerWithClient(cfg config.Config, cli *client.Client) *ZygoteRunner {
	r := &ZygoteRunner{cli: cli, cfg: cfg}
	r.pool = newPoolManager(cfg, &dockerPoolBackend{cli: cli, cfg: cfg}, defaultDialer)
	return r
}

// defaultDialer dials the agent over plain TCP with a connect timeout.
func defaultDialer(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 5*time.Second)
}

// Create routes the job to the warm parent for (spec.Language, spec.Version),
// dials the agent, performs the HELLO → STARTED handshake, and returns a
// zygoteSandbox bound to that connection. On any failure the connection and any
// reserved pool resource are released so no worker slot leaks (POOL-04).
func (r *ZygoteRunner) Create(ctx context.Context, spec wire.JobSpec) (Sandbox, error) {
	// Record the wall time of the whole fork/spawn handshake (Create→STARTED):
	// the dial + HELLO + agent fork+harden+cgroup-place until STARTED arrives.
	// This is the zygote analogue of the Docker tier's sandbox.create.duration
	// and only records on the success path (ZOBS-01).
	forkStart := time.Now()

	// Resolve (get-or-create) the warm parent for this language+version, then
	// dial its agent. dead-parent detection + respawn lives in the pool manager.
	rc, release, err := r.pool.dial(ctx, poolKey{
		language: spec.Language,
		version:  spec.Version,
	}, spec.Image)
	if err != nil {
		return nil, err
	}

	// Build the HELLO from the resolved spec.
	hello := buildHello(spec, r.cfg)
	if err := rc.sendHello(hello); err != nil {
		_ = rc.close()
		release()
		return nil, fmt.Errorf("zygote: send HELLO: %w", err)
	}

	// Single decoder shared across the handshake and the demux loop so no frame
	// is dropped between STARTED and the reader goroutine.
	dec := newFrameDecoder(rc.conn)
	realpid, err := rc.readStarted(dec)
	if err != nil {
		_ = rc.close()
		release()
		return nil, err
	}
	rc.startReader(dec)

	// STARTED arrived: the child is forked, hardened and cgroup-placed. Record
	// the fork latency tagged by (language, version).
	zygoteForkDuration().Record(ctx,
		time.Since(forkStart).Seconds(),
		langVersionAttr(spec.Language, spec.Version),
	)

	sb := &zygoteSandbox{
		rc:        rc,
		release:   release,
		spec:      spec,
		startTime: time.Now(),
		realPID:   realpid,
		stdin:     &relayStdin{rc: rc},
	}
	return sb, nil
}

// Close releases pool resources (idle-reaper goroutine + any warm containers).
// Intended for worker shutdown. Safe to call once.
func (r *ZygoteRunner) Close() error {
	return r.pool.close()
}

// RegisterMetrics registers the per-language warm-parent observable gauge
// (ZOBS-01) against the current global MeterProvider, observing this runner's
// live pool. It returns an Unregister func so the worker can detach it on
// shutdown (mirrors Worker.RegisterMetrics). The fork-latency histogram and the
// reap/respawn/terminal counters are recorded inline on the hot path and need no
// registration.
func (r *ZygoteRunner) RegisterMetrics() (func() error, error) {
	return registerWarmParentGauge(r.pool)
}

// buildHello assembles the HELLO payload from the resolved JobSpec and config.
// memMaxBytes / pidsMax / tmpfsBytes are per-CHILD limits derived from the job's
// own Limits (the pool container's own memory is config.ZygotePoolMemoryMb).
func buildHello(spec wire.JobSpec, cfg config.Config) helloPayload {
	memBytes := int64(spec.Limits.MemoryMb) * 1024 * 1024
	if memBytes <= 0 {
		memBytes = 128 * 1024 * 1024
	}
	pids := spec.Limits.Pids
	if pids <= 0 {
		pids = 64
	}
	tmpfs := int64(defaultZygoteTmpfsBytes)
	if spec.Limits.OutputKb > 0 {
		// Match the Docker tier's heuristic: a bit more headroom than the output
		// cap, still bounded.
		mb := int64((spec.Limits.OutputKb/1024+1)*2) * 1024 * 1024
		if mb > tmpfs {
			tmpfs = mb
		}
	}

	files := make([]helloFile, 0, len(spec.Files))
	for _, f := range spec.Files {
		files = append(files, helloFile{Name: f.Name, Content: f.Content})
	}

	entry := spec.Entrypoint
	if entry == "" {
		entry = "main.py"
	}

	return helloPayload{
		JobID:       spec.JobId,
		Entrypoint:  entry,
		Files:       files,
		UID:         cfg.ZygoteUIDBase,
		MemMaxBytes: memBytes,
		PidsMax:     pids,
		TmpfsBytes:  tmpfs,
	}
}

// ── zygoteSandbox ─────────────────────────────────────────────────────────────

// zygoteSandbox is the live handle to one job running as a hardened child inside
// a warm parent pool container. It implements Sandbox over the relay connection.
type zygoteSandbox struct {
	rc        *relayConn
	release   func() // releases the per-job pool reservation (idempotent via Cleanup once)
	spec      wire.JobSpec
	startTime time.Time
	realPID   int

	stdin *relayStdin

	cleanupOnce sync.Once
}

// Stdin returns the relay-backed stdin writer (emits STDIN frames; STDIN_CLOSE
// on Close). The same writer instance is returned on every call.
func (s *zygoteSandbox) Stdin() io.WriteCloser { return s.stdin }

// Stdout returns the demuxed stdout reader (fed by STDOUT frames).
func (s *zygoteSandbox) Stdout() io.Reader { return s.rc.stdoutR }

// Stderr returns the demuxed stderr reader (fed by STDERR frames).
func (s *zygoteSandbox) Stderr() io.Reader { return s.rc.stderrR }

// Wait blocks until the EXIT frame arrives (or the connection breaks), and
// returns a Result built from exitCode/signal. The three clocks (wall/idle/cpu)
// are enforced by internal/session exactly as with the Docker tier; the session
// calls Kill/Cleanup on expiry, which closes the conn and unblocks this Wait.
//
// TimedOut / IdleTimedOut are NOT set here — the session supervisor sets them
// based on which clock fired, identical to dockerSandbox.Wait.
func (s *zygoteSandbox) Wait(ctx context.Context) (Result, error) {
	select {
	case e := <-s.rc.exitCh:
		result := Result{
			ExitCode:   e.exitCode,
			DurationMs: int(time.Since(s.startTime).Milliseconds()),
		}
		if e.signal != nil {
			name := signalName(*e.signal)
			result.Signal = &name
		}
		// Runner-agnostic terminal-outcome counter (ZOBS-02): mirrors the Docker
		// tier so dashboards plot terminal states uniformly. A signal-terminated
		// child is "killed" (OOM/cgroup.kill/supervisor); otherwise "exited".
		outcome := "exited"
		if result.Signal != nil {
			outcome = "killed"
		}
		sandboxTerminal().Add(ctx, 1, terminalAttr(s.spec.Language, outcome))
		if e.err != nil {
			// A broken connection mid-job: surface the error so the worker fails
			// the job cleanly. The slot is still released via Cleanup (POOL-04).
			if ctx.Err() != nil {
				// Context cancelled (Kill/Cleanup fired): treat as a clean
				// supervisor-driven termination, mirroring dockerSandbox.
				return result, nil
			}
			return result, e.err
		}
		return result, nil
	case <-ctx.Done():
		return Result{DurationMs: int(time.Since(s.startTime).Milliseconds())}, nil
	}
}

// Kill sends a KILL frame; the agent cgroup.kills the whole child tree (full
// subtree, not a single pid). Safe to call after Wait returns. A failed KILL
// frame is tolerated (the conn may already be gone); Cleanup's conn-close also
// signals an implicit KILL to the agent.
func (s *zygoteSandbox) Kill(ctx context.Context) error {
	// Record kill latency into the SAME runner-agnostic histogram the Docker
	// tier uses (ZOBS-02): the agent does cgroup.kill on the child tree in
	// response to the KILL frame.
	killStart := time.Now()
	_ = s.rc.sendKill()
	sandboxKillDuration().Record(ctx,
		time.Since(killStart).Seconds(),
		langAttr(s.spec.Language),
	)
	return nil
}

// Cleanup releases all resources exactly once (idempotent via sync.Once):
// closes the stdin writer, closes the relay connection (which the agent treats
// as an implicit KILL → cgroup.kill + reap + cgroup leaf removal + job tmp
// teardown agent-side), and releases the per-job pool reservation. No
// parent/slot/fd/cgroup leak on any path (the parent stays warm for reuse).
func (s *zygoteSandbox) Cleanup() error {
	var cleanupErr error
	s.cleanupOnce.Do(func() {
		// Close the connection FIRST. Closing the conn is the agent's implicit
		// KILL signal and it unblocks any in-flight relay write (so a teardown
		// never hangs on a STDIN_CLOSE write to a stalled/dead peer). Then mark
		// the stdin writer closed (idempotent; a STDIN_CLOSE on the now-closed
		// conn is harmless and tolerated).
		if err := s.rc.close(); err != nil {
			cleanupErr = fmt.Errorf("zygote: Cleanup close conn: %w", err)
		}
		_ = s.stdin.Close()
		if s.release != nil {
			s.release()
		}
	})
	return cleanupErr
}

// CPUReader returns a CPUUsageFunc backed by the latest CPU frame pushed by the
// agent (cumulative cpuMs). It matches the accessor name/signature the worker
// type-asserts on dockerSandbox today (CPUReader() func(ctx) (int, error)) so
// Phase 13 wiring is drop-in. The returned func never errors — it reports the
// last observed value (0 until the first CPU frame).
func (s *zygoteSandbox) CPUReader() CPUUsageFunc {
	return func(_ context.Context) (int, error) {
		return int(s.rc.latestCPUMs.Load()), nil
	}
}

// Limits returns the job limits stored in the sandbox, mirroring
// dockerSandbox.Limits so the worker can pass them to session.Run uniformly.
func (s *zygoteSandbox) Limits() wire.Limits { return s.spec.Limits }

// Compile is never called on the zygote tier: Python/R manifests have
// compile == nil, and any compile-bearing manifest is routed to the Docker tier
// by the TieredRunner (Phase 13). The method exists only to satisfy the Sandbox
// interface and returns an explicit error if ever invoked.
func (s *zygoteSandbox) Compile(_ context.Context, _ []string, _ func([]byte)) (CompileResult, error) {
	return CompileResult{}, fmt.Errorf("zygote: compile unsupported on zygote tier")
}

// signalName maps a numeric signal to its conventional name for Result.Signal.
// Only the signals the sandbox realistically produces are named; others fall
// back to a numeric form so the wire value is always populated.
func signalName(sig int) string {
	switch sig {
	case 9:
		return "SIGKILL"
	case 15:
		return "SIGTERM"
	case 11:
		return "SIGSEGV"
	case 6:
		return "SIGABRT"
	case 2:
		return "SIGINT"
	case 8:
		return "SIGFPE"
	case 4:
		return "SIGILL"
	case 7:
		return "SIGBUS"
	default:
		return fmt.Sprintf("SIG%d", sig)
	}
}
