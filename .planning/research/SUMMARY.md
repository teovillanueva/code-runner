# Project Research Summary

**Project:** code-runner
**Domain:** Internal sandboxed remote code execution engine (Piston-style, Go) with live interactive stdin sessions
**Researched:** 2026-06-02
**Confidence:** HIGH

## Executive Summary

code-runner is an internal, horizontally-scalable sandboxed code execution engine written in Go. It follows the Piston model (manifest-driven language packages, pre-baked images, compile/run split) but adds the genuinely hard requirement competitors skip: a **live interactive stdin session** with a start-handshake and three independent clocks. The system decomposes into two stateless Go binaries (Executor API + Worker) decoupled entirely through Redis, with output pushed to clients via soketi/Pusher. It is explicitly not a public SaaS — the TS API is the only trusted caller, which eliminates auth/quota complexity and lets the service focus on correctness, safety, and interactive latency.

The recommended approach is to build **bottom-up**, proving the interactive Python end-to-end path first before fanning out to other languages. The two highest-risk areas are the **Runner** (Docker SDK hardening + three clocks) and the **Session goroutine tree** (`sync.Once` cleanup, pipe backpressure, idle/CPU clocks). The stack is fully prescribed: Go 1.26, chi v5, go-redis v9, Docker moby SDK v28.5.2, pusher-http-go v5, and a thin roll-your-own Redis list/streams queue (no job-queue library).

The central operational risk is **correctness under the many terminal paths of a live session** — any path that fails to atomically destroy the sandbox, unsubscribe stdin, and free the slot bleeds capacity to zero and accumulates containers on the host. A second high-severity risk is the **wall-clock-only trap**: for interactive sessions, only a cgroup CPU clock catches compute hidden behind stdin reads. Both are preventable if hardening is built into Phase 2 and never retrofitted.

## Key Findings

### Recommended Stack

Single Go module, two binaries (`cmd/executor`, `cmd/worker`) sharing `internal/`. Every external dependency sits behind a narrow interface so the two mandated future swaps (Docker→gVisor, pub/sub→Streams) are impl/config changes, never core rewrites. See `STACK.md` for versions, rationale, and the "do not use" list.

**Core technologies:**
- **Go 1.26.x** — required by spec; strong concurrency + container-runtime integration
- **chi v5.3.0** — stdlib `net/http`-native router for the 6-route internal API; no framework lock-in (Gin/Echo rejected as over-scoped)
- **go-redis/v9 v9.20.0** — queue (LIST), per-job stdin/ctrl pub/sub, metadata hashes; Streams (`XREAD BLOCK` + consumer groups) is the documented guaranteed-delivery upgrade behind a `StdinTransport` interface
- **docker/docker v28.5.2 (moby SDK)** — `ContainerCreate`/`ContainerAttach`/`ContainerKill`; all Docker-aware code isolated in `internal/runner/docker`; gVisor swap = `HostConfig.Runtime="runsc"` and nothing else
- **pusher-http-go/v5 v5.1.1** — output-only publish to soketi via `Host` override (note: maintenance-mode lib; pin version, trigger endpoint is trivially re-implementable)
- **Roll-your-own Redis queue** behind a `Queue` interface — asynq/river rejected (run-to-completion model, wrong fit for long-lived interactive slot-holding sessions; river also needs Postgres)

### Expected Features

Batch execution is the no-stdin subcase of interactive — **one model delivers both**. See `FEATURES.md`.

**Must have (table stakes):**
- Multi-file submission, resource limits (memory/pids/output caps), stdin-as-buffer, exit-code/signal result reporting
- Manifest-driven language packages (Piston model): `language, version, aliases, image, entrypoint, compile (nullable), run, defaultLimits{wallTimeMs,idleMs,cpuMs,memoryMb,pids,outputKb}, interactive`
- Output event model: `stage(queued|compiling|running)`, `stdout`, `stderr`, `result(exitCode, signal, timedOut, idleTimedOut, truncated, durationMs)`

**Should have (the differentiators — these ARE the product):**
- **Streaming interactive stdin session** (process kept alive, pipes open) — only e2b does this well, and via heavy Firecracker VMs we don't need
- **Start-handshake** (`queued` → client subscribes → explicit `/start`) — the subtle correctness feature competitors skip; without it a program that prints a prompt on launch loses its first output
- **Three independent clocks**: wall (total lifetime), idle (since last activity — exists only because of interactivity), CPU/cgroup (cumulative compute)
- **Output buffering control** (`python -u`, `stdbuf`, etc.) — mandatory v1 concern: streaming UX silently breaks without it (interpreters buffer stdout when not on a TTY)

