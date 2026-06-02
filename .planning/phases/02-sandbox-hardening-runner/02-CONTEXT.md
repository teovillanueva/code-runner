# Phase 2: Sandbox Hardening & Runner - Context

**Gathered:** 2026-06-02
**Status:** Ready for planning
**Mode:** Auto-generated (discuss skipped — autonomous run)

<domain>
## Phase Boundary

Build the highest-risk component: a hardened Go `DockerSocketRunner` (real implementation of the Phase 1 `internal/runner.Runner`/`Sandbox` interfaces) that creates an ephemeral container per execution via the mounted host Docker socket (NO Docker-in-Docker), attaches and demuxes stdin/stdout/stderr, enforces the three clocks and all resource caps, tree-kills, and tears down idempotently. Plus the soketi output publisher (Pusher protocol). Safety is baked in here, never retrofitted.

This phase is about the SANDBOX + RUNNER + CLOCKS + PUBLISHER in isolation. The Redis queue, Hono API, start-handshake wiring, and the Python image build are Phase 3. Phase 2 should be testable directly against Docker with a tiny test image (e.g. `alpine` or `python:3.12-slim` pulled in tests).
</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
Follow `.planning/research/STACK.md` (Docker moby SDK v28, go-redis, pusher-http-go) and `.planning/research/PITFALLS.md` (the load-bearing pitfalls below). Locked facts:
- Implement the existing `internal/runner.Runner`/`Sandbox` interface from Phase 1 with a `DockerSocketRunner` (new file in `internal/runner`, e.g. `docker.go`). Keep the stub for tests.
- Use the moby Docker SDK: `github.com/docker/docker/client` + `ContainerCreate` (HostConfig hardening), `ContainerAttach` (pipes), `ContainerKill`/`ContainerRemove`. Demux stdout/stderr with `stdcopy.StdCopy`.
- **Hardening per container (HARD-01..05):** `NetworkMode=none`, `ReadonlyRootfs=true` + a size-capped tmpfs `/tmp` (the workspace), `Memory==MemorySwap` (no swap), `PidsLimit`, `NanoCPUs` (from cpus), `CapDrop=["ALL"]`, `SecurityOpt=["no-new-privileges"]` + a restrictive seccomp profile (start from Docker's default seccomp or a bundled restrictive profile; document the choice), run as a non-root user.
- **Three clocks (LIM-01..03):** wall-clock (`time.AfterFunc`, kills unconditionally), idle (resettable timer reset on stdout OR stdin activity), CPU/cgroup (poll container stats `cpu.stat`/cgroup v2 usage vs cpuMs). cgroup-v2-aware (this machine is v2). Each clock funnels into ONE terminate path.
- **Output caps (LIM-04):** cap combined stdout+stderr at outputKb, truncate, set `truncated=true`, but KEEP DRAINING the pipe so the process never blocks.
- **Tree-kill (RUN-04):** kill = `ContainerKill` + `ContainerRemove(force)` — destroy the whole container/process tree, never a bare PID.
- **Idempotent cleanup (LIFE-01/02):** a single `sync.Once` `terminate()` called by EVERY terminal path (wall/idle/CPU/normal exit/kill/output-cap/error) that closes pipes, removes the container, and reports the result exactly once. No leaked containers.
- **soketi publisher (OUT-04):** `internal/publisher` using `github.com/pusher/pusher-http-go/v5` with `Host`/`Secure` from env (SOKETI_*). Publish `stage`/`stdout`/`stderr`/`result` events (contract `wire` types, `internal/keys` event names) to `private-run-<jobId>`, batched/chunked under soketi's ~10KB event-size limit, monotonic `seq`. Credentials from env only.
- Sessions/clocks likely live in a new `internal/session` package that drives a `Sandbox` + `Publisher` + the three clocks; or keep clocks inside the runner — planner's discretion, but the `sync.Once` single-terminate invariant is mandatory.
</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Phase 1 foundation (build ON these)
- `internal/runner/runner.go` + `internal/runner/stub.go` — the `Runner`/`Sandbox` interface to implement
- `internal/config/config.go` — worker config (DOCKER_HOST, SANDBOX_RUNTIME, SOKETI_*, limits)
- `internal/keys/keys.go` — channel + event names
- `packages/contract/gen/go/wire/wire.gen.go` — `JobSpec`, `Limits`, `ResultEvent`, `StageEvent`, `OutputChunkEvent`

### Research
- `.planning/research/STACK.md` — Docker SDK approach, versions, soketi publish
- `.planning/research/PITFALLS.md` — CPU-clock-not-wall-clock, kill=destroy-container, single idempotent teardown, output buffering, cgroup v1/v2
- `.planning/research/ARCHITECTURE.md` — Runner interface + session goroutine tree
</canonical_refs>

<specifics>
## Specific Ideas

Phase 2 requirement IDs: RUN-02, RUN-03, RUN-04, HARD-01..05, LIM-01..04, OUT-04, LIFE-01, LIFE-02.

**Testing (this machine HAS Docker, cgroup v2):** write Go integration tests (build tag e.g. `//go:build docker` or skip-if-no-docker) that actually launch containers via the runner and assert: hardening flags present (inspect the created container config), wall/idle/CPU clocks each terminate a container, output truncation sets `truncated=true`, kill removes the container (no leak — `docker ps -a` shows none with the job label), and a basic echo/stdin round-trip streams stdout. Use a small public image (alpine / busybox / python:3.12-slim) pulled in test setup. Unit-test the clock/teardown logic with a fake Sandbox where possible so logic is testable without Docker. Label containers (e.g. `code-runner.jobId`) so the reaper (Phase 5) and tests can find them.
</specifics>

<deferred>
## Deferred Ideas

Redis queue/claim, Hono API, start-handshake transport wiring, slot accounting, dead-worker reaper (Phase 5), the real Python image + compose (Phase 3), the abuse suite (Phase 4). Phase 2 proves the runner+clocks+publisher against Docker directly.
</deferred>
