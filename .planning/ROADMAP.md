# Roadmap: code-runner

## Overview

code-runner is an open-source, self-hostable, Piston-style remote code execution service built as a polyglot monorepo: a thin Hono/TypeScript API gateway, a Go worker that orchestrates hardened sandboxes and keeps live interactive sessions alive, manifest-driven language packages, and a shared wire contract that is the fragile seam between the two languages. The journey is deliberately bottom-up: first lock the monorepo layout, the JSON-Schema wire contract (TS types + zod validators + Go structs + CI drift check), the manifest schema/loader, and the `Runner` interface skeleton — and **stop for human approval before any implementation**. Then build the highest-risk core (the Go runner with Docker hardening, three clocks, tree-kill, and idempotent cleanup), drive one language (Python) end-to-end through the whole interactive path (Hono API → Redis → worker session → soketi), and immediately build the abuse test suite that gates everything after it. Only once safety is proven do we fan out to Rust, R, and SQLite, harden statelessness/scale, and ship the OSS release with deployment-per-target docs.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [ ] **Phase 1: Foundation & Wire Contract** - Monorepo layout, JSON-Schema wire contract codegen + drift check, manifest schema/loader, `Runner` interface, OSS scaffolding — ends at a human approval gate before coding
- [x] **Phase 2: Sandbox Hardening & Runner** - Go `DockerSocketRunner`: full hardening flags, three clocks, tree-kill, output caps, idempotent `sync.Once` cleanup (completed 2026-06-02)
- [x] **Phase 3: Interactive Python End-to-End** - Hono API (all `/v1/*`), Redis queue + stdin/control routing, worker session, soketi publisher, Python package, docker compose dev stack — full interactive demo works (completed 2026-06-02)
- [x] **Phase 4: Abuse Suite & Safety Validation** - Fork bomb, OOM, infinite loop, idle, EOF, giant output on Linux CI — gates the language fan-out (completed 2026-06-02)
- [x] **Phase 5: Statelessness & Scale** - Slot capacity, backpressure 429, reliable claim, dead-worker reaper, N replicas, autoscaling-by-queue-depth + scale-to-zero design, native-Redis worker constraint (completed 2026-06-02)
- [x] **Phase 6: Language Fan-out** - Rust (compile stage), R 4.4, SQLite 3 (interactive SQL shell) added as manifest + Dockerfile, zero core changes (completed 2026-06-03)
- [x] **Phase 7: OSS Release & Deployment** - README quickstart, API contract, add-a-language guide, deployment-per-target, channel-auth documentation (completed 2026-06-03)

## Phase Details

### Phase 1: Foundation & Wire Contract
**Goal**: Establish the polyglot monorepo, the single-source-of-truth wire contract, the manifest model, and the swap seams — then **stop and get human approval on the layout + contract + manifest schema before any implementation proceeds**.
**Mode:** mvp
**Depends on**: Nothing (first phase)
**Requirements**: CONT-01, CONT-02, CONT-03, CONT-04, CONT-05, CONT-06, LANG-01, LANG-02, LANG-03, RUN-01, STDIN-04, CFG-04, OSS-01, OSS-02
**Success Criteria** (what must be TRUE):
  1. The monorepo layout exists (`apps/api`, `apps/worker`, `packages/contract`, `languages/`) and both apps build empty.
  2. A single canonical JSON Schema in `packages/contract` defines every wire message (job spec, stdin chunk, control start/kill/stdin-close, output stage/stdout/stderr/result); `make contract` generates TS types + zod validators + Go structs from it.
  3. `make contract-check` regenerates the artifacts and fails CI on any diff (drift check proven by intentionally editing the schema).
  4. A `manifest.json` schema is defined and a loader reads all `languages/*/manifest.json` at boot, exposing the declared languages with zero language identifiers hardcoded in Go or the API.
  5. The load-bearing interfaces exist as skeletons: `Runner`/`Sandbox` (create/attach/limits/kill/cleanup) and `StdinTransport` (so Redis pub/sub can later swap to Streams), with the native-Redis-for-worker constraint recorded.
  6. OSS scaffolding is in place: MIT `LICENSE` and a `.env.example` documenting every env var; the layout + contract + manifest schema are presented and **explicitly approved by the human before implementation begins**.
**Plans**: 3 plans
Plans:
- [ ] 01-01-PLAN.md — Go internal/keys (contract mirror) + internal/manifest loader/validator/resolver + sample python manifest; verify committed contract & OSS scaffolding
- [ ] 01-02-PLAN.md — Runner/Sandbox interface + StdinTransport interface (stubs) and native-Redis-for-worker constraint (config + docs)
- [ ] 01-03-PLAN.md — Worker boot entrypoint (loads manifests, lists languages, wires stubs) + shared TS manifest loader so the API is non-hardcoded