**Defer (v2+):**
- Redis Streams guaranteed stdin delivery, gVisor runtime, third-party crate/CRAN vendoring beyond baked-in libs, PTY allocation if pipe unbuffering proves unmanageable

**Anti-features (deliberately NOT built):** end-user auth, Pusher channel authz (TS API owns it), runtime package install, network access from sandboxes, persistent/pausable sandboxes, Docker-in-Docker.

### Architecture Approach

Two stateless binaries that **never talk directly** — all coupling is indirect through Redis. The only mutable live state in the system is the `Session` inside the worker process that launched a given sandbox. See `ARCHITECTURE.md`.

**Major components:**
1. **Executor API** (`cmd/executor`) — accepts the internal contract, enqueues jobs, publishes stdin/ctrl blindly to Redis; stateless
2. **Worker** (`cmd/worker`) — claims jobs, owns the live sandbox + session goroutine tree, subscribes to `stdin:<id>`/`ctrl:<id>` only for jobs it owns (ownership-by-subscription = no service discovery)
3. **Runner interface** (`internal/runner`) — `create hardened sandbox / attach pipes / enforce 3 clocks + cgroup limits / kill / cleanup`; Docker-hardened impl now, gVisor later
4. **Redis** — job queue (LIST/Streams) + per-job stdin/ctrl pub/sub + metadata + slot accounting
5. **soketi publisher** (`internal/publisher`) — output-only Pusher HTTP publish on `private-run-<jobId>` with batching + sub-10KB chunking
6. **Language packages** (`languages/<lang-version>/`) — manifest + Dockerfile; core stays language-agnostic

### Critical Pitfalls

(Top items from `PITFALLS.md` — 13 documented, each with warning signs + prevention + phase mapping.)

1. **Wall-clock-only is insufficient for interactive** — a "read one byte then spin" attacker hides compute behind stdin reads; only the cgroup **CPU clock** catches it. Build it into the Runner from Phase 2; retrofit cost is high.
2. **Kill must destroy the container (`docker rm -f`), never kill the PID** — `Process.Kill()` orphans the process tree (esp. Rust compiler→linker chain). Only the PID namespace + container removal guarantees a clean tree-kill.
3. **Every terminal path must call one idempotent teardown** — there are ≥8 ways a session ends (wall/idle/CPU/kill/exit/output-cap/panic/worker-death). A single `sync.Once` `terminate()` prevents the double-cleanup race and leaks.
4. **Output buffering breaks streaming silently** — force per-language unbuffering or the interactive demo shows nothing until exit.
5. **cgroup v1 vs v2 OOM scope differs** and macOS Docker Desktop (v2 in a VM) hides v1 prod behavior — the abuse suite **must run on Linux CI**, not just the dev Mac.

## Implications for Roadmap

Based on research, suggested **7-phase** structure (bottom-up; Python E2E before language fan-out):

### Phase 1: Foundation & Manifest Schema
**Rationale:** Lock module scaffold + manifest schema before anything reads it; prove the "drop a folder = zero core changes" invariant up front.
**Delivers:** Go module layout, `cmd/executor` + `cmd/worker` skeletons, manifest loader, JSON schema, the load-bearing interfaces (`Runner`, `Sandbox`, `Queue`, `StdinTransport`).
**Addresses:** manifest-driven languages, extensibility.
**Avoids:** Pitfall 13 (manifest shape forces zero-core-change for Rust compile later).

### Phase 2: Sandbox Hardening & Runner
**Rationale:** Highest-risk phase; the three things expensive to retrofit (CPU clock, kill=destroy-container, cgroup-version-aware code) must land here.
**Delivers:** Docker hardened Runner impl + three clocks + full hardening flags (network=none, read-only+tmpfs, no-swap memory, pids-limit, cpus, cap-drop=ALL, no-new-privileges, seccomp).
**Uses:** moby SDK v28, cgroup v1/v2 stats.
**Implements:** Runner/Sandbox interfaces.

### Phase 3: Interactive Python End-to-End
**Rationale:** First full demo; exercises interactive stdin + three clocks + buffering on one language.
**Delivers:** Redis queue + stdin/ctrl pub/sub + start-handshake + session goroutine tree + soketi publisher + Python 3.12 package. `/execute → subscribe → /start → stdin → result` works.
**Addresses:** streaming interactive session, start-handshake, output event model.
**Avoids:** Pitfalls 1, 4 (CPU clock, buffering).

### Phase 4: Lifecycle / Cleanup Hardening
**Rationale:** Must precede scale work — leaks compound under concurrency.
**Delivers:** Single `sync.Once` teardown verified on every terminal path; no leaked containers/subscriptions/slots.
**Avoids:** Pitfalls 2, 3 (tree-kill, idempotent teardown).

