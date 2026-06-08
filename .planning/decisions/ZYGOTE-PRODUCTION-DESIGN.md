# ZygoteRunner — Production Design (v1.1 authoritative spec)

This is the **source of truth** for the v1.1 ZygoteRunner implementation. Every phase
executor MUST follow it. It encodes decisions already locked with the user and the
architecture derived from validated spikes 005 (CoW density 2.7×) and 006 (per-child
hardening is free) — see `.planning/spikes/005-zygote-cow`, `006-zygote-hardening`,
and `.planning/decisions/FAST-FOLLOW-zygote-runner.md`.

## Locked decisions

1. **Tiered coverage.** Python + R run on the ZygoteRunner (interpreted, heavy-import
   → copy-on-write density). Rust + SQLite stay on the existing `DockerSocketRunner`
   (compiled / no-import → zero CoW benefit). A `TieredRunner` routes per-manifest. All
   4 languages must keep working end-to-end. The routing predicate is **already shipped**
   in Phase 10: `manifest.ZygoteEligible(m)` == "manifest has a NON-EMPTY `preimport`
   array". No language-name branching anywhere.

2. **Fly-only security posture.** The zygote pool container runs **privileged**
   (`CAP_SYS_ADMIN` + `CAP_SETUID/SETGID` + host cgroups via `--cgroupns=host`) so it can
   rebuild per-child isolation inside itself. This is only acceptable because on Fly the
   **Firecracker microVM is the real host boundary** (threat model = host-escape-only).
   ZygoteRunner is therefore **gated behind a config flag, default OFF**. Dev + CI keep
   `DockerSocketRunner`. The mechanism MAY be tested locally in a privileged container
   (Docker Desktop is cgroup v2) — that is for testing only, not the prod default.

## Non-negotiable design rules (spike-proven — do not violate)

- **Rule #1 — credential-free parent.** The warm parent/agent and the pool supervisor
  MUST NOT hold any Redis / soketi / job-queue FD or secret. `fork()` inherits open FDs
  and **CLOEXEC does NOT help without `exec()`** (the agent runs user code in-process, no
  exec). The agent only ever holds the worker↔agent relay socket (which carries no
  secrets). Defense-in-depth: **the child scrubs all fds > 2 before running user code.**
