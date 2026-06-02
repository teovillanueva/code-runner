# Roadmap: code-runner

## Overview

code-runner is built bottom-up so the hardest mechanism — a live interactive stdin session governed by three independent clocks — is proven on a single language (Python) before fanning out. We lock the load-bearing data contract and interfaces first (manifest schema, `Runner`/`Sandbox`, `Queue`, `StdinTransport`), then build the high-risk Docker-hardened runner with its three clocks and full hardening flags. Phase 3 wires the queue, stdin pub/sub, start-handshake, soketi publisher, and the Python package into the first end-to-end interactive execute (shipped with the docker-compose stack and TS API stub). Lifecycle/cleanup hardening (one idempotent teardown) lands before any scale work, because leaks compound under concurrency. Statelessness and capacity accounting then make it multi-replica safe; the language fan-out (Rust/R/SQLite) proves the zero-core-change extensibility invariant; and the abuse suite on Linux CI plus the README close out v1.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [ ] **Phase 1: Foundation & Manifest Schema** - Module scaffold, manifest contract, and the load-bearing interfaces (Runner/Sandbox, Queue, StdinTransport)
- [ ] **Phase 2: Sandbox Hardening & Runner** - Docker-hardened runner with three clocks, full hardening flags, tree-kill, and output caps
- [ ] **Phase 3: Interactive Python End-to-End** - Queue + stdin pub/sub + start-handshake + soketi + Python package, run end-to-end via docker compose
- [ ] **Phase 4: Lifecycle & Cleanup Hardening** - One idempotent teardown across every terminal path; no leaked containers, subscriptions, or slots
- [ ] **Phase 5: Statelessness & Scale** - Slot-based capacity, backpressure 429s, reliable claim, dead-worker reaper, N replicas
- [ ] **Phase 6: Language Fan-out (Rust, R, SQLite)** - Three more language packages with zero core changes, exercising the compile path
- [ ] **Phase 7: Abuse Suite & Docs** - Fork bomb / OOM / loop / idle / EOF / giant-output tests on Linux CI, plus the README

## Phase Details

### Phase 1: Foundation & Manifest Schema
**Goal**: The data contract and every swap-boundary interface exist and are proven, so "drop a folder = a new language" and "swap Docker→gVisor / pub-sub→Streams" never require core changes later.
**Mode:** mvp
**Depends on**: Nothing (first phase)
**Requirements**: LANG-01, LANG-02, LANG-03, LANG-04, RUN-01, STDIN-04
**Success Criteria** (what must be TRUE):
  1. The core boots, discovers every `languages/<lang-version>/` folder, and lists the available languages with zero language names hardcoded in Go
  2. A `manifest.json` declaring `language, version, aliases, image, entrypoint, compile (nullable), run, defaultLimits{wallTimeMs,idleMs,cpuMs,memoryMb,pids,outputKb}, interactive` loads, validates, and rejects a malformed manifest with a clear error
  3. A per-request `limits` payload visibly overrides the manifest `defaultLimits` for the resolved spec
  4. The `Runner`/`Sandbox` and `StdinTransport` interfaces compile with a stub/no-op implementation behind them, demonstrating the swap seam without touching callers
**Plans**: TBD

### Phase 2: Sandbox Hardening & Runner
**Goal**: A single Docker-hardened runner can create an ephemeral sandbox, attach demuxed pipes, enforce three independent clocks and all hardening flags, and destroy the whole process tree on kill — the highest-risk mechanisms that are expensive to retrofit.
**Mode:** mvp
**Depends on**: Phase 1
**Requirements**: RUN-02, RUN-03, RUN-04, HARD-01, HARD-02, HARD-03, HARD-04, HARD-05, LIM-01, LIM-02, LIM-03, LIM-04
**Success Criteria** (what must be TRUE):
  1. The runner launches a container via the mounted host socket (no Docker-in-Docker), attaches and demuxes stdout/stderr separately, and a test asserts every hardening flag is set: `--network=none`, `--read-only` + size-capped tmpfs, `--memory == --memory-swap`, `--pids-limit`, `--cpus`, `--cap-drop=ALL`, `--security-opt=no-new-privileges`, restrictive seccomp, non-root user
  2. A `kill` removes the container and a follow-up check shows no surviving process tree (tree-kill, never a bare PID kill)
  3. Each of the three clocks independently terminates the sandbox: wall-clock on total lifetime, idle on no-activity, and the cgroup CPU clock on accumulated compute (a "read one byte then spin" program is caught by the CPU clock, not just the wall clock)
  4. stdout/stderr bytes are capped: output is truncated, `truncated=true` is reported, and the worker keeps draining the pipe so the process never blocks
  5. The CPU-clock and OOM code is cgroup-version-aware (detects v1 vs v2 and reads the correct files)
**Plans**: TBD