### Phase 5: Statelessness & Scale
**Rationale:** Multi-replica correctness and capacity accounting.
**Delivers:** Reliable job claim (LMOVE/processing list), dead-worker detection, label-based container reaper, Redis-backed slot TTL + backpressure → 429, multi-worker validation.
**Avoids:** orphaned containers on worker crash, slots never freed, stdin routed to dead worker.

### Phase 6: Language Fan-out (Rust, R, SQLite)
**Rationale:** Proves the manifest extensibility invariant with zero core changes.
**Delivers:** Rust (compile stage with own limits), R 4.4, SQLite 3 (interactive SQL shell), each as manifest + Dockerfile.
**Addresses:** the 4 initial languages; compile vs interpreted handling.

### Phase 7: Abuse Test Suite & Hardening Audit
**Rationale:** Verification backbone; every critical pitfall maps to a concrete test.
**Delivers:** fork bomb, OOM, infinite loop (CPU clock), stdin-blocked idle, EOF (stdin/close), giant output (truncation), poison job, worker mid-session kill — all on **Linux CI**. README finalized.

### Phase Ordering Rationale

- Bottom-up: hardening + cleanup must precede slot accounting; manifest model precedes any language; stage events precede the start-handshake.
- Python first (Phase 3) because it exercises every hard mechanism (interactive stdin, three clocks, buffering) on a single interpreted language before fan-out.
- docker-compose E2E and the abuse suite come after the single-language path works, so they validate a real system rather than scaffolding.

### Research Flags

Phases likely needing deeper research during planning:
- **Phase 2:** seccomp allowlist per language (start from Docker default, tune by observing SIGSYS); verify moby v28 `HostConfig` field names against godoc; cgroup v1/v2 detection.
- **Phase 3:** per-language output buffering under non-TTY pipes (R, SQLite empirical); PTY-vs-pipe revisit if buffering is unmanageable.
- **Phase 5:** LMOVE reliable-list vs Stream consumer group trade-off; heartbeat TTL sizing.

Phases with standard patterns (skip research-phase):
- **Phase 1:** Go module layout, JSON schema, multi-stage Dockerfile — well-documented.
- **Phase 4:** `sync.Once` goroutine teardown is textbook Go — discipline, not research.
- **Phase 6:** manifest + Dockerfile additions only; no new integrations.
- **Phase 7:** test cases fully specified in PITFALLS.md "looks done but isn't" checklist.

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | Versions verified against module proxy 2026-06-02 |
| Features | HIGH | Spec from PROJECT.md (authoritative) + validated against Piston/Judge0/e2b |
| Architecture | HIGH | Core design sound; Docker/Redis/Pusher semantics verified; soketi limits MEDIUM (configurable) |
| Pitfalls | HIGH | Hardening flags + cgroup OOM scope verified against official docs |

**Overall confidence:** HIGH

### Gaps to Address

- **Per-language buffering under pipes** — validate empirically during Phase 3 (Python) and Phase 6 (R, SQLite).
- **seccomp allowlist tuning per language** — start from Docker default, observe SIGSYS, tighten in Phase 2/6.
- **gVisor per-language syscall parity** — validate empirically before declaring the runtime swap done (deferred).
- **soketi payload/batch limits** — size `outputKb` and flush window against the shipped soketi config (MEDIUM, configurable).
- **cgroup version in prod** — pin/assert v2 in CI, or carry version-aware code if v1 must be supported.

## Sources

### Primary (HIGH confidence)
- proxy.golang.org `@latest` + official repos (go-redis, moby/moby, pusher-http-go, chi) — current versions, 2026-06-02
- [Docker resource constraints](https://docs.docker.com/engine/containers/resource_constraints/) — cgroup v1/v2 OOM scope, swap, pids
- [gVisor performance guide](https://gvisor.dev/docs/architecture_guide/performance/) + [syscall subset](https://github.com/google/gvisor/blob/master/test/syscalls/README.md)
- [Piston API v2 docs](https://piston.readthedocs.io/en/latest/api-v2/) + [engineer-man/piston](https://github.com/engineer-man/piston)
- [Judge0 CE API](https://ce.judge0.com/), [e2b PTY docs](https://e2b.dev/docs/sandbox/pty)
- [soketi events & channels limits](https://docs.soketi.app/rate-limiting-and-limits/events-and-channels-limits) — ~10KB event cap

### Secondary (MEDIUM confidence)
- [Northflank RCE sandbox hardening guide](https://northflank.com/blog/remote-code-execution-sandbox)
- soketi configurable payload/batch limits (depends on shipped config)

---
*Research completed: 2026-06-02*
*Ready for roadmap: yes*