### Phase 2: Sandbox Hardening & Runner
**Goal**: Build the highest-risk component — a hardened Go `DockerSocketRunner` that creates an ephemeral container per execution, enforces the three clocks and resource caps, tree-kills, and tears down idempotently — so safety is baked in, never retrofitted.
**Mode:** mvp
**Depends on**: Phase 1
**Requirements**: RUN-02, RUN-03, RUN-04, HARD-01, HARD-02, HARD-03, HARD-04, HARD-05, LIM-01, LIM-02, LIM-03, LIM-04, OUT-04, LIFE-01, LIFE-02
**Success Criteria** (what must be TRUE):
  1. `DockerSocketRunner` launches an ephemeral container per execution via the mounted host socket (no Docker-in-Docker) and demuxes stdout/stderr separately to the session.
  2. Every sandbox runs fully hardened: `--network=none`, `--read-only` + size-capped tmpfs `/tmp`, `--memory==--memory-swap`, `--pids-limit`, `--cpus`, `--cap-drop=ALL`, `--security-opt=no-new-privileges`, restrictive seccomp, non-root user.
  3. Three independent clocks each kill the sandbox: wall-clock (unconditional), idle (no stdout/stdin in the window), and CPU/cgroup (accumulated compute), so compute hidden behind stdin reads is still caught.
  4. `kill` destroys the whole container (process tree), never a single PID, and the worker keeps draining stdout/stderr while truncating at the byte cap so the process never blocks (`truncated=true` reported).
  5. The worker triggers soketi directly via the Pusher protocol using env credentials, batched/chunked under soketi's event-size limit.
  6. A single idempotent `sync.Once` teardown runs exactly once across every terminal path (wall/idle/CPU/kill/exit/output-cap), unsubscribing, closing pipes, removing the container, and freeing the slot — no leaked containers, subscriptions, or slots.
**Plans**: 4 plans
Plans:
- [x] 02-01-PLAN.md — Phase 2 deps + restrictive seccomp profile + testable safety logic (three clocks, output pump, sync.Once teardown) in internal/session
- [x] 02-02-PLAN.md — soketi/Pusher publisher (stage/stdout/stderr/result, env creds, <10KB chunking, monotonic seq) in internal/publisher
- [x] 02-03-PLAN.md — DockerSocketRunner over the moby SDK: full hardening + attach/demux + tree-kill + idempotent cleanup + cgroup-v2 CPU reader
- [x] 02-04-PLAN.md — Guarded Docker integration tests: hardening inspect, three clocks, truncation, stdin round-trip, no-leak label check + make target

### Phase 3: Interactive Python End-to-End
**Goal**: Drive one language (Python 3.12) through the entire interactive path — Hono API, Redis routing, worker session with start-handshake, soketi output — so `/execute → subscribe → /start → stdin → result` works end-to-end against the local stack.
**Mode:** mvp
**Depends on**: Phase 2
**Requirements**: API-01, API-02, API-03, API-04, API-05, API-06, API-07, API-08, API-09, API-10, API-11, WRK-01, WRK-02, WRK-03, WRK-04, SESS-01, SESS-02, SESS-03, STDIN-01, STDIN-02, STDIN-03, OUT-01, OUT-02, OUT-03, LANG-04, LANG-05, CFG-01, CFG-02, CFG-03, CHAN-02, DEV-01, DEV-02, DEV-03
**Success Criteria** (what must be TRUE):
  1. The Hono API serves all endpoints — `POST /v1/execute` (returns `202 {jobId, channel, status:"queued"}` before the process starts), `/start`, `/stdin`, `/stdin/close`, `/kill`, `GET /v1/jobs/:id`, `GET /v1/languages` — with constant-time `EXECUTOR_API_TOKEN` auth, contract-derived validation with clear errors, and a per-job stdin rate-limit + pending-byte cap returning `429`; it talks to the worker only via Redis.
  2. The start-handshake holds: the process only starts on `/start` after the client subscribes, so no early prompt is lost; batch (no-stdin) runs as the degenerate case; a warm-up timeout reclaims the slot if `/start` never arrives.
  3. stdin and control route without service discovery: the API `PUBLISH`es `stdin:<jobId>` and per-job control, and only the owning worker (subscribed to its live jobs) writes to the pipe; `/stdin/close` delivers EOF exactly once.
  4. The worker keeps the Python process alive with pipes open and publishes `stage`, `stdout`/`stderr`, and a terminal `result` event on `private-run-<jobId>`; per-request `limits` override the manifest `defaultLimits`.
  5. The Python 3.12 package runs `python main.py` with numpy/pandas/requests baked in, driven entirely by its manifest with no hardcoding.
  6. `docker compose up` brings up api + worker + redis + soketi + a stub upstream app; the stub and a documented script drive a full interactive execute end-to-end; all config is env-only (no config or secret-returning endpoints, soketi secret never persisted in Redis).