### Phase 3: Interactive Python End-to-End
**Goal**: A client can run an interactive Python session end-to-end — submit, subscribe, start, type stdin, see streamed output, and get a terminal result — through the full executor → queue → worker → sandbox → soketi path, runnable locally with `docker compose up`.
**Mode:** mvp
**Depends on**: Phase 2
**Requirements**: API-01, API-02, API-03, API-04, API-05, API-06, API-08, QUEUE-01, SESS-01, SESS-02, SESS-03, STDIN-01, STDIN-02, STDIN-03, OUT-01, OUT-02, OUT-03, OUT-04, LANG-05, DEV-01, DEV-02, DEV-03
**Success Criteria** (what must be TRUE):
  1. `POST /execute` returns `202 {jobId, status:"queued"}` before the process starts; the process only launches on `POST /run/:jobId/start` after the client has subscribed, so the first Python prompt is never lost (start-handshake)
  2. A running Python session accepts `POST /run/:jobId/stdin` keystrokes routed via `PUBLISH stdin:<jobId>` to the owning worker (no service discovery), and `POST /run/:jobId/stdin/close` delivers EOF exactly once so an `input()`/REPL loop exits cleanly rather than idle-timing-out
  3. The client receives `stage`, `stdout`, `stderr`, and a terminal `result {exitCode, signal, timedOut, idleTimedOut, truncated, durationMs}` on `private-run-<jobId>`, published via the Pusher HTTP API batched and chunked under soketi's event-size limit
  4. A batch (no-stdin) Python program runs correctly as the degenerate case of the same interactive model
  5. The API authenticates the TS-API caller (shared-secret/bearer) and rejects unknown `jobId`, unknown `language`/`version`, and malformed payloads with clear errors; `POST /run/:jobId/kill` terminates a session
  6. `docker compose up` brings up executor + worker + redis + soketi + TS-API stub, and a documented script drives a full punta-a-punta interactive Python execute against the local stack
**Plans**: TBD

### Phase 4: Lifecycle & Cleanup Hardening
**Goal**: Every way a session can end funnels into one idempotent teardown that runs exactly once, leaving no orphaned container, leaked subscription, or stuck slot — proven before any concurrency is added.
**Mode:** mvp
**Depends on**: Phase 3
**Requirements**: LIFE-01, LIFE-02, SESS-04
**Success Criteria** (what must be TRUE):
  1. On any terminal event (wall/idle/CPU timeout, normal result, or `/kill`) the worker unsubscribes `stdin:<jobId>`, closes pipes, removes the sandbox, and frees the slot
  2. Two simultaneous terminal events (e.g. CPU clock firing as the process exits) trigger cleanup exactly once via `sync.Once` — no double-free, no `stdin` written to a destroyed session
  3. A warm-up timeout reclaims the slot and tears down the sandbox if `/start` never arrives after `/execute`
  4. After a kill/timeout abuse run, `docker ps -a` shows no surviving containers and capacity returns to full
**Plans**: TBD

### Phase 5: Statelessness & Scale
**Goal**: The executor and workers run as N stateless replicas with capacity counted in live sandboxes, backpressure surfaced as 429s, and a reaper that recovers a dead worker's orphaned sandboxes and slots.
**Mode:** mvp
**Depends on**: Phase 4
**Requirements**: QUEUE-02, QUEUE-03, API-07, LIM-05, SCALE-01, SCALE-02, SCALE-03
**Success Criteria** (what must be TRUE):
  1. A worker only claims a job when it has a free sandbox slot (acquire-before-claim), and each worker enforces a max concurrent-live-sandbox count derived from CPU/RAM headroom
  2. When all workers are at capacity, the queue grows and `/execute` returns `429` rather than silently dropping work; the pending-stdin byte cap and stdin rate limit also surface as `429` at the API
  3. `docker compose up --scale worker=2` runs the system across multiple replicas, and stdin published for a job reaches only the replica that owns it
  4. Killing a worker mid-session leaves no orphaned host containers: a label-based reaper removes sandboxes whose owning worker is gone, and slot accounting (Redis + TTL) reclaims its slots
**Plans**: TBD

### Phase 6: Language Fan-out (Rust, R, SQLite)
**Goal**: Three additional languages — including a compiled one and an interactive SQL shell — work by adding only a folder + image, proving the manifest extensibility invariant required zero Go core changes.
**Mode:** mvp
**Depends on**: Phase 5
**Requirements**: LANG-06, LANG-07, LANG-08
**Success Criteria** (what must be TRUE):
  1. A Rust package compiles with `rustc -O` as a distinct hardened, network-none compile stage with its own limits (a compile-bomb is killed as a tree), then runs the produced binary
  2. An R 4.4 package runs `Rscript main.R` with common libs baked in
  3. A SQLite 3 package runs SQL against an ephemeral in-memory DB, supporting both a `.sql` file and an interactive `sqlite3` shell that reads stdin and exits cleanly on EOF
  4. Adding all three required no changes to the Go core — only `manifest.json` + `Dockerfile` per language
**Plans**: TBD

### Phase 7: Abuse Suite & Docs
**Goal**: The full abuse test suite proves the safety guarantees on real Linux cgroups in CI, and the README documents how to run the stack, the API contract, and how to add a language.
**Mode:** mvp
**Depends on**: Phase 6
**Requirements**: TEST-01, TEST-02, TEST-03, TEST-04, TEST-05, TEST-06, TEST-07, DOCS-01, DOCS-02, DOCS-03
**Success Criteria** (what must be TRUE):
  1. Automated abuse tests prove: a fork bomb is contained by `--pids-limit`, an OOM program is killed by the memory cap without taking down the worker, an infinite loop is killed by the wall-clock, a stdin-blocked program is killed by the idle timeout, an EOF-reading program terminates correctly on `/stdin/close`, and a giant-output program is truncated with `truncated=true` without exhausting memory
  2. The abuse suite runs on Linux CI (not only macOS dev) so real cgroup OOM and CPU-accounting behavior is exercised
  3. The README documents how to run the stack locally, the Executor internal API contract, and the "add a new language" package guide
**Plans**: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3 → 4 → 5 → 6 → 7

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundation & Manifest Schema | 0/TBD | Not started | - |
| 2. Sandbox Hardening & Runner | 0/TBD | Not started | - |
| 3. Interactive Python End-to-End | 0/TBD | Not started | - |
| 4. Lifecycle & Cleanup Hardening | 0/TBD | Not started | - |
| 5. Statelessness & Scale | 0/TBD | Not started | - |
| 6. Language Fan-out (Rust, R, SQLite) | 0/TBD | Not started | - |
| 7. Abuse Suite & Docs | 0/TBD | Not started | - |