- **Rule #2 — per-child hardening, applied in the child after fork, before user code:**
  - distinct UID per child (`setresgid`/`setresuid`, `UID_BASE + n`; `setgroups([])`)
  - `prctl(PR_SET_NO_NEW_PRIVS, 1)`
  - private PID namespace — **requires a double-fork** (`unshare(CLONE_NEWPID)` may be
    called only once per process; an intermediate unshares then forks the session as PID 1
    of the new ns and exits; the session reparents to the agent, which is set
    `PR_SET_CHILD_SUBREAPER`)
  - private network namespace (`unshare(CLONE_NEWNET)`) — child has no network
  - private mount namespace (`unshare(CLONE_NEWNS)`) + make `/` rec-private +
    private `/tmp` tmpfs (`size` from limits) + remount `/proc`
  - per-child cgroup-v2 sub-cgroup with `memory.max` + `pids.max` (placement done by the
    agent/parent using the session's real root-ns pid, returned via the double-fork pipe)
  - **ctypes gotcha (mandatory):** declare `argtypes`/`restype` on `unshare`/`mount`/
    `prctl` or the large `CLONE_*` flags mis-marshal and the syscall returns EINVAL.
- **Rule #3 — per-language pools.** CoW only shares within the same parent/image. One warm
  parent per `(language, version)`.
- **Rule #4 — pre-import the manifest set** in the parent at warm time
  (`manifest.Preimports(m)`): Python via `importlib.import_module`, R via `library()`.

The exact, working hardening code is in `.planning/spikes/_harness/zygote_hardened.py`
(`child_body`, `spawn_session`, `setup_cgroup_base`, `place_in_cgroup`) and
`isolation_probe.py`. **Lift it; do not reinvent it.**

## Component architecture

```
worker (Go, dind on Fly)                 pool container (privileged, per language+version)
┌───────────────────────────┐            ┌─────────────────────────────────────────────┐
│ TieredRunner              │  TCP/relay │ zygote agent = the language runtime, warm:    │
│  ├─ ZygoteRunner ─────────┼───────────▶│  - pre-imported preimport set (CoW base)      │
│  │   └─ zygote pool mgr   │  one conn  │  - PR_SET_CHILD_SUBREAPER, cgroup base set up │
│  │      (1 warm parent /  │  per job   │  - accept(job): write files, socketpairs,     │
│  │       language+version)│            │     double-fork + harden child, dup2 stdio,   │
│  └─ DockerSocketRunner    │            │     cgroup-place, run entrypoint in-process    │
│     (Rust, SQLite, + R/Py │            │  - relay: conn ⇄ child stdio, cpu push, EXIT  │
│      fallback when off)   │            │  - conn close ⇒ cgroup.kill (worker Kill)     │
└───────────────────────────┘            └─────────────────────────────────────────────┘
```

- The **pool container** is launched by the worker via the same Docker SDK used by
  `DockerSocketRunner`, but it is **long-lived, privileged, network-attached** (so the
  worker can reach its relay port), and **one per (language, version)**. It is NOT a
  per-job container.
- The worker reaches the agent over **TCP on the pool container's Docker-network IP**
  (inspected via the SDK). One TCP connection per job carries the full session.
- Children get their own empty netns, so user code still has **no network** even though
  the pool container does.

## Worker ↔ agent relay protocol (framed, length-prefixed)

One connection per job. Frame = `[1 byte type][4 byte big-endian length][payload]`.

Worker → agent:
- `0x01 HELLO` — JSON `{jobId, entrypoint, files:[{name,content}], uid, memMaxBytes, pidsMax, tmpfsBytes}`. First frame.
- `0x02 STDIN` — raw stdin bytes
- `0x03 STDIN_CLOSE` — deliver EOF to child stdin
- `0x04 KILL` — kill the child (agent does `cgroup.kill`); also triggered by conn close

Agent → worker:
- `0x10 STARTED` — JSON `{realpid}` (after fork+harden+cgroup placement succeed)
- `0x11 STDOUT` — raw stdout bytes
- `0x12 STDERR` — raw stderr bytes
- `0x13 CPU` — JSON `{cpuMs}` cumulative, pushed ~every 100ms (feeds CPUReader)
- `0x14 EXIT` — JSON `{exitCode|null, signal|null}` terminal; agent then closes

The Go side wraps this so `Sandbox.Stdin()` is an `io.WriteCloser` that emits STDIN /
STDIN_CLOSE frames; `Stdout()`/`Stderr()` are `io.Reader`s fed by demuxed STDOUT/STDERR
frames; `Wait()` blocks on EXIT; `Kill()` sends KILL (or closes the conn); `CPUReader()`
returns the latest pushed cpuMs. This mirrors how `dockerSandbox` demuxes Docker's stdcopy
— reuse the patterns in `internal/runner/docker.go`.

## Sandbox interface mapping (internal/runner/runner.go — match dockerSandbox exactly)

- `Create(ctx, spec)` — pick/warm the parent for `(spec.Language, spec.Version)`, dial the
  agent, send HELLO, wait for STARTED, return a `zygoteSandbox`.
- `Stdin()/Stdout()/Stderr()` — relay-backed streams (above).
- `Wait(ctx)` — returns `runner.Result` from the EXIT frame (exitCode/signal); the three
  clocks (wall/idle/cpu) are enforced by `internal/session` exactly as today.
- `Kill(ctx)` — KILL frame ⇒ agent `cgroup.kill` the child's sub-cgroup (full tree). Must
  kill the whole child tree, not one pid.
- `Cleanup()` — idempotent `sync.Once`: close the conn, the agent reaps the child + removes
  the cgroup leaf + job tmp dir. No parent/slot/fd/cgroup leak on any path.
- `Compile(...)` — interpreted languages (Python, R) have `compile == nil`, so zygote
  never compiles. If a zygote-eligible manifest ever had a compile step, fall back to the
  Docker tier for that job. (Keep the method present to satisfy the interface; it can
  return an error "compile not supported on zygote tier" since it's never called for
  Python/R.)
- `CPUReader()` — return latest CPU frame value (cumulative cpuMs) so the session CPU clock
  works (ZYG-05).

## Warm parent pool (POOL-01..04)

- One warm parent per `(language, version)`, created lazily on first job for that language
  and kept warm. Pre-warming on worker boot for zygote-eligible manifests is acceptable.
- **Idle reaping:** a configurable idle window (e.g. `ZYGOTE_POOL_IDLE_MS`, default a few
  minutes); after no jobs for that long, tear the pool container down to reclaim RAM.
- **Dead-parent detection + respawn:** if the agent/container dies, detect on next dial,
  respawn the pool container; in-flight jobs that lose their conn fail cleanly (EXIT with
  error) and the slot is released — no leak.
- Slot accounting stays the worker's existing semaphore; the pool is orthogonal.

## Language agents

- **Python (`zygote_agent.py`)** — P0, lowest risk. Pure-Python + ctypes, lifted directly
  from `zygote_hardened.py` + `isolation_probe.py`. Pre-imports `manifest.preimport`,
  binds the relay port, accept loop, per-job: write files, socketpairs, double-fork +
  full Rule-#2 hardening, dup2 stdio, scrub fds>2, cgroup-place, `runpy.run_path(entry,
  run_name="__main__")`, relay loop (select), cpu push from the child cgroup `cpu.stat`,
  EXIT on child reap. No external deps beyond the stdlib + the already-baked sci stack.
- **R (`zygote_agent.R`)** — P1. R cannot call `unshare`/`mount`/`prctl` without native
  code. Implement a tiny **C helper `libzygote_hard.so`** (compiled into the R image) that
  exposes the syscall primitives + the double-fork-and-harden routine, called from R via
  `.C`/`.Call`; the relay + accept loop can be done by the same C helper so R only does
  `library(...)` then `repeat { job <- zyg_accept(); setwd(job$dir); source(job$entry);
  quit("no") }`. **If the R native path cannot be made solid and tested within this
  milestone, ship R on the Docker tier** (remove `preimport` from `languages/r-4.4/
  manifest.json`) so R STILL WORKS in prod, and record an explicit follow-up. Python
  must not be blocked on R.

## Config / gating (ZDEP-01..03)

- New env in `internal/config`: `ZYGOTE_ENABLED` (bool, default false), plus knobs:
  `ZYGOTE_POOL_IDLE_MS`, `ZYGOTE_RELAY_PORT`, `ZYGOTE_UID_BASE`, `ZYGOTE_POOL_MEMORY_MB`.
- Worker boot (`apps/worker/main.go`): build `DockerSocketRunner` always; if
  `ZYGOTE_ENABLED`, build `ZygoteRunner` and wrap both in a `TieredRunner`; else use the
  Docker runner directly. Safe default = Docker for everything.
- Fly: the worker's `deploy/fly/worker/fly.toml` must allow launching privileged pool
  containers (it already runs dind per the existing deploy + `fly-worker-needs-ext4-volume`
  memory). Document the privilege requirement and set `ZYGOTE_ENABLED=true` only on the
  Fly worker.

## Observability (ZOBS-01..02)

Reuse the existing OTel setup (Phase 8). Emit: per-language warm-parent gauge, fork/spawn
latency histogram, parent reap/respawn counters. The zygote terminal/kill paths increment
the **same** runner-agnostic domain counters as the Docker path so dashboards stay uniform.

## Testing strategy (do NOT burn Fly for iteration)

- Unit-test the Go relay framing, pool manager, TieredRunner routing with fakes.
- Integration-test the real agent **locally in a privileged container** (Docker Desktop,
  cgroup v2): `docker run --privileged --cgroupns=host ...` the language image running the
  agent, then drive the relay protocol from Go. Mirror the spike's `run.sh` flags.
- Phase 14 abuse + isolation suite must reach **Phase 4 parity** for the zygote path
  (fork bomb, OOM, infinite loop, idle, EOF, giant output) plus cross-child isolation
  (no sibling mem/proc/tmp/FD) — port `internal/worker/abuse_test.go` patterns and
  `isolation_probe.py` assertions.
- Reserve Fly for the FINAL prod deploy + one E2E verification of all 4 languages.

## Sequencing (phase scope)

- **Phase 10 — DONE** (preimport contract + manifests + `ZygoteEligible`/`Preimports`).
- **Phase 11** — agents + per-child hardening: `zygote_agent.py` (full), R native helper
  or Docker-tier fallback, the relay protocol on the agent side, local privileged test.
- **Phase 12** — Go `ZygoteRunner` + `zygoteSandbox` + warm pool manager + relay client;
  satisfy the Runner/Sandbox interface; unit + local integration tests.
- **Phase 13** — `TieredRunner` + worker wiring + config gating + Fly deploy config; all 4
  languages E2E through the tiered runner; Docker fallback proven.
- **Phase 14** — abuse + isolation + density + no-leak suite (Phase 4 parity) + pool OTel.

Each phase: build (`go build ./...`, `go test ./...`, `pnpm -r test` where relevant),
atomic commit ending with the `Co-Authored-By: Claude Opus 4.8 (1M context)` trailer, keep
the existing `DockerSocketRunner` path 100% intact.