**Plans**: 5 plans
Plans:
- [x] 03-01-PLAN.md — Go Redis layer: go-redis client, real pub/sub StdinTransport (stdin:/ctrl:), job store + BRPOP queue consumer (Wave 1)
- [x] 03-02-PLAN.md — Worker run loop: claim → create → start-handshake/warm-up → session.RunInteractive (publisher sinks + stdin routing) → single teardown; guarded full-Python integration test (Wave 2)
- [x] 03-03-PLAN.md — Python 3.12 image: python:3.12-slim + numpy/pandas/requests, non-root, PYTHONUNBUFFERED unbuffered streaming (Wave 1)
- [x] 03-04-PLAN.md — Hono API: all /v1/* endpoints, constant-time bearer auth, generated-zod validation, jobId+channel gen, LPUSH+spec/status, PUBLISH stdin/control, 429 rate/byte-cap, optional channel-auth (Wave 1)
- [x] 03-05-PLAN.md — docker-compose + stub upstream + scripts/e2e.sh + README quickstart; blocking human-verify of the live interactive demo (Wave 3)
**UI hint**: yes

### Phase 4: Abuse Suite & Safety Validation
**Goal**: Build the abuse test suite immediately after the Python E2E path and run it on Linux CI so real cgroup OOM/CPU behavior is exercised — this suite is the verification backbone and **gates the language fan-out**.
**Mode:** mvp
**Depends on**: Phase 3
**Requirements**: TEST-01, TEST-02, TEST-03, TEST-04, TEST-05, TEST-06, TEST-07, TEST-08
**Success Criteria** (what must be TRUE):
  1. A fork bomb is contained by `--pids-limit` and the sandbox is killed cleanly without harming the worker.
  2. An OOM program is killed by the memory cap and an infinite loop is killed by the wall-clock timeout, neither taking down the worker.
  3. A program blocked on stdin is killed by the idle timeout, and a program reading to EOF terminates correctly after `/stdin/close`.
  4. A giant-output program is truncated with `truncated=true` and does not exhaust memory (the worker keeps draining).
  5. The entire abuse suite runs on Linux CI (not just macOS dev) and is wired as the gate that must pass before any new language is added.
**Plans**: 2 plans
Plans:
- [x] 04-01-PLAN.md — Adversarial abuse suite (fork bomb, OOM, infinite loop, idle, EOF, giant output + CPU-evasion) through the full worker path, build-tagged + `make abuse`, run green on cgroup v2
- [x] 04-02-PLAN.md — Linux CI workflow (.github/workflows/abuse.yml) running `make abuse` on ubuntu-latest + README gate note tying the language fan-out to a green abuse run

### Phase 5: Statelessness & Scale
**Goal**: Make the API and workers safely horizontally scalable — slot-bounded capacity, backpressure instead of dropped work, reliable claim, a reaper that prevents container/slot leaks on worker death, and a documented autoscaling/scale-to-zero design under the native-Redis-for-worker constraint.
**Mode:** mvp
**Depends on**: Phase 4
**Requirements**: SCALE-01, SCALE-02, SCALE-03, SCALE-04, SCALE-05
**Success Criteria** (what must be TRUE):
  1. The API and workers run as N stateless replicas with no shared in-process state.
  2. A worker claims a job only when it has a free sandbox slot; capacity is counted in concurrent live sandboxes (derived from CPU/RAM), not request bursts, using a reliable claim mechanism.
  3. Queue depth and full-capacity conditions surface as backpressure (`429`) rather than silently dropping work.
  4. Worker death mid-session leaves no orphaned host containers: a label-based reaper removes them and reclaims their slots.
  5. The repo documents the autoscaling-by-queue-depth and scale-to-zero mechanism per deploy target — where the scaling unit is the **worker node** (which launches its sandboxes internally and hosts N concurrent ones), not a microVM per execution — including the requirement that the worker's Redis speak native pub/sub + blocking ops (Upstash is API-only, not worker-viable); the `FlyMachinesRunner` microVM-per-execution model is noted as a v2 option.
**Plans**: 3 plans
Plans:
- [x] 05-01-PLAN.md — Worker slot semaphore (acquire-before-claim) + ephemeral workerId + Redis heartbeat/owned-jobs substrate + new keys/config + concurrency-cap test
- [x] 05-02-PLAN.md — Label-based dead-worker reaper (removes orphaned containers + anonymous volumes, frees slots, marks jobs error) wired into every worker + integration test
- [x] 05-03-PLAN.md — API job-admission 429 (queue-depth backpressure) + autoscale-by-queue-depth/scale-to-zero docs (docs/scaling.md + README) + docker compose --scale worker=2 smoke

### Phase 6: Language Fan-out
**Goal**: Prove the manifest extensibility invariant by adding Rust, R, and SQLite as folder + image with zero core changes — including the SQLite case that stress-tests whether "language = image + compile? + run" holds for something that is not a general-purpose language.
**Mode:** mvp
**Depends on**: Phase 5
**Requirements**: LANG-06, LANG-07, LANG-08
**Success Criteria** (what must be TRUE):
  1. The Rust package compiles with `rustc -O main.rs -o /tmp/prog` (a compile stage with its own limits) then runs the produced binary — added with no changes to the worker or API.
  2. The R 4.4 package runs `Rscript main.R` with common libs baked in, driven by its manifest.
  3. The SQLite 3 package runs SQL against an ephemeral in-memory DB, supporting both a `.sql` file and an interactive `sqlite3` shell reading from stdin.
  4. Each language is added purely as `languages/<lang-version>/{manifest.json, Dockerfile}` and the abuse suite (Phase 4 gate) passes for the new images.
**Plans**: 4 plans
Plans:
- [x] 06-01-PLAN.md — Task 0: generic manifest-driven compile stage (extend Sandbox seam + worker run path + stub + generic gate test + Wave-2 Makefile targets) — the ONLY core change
- [x] 06-02-PLAN.md — Rust 1.83 package (rustc -O compile + produced-binary run) + e2e compile/run + compile-error test
- [x] 06-03-PLAN.md — R 4.4 package (Rscript main.R, common libs, unbuffered streaming, null compile) + e2e Rscript + interactive stdin test
- [x] 06-04-PLAN.md — SQLite 3 package (alpine+sqlite, :memory: SQL, .sql file + interactive shell, clean EOF) + e2e file + interactive SELECT test

### Phase 7: OSS Release & Deployment
**Goal**: Ship the open-source release: a README that gets a self-hoster from clone to a running interactive execute, documents the API contract and how to add a language, covers deployment per target, and documents the upstream channel-auth responsibility (with the optional non-core helper noted).
**Mode:** mvp
**Depends on**: Phase 6
**Requirements**: DOCS-01, DOCS-02, DOCS-03, DOCS-04, CHAN-01
**Success Criteria** (what must be TRUE):
  1. The README quickstart takes a self-hoster from clone to a running local stack and a successful interactive execute.
  2. The README documents the API contract (`/v1/*` endpoints + wire events) accurately against the shipped contract.
  3. The README documents deployment per target: dev (docker compose), prod (Fly Machines/Firecracker workers + native Redis + soketi; API anywhere), and future k8s `RuntimeClass=gvisor`.
  4. The README documents how to add a new language (the package model guide) and how the upstream app authorizes the browser's private soketi channel via the app key/secret (HMAC), noting the optional non-core helper.
**Plans**: 2 plans
Plans:
- [x] 07-01-PLAN.md — Complete README: quickstart, full /v1/* API contract + output events, deployment-per-target, add-a-language guide, channel-auth (DOCS-01..04, CHAN-01)
- [x] 07-02-PLAN.md — Broader CI matrix (.github/workflows/ci.yml): lint, go-unit, js+redis, contract-drift
**UI hint**: yes

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3 → 4 → 5 → 6 → 7

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundation & Wire Contract | 0/3 | Not started | - |
| 2. Sandbox Hardening & Runner | 4/4 | Complete   | 2026-06-02 |
| 3. Interactive Python End-to-End | 5/5 | Complete   | 2026-06-02 |
| 4. Abuse Suite & Safety Validation | 2/2 | Complete   | 2026-06-02 |
| 5. Statelessness & Scale | 3/3 | Complete   | 2026-06-02 |
| 6. Language Fan-out | 4/4 | Complete   | 2026-06-03 |
| 7. OSS Release & Deployment | 2/2 | Complete   | 2026-06-03 |
