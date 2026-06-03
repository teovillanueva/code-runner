# Requirements: code-runner

**Defined:** 2026-06-02
**Core Value:** Run untrusted code in a hardened, resource-bounded sandbox with a live interactive stdin session and reliable real-time output — without ever leaking a container, a subscription, or a session slot — and make it trivially self-hostable and extensible.

## v1 Requirements

Requirements for the initial release. Each maps to roadmap phases.

### API Gateway (Hono / TypeScript)

- [x] **API-01**: `POST /v1/execute` with `{language, version, files[], limits?}` returns `202 {jobId, channel, status:"queued"}`; the API generates `jobId`+`channel` and returns before the process starts
- [x] **API-02**: `POST /v1/jobs/:id/start` starts the process (only valid after the client has subscribed)
- [x] **API-03**: `POST /v1/jobs/:id/stdin {chunk}` writes to the process stdin
- [x] **API-04**: `POST /v1/jobs/:id/stdin/close` closes stdin (EOF)
- [x] **API-05**: `POST /v1/jobs/:id/kill` terminates the session
- [x] **API-06**: `GET /v1/jobs/:id` returns the job status
- [x] **API-07**: `GET /v1/languages` lists available languages, sourced from the manifests
- [x] **API-08**: A Hono middleware authenticates the upstream caller with `EXECUTOR_API_TOKEN` using a constant-time comparison and rejects missing/invalid tokens
- [x] **API-09**: The API validates requests against the shared contract and returns clear errors for malformed payloads, unknown `language`/`version`, and unknown `jobId`
- [x] **API-10**: The API enforces a per-job stdin rate limit and a pending-stdin byte cap, returning `429` on overflow (backpressure)
- [x] **API-11**: The API is stateless and communicates with the worker **only** via Redis (enqueue job, `PUBLISH` stdin/control, read job status) — it never calls the worker directly

### Shared Wire Contract (`packages/contract`)

- [ ] **CONT-01**: A single canonical JSON Schema defines every wire message exchanged between the API and the worker
- [ ] **CONT-02**: TypeScript types are generated from the schema and consumed by the Hono API (not hand-written)
- [ ] **CONT-03**: Runtime validators (zod) are generated from the schema so API validation stays in lockstep with the contract
- [ ] **CONT-04**: Go structs (with JSON tags) are generated from the schema and consumed by the worker
- [ ] **CONT-05**: A CI/script drift check regenerates the artifacts and fails on any diff
- [ ] **CONT-06**: The contract covers the job spec, stdin chunk, control messages (start/kill/stdin-close), and output events (stage/stdout/stderr/result)

### Worker & Runner (Go)

- [ ] **RUN-01**: A `Runner`/`Sandbox` interface abstracts "create hardened sandbox, attach pipes, enforce limits, kill, cleanup" so the backend can swap (Docker → gVisor → Firecracker) without touching core logic
- [x] **RUN-02**: A `DockerSocketRunner` launches an ephemeral container per execution via the mounted host socket (no Docker-in-Docker)
- [x] **RUN-03**: The runner attaches and demuxes stdout/stderr separately and forwards them to the session
- [x] **RUN-04**: `kill` destroys the whole container (process tree), never just a PID
- [x] **WRK-01**: The worker consumes jobs from the Redis queue
- [x] **WRK-02**: The worker subscribes to `stdin:<jobId>` only for jobs it owns and writes chunks to the process pipe — no service discovery
- [x] **WRK-03**: The worker keeps the process alive with stdin/stdout/stderr pipes open (interactive session, not batch-ephemeral)
- [x] **WRK-04**: The worker is stateless and coupled to the rest of the system only through Redis (jobs + stdin) and soketi (output); it never calls the API

### Interactive Session & Handshake

- [x] **SESS-01**: The start-handshake holds: `/execute` creates `jobId`+`channel` and returns before the process starts; the process only starts on `/start` (no early prompt is lost)
- [x] **SESS-02**: Batch (no-stdin) execution works as the degenerate case of the interactive model
- [x] **SESS-03**: A warm-up timeout reclaims the slot and tears down the sandbox if `/start` never arrives after `/execute`

### stdin Routing

- [x] **STDIN-01**: stdin is routed without service discovery: the API `PUBLISH`es to `stdin:<jobId>` and the owning worker writes it to the process pipe
- [x] **STDIN-02**: `/stdin/close` delivers EOF to the process exactly once
- [x] **STDIN-03**: Control messages (start/kill) route to the owning worker via a per-job control channel
- [ ] **STDIN-04**: The stdin transport sits behind an interface so Redis pub/sub (MVP) can be swapped for Redis Streams (`XREAD BLOCK`) later without core changes

### Sandbox Hardening

- [x] **HARD-01**: Every sandbox runs with `--network=none`
- [x] **HARD-02**: Every sandbox runs `--read-only` with a size-capped tmpfs `/tmp` workspace
- [x] **HARD-03**: Memory is capped with `--memory` == `--memory-swap` (no swap)
- [x] **HARD-04**: Every sandbox runs with `--pids-limit` and `--cpus` set
- [x] **HARD-05**: Every sandbox runs with `--cap-drop=ALL`, `--security-opt=no-new-privileges`, and a restrictive seccomp profile, as a non-root user

### Resource Limits (three clocks + caps)

- [x] **LIM-01**: A wall-clock timeout kills the entire session unconditionally when exceeded
- [x] **LIM-02**: An idle timeout kills the sandbox when no stdout and no stdin is received within the window
- [x] **LIM-03**: A CPU (cgroup) limit kills the sandbox on accumulated real compute, defeating use of interactive mode to smuggle heavy work past the wall clock
- [x] **LIM-04**: stdout/stderr bytes are capped — output is truncated and `truncated=true` is reported, and the worker keeps draining the pipe so the process never blocks

### Real-time Output (soketi)

- [x] **OUT-01**: The worker publishes `stage {phase: queued|compiling|running}` events on `private-run-<jobId>`
- [x] **OUT-02**: The worker streams `stdout {chunk}` and `stderr {chunk}` events
- [x] **OUT-03**: The worker publishes a terminal `result {exitCode, signal, timedOut, idleTimedOut, truncated, durationMs}` event
- [x] **OUT-04**: The worker triggers soketi **directly** via the Pusher protocol, batched and chunked to stay within soketi's event-size limit, using credentials from env

### Lifecycle / Cleanup

- [x] **LIFE-01**: On any terminal event (any timeout, result, or kill) the worker unsubscribes `stdin:<jobId>`, closes pipes, removes the sandbox, and frees the slot
- [x] **LIFE-02**: Cleanup is idempotent and runs exactly once across all terminal paths (no double-cleanup, no leaked containers/subscriptions/slots)

### Language Packages (manifest-driven)

- [ ] **LANG-01**: Adding a language is adding a `languages/<lang-version>/` folder with `manifest.json` + `Dockerfile` — no changes to the Go worker or the API
- [ ] **LANG-02**: `manifest.json` declares `language, version, aliases, image, entrypoint, compile (nullable), run, interactive, defaultLimits{wallTimeMs,idleMs,cpuMs,memoryMb,pids,outputKb}`
- [ ] **LANG-03**: The core loads all manifests at boot and exposes the available languages; no languages are hardcoded in Go or the API
- [x] **LANG-04**: Per-request `limits` override a manifest's `defaultLimits`
- [x] **LANG-05**: The Python 3.12 package runs `python main.py` with numpy/pandas/requests baked into the image
- [x] **LANG-06**: The Rust package compiles with `rustc -O main.rs -o /tmp/prog` (compile stage with its own limits) then runs the produced binary
- [x] **LANG-07**: The R 4.4 package runs `Rscript main.R` with common libs baked in
- [x] **LANG-08**: The SQLite 3 package runs SQL against an ephemeral in-memory DB, supporting both a `.sql` file and an interactive `sqlite3` shell reading from stdin — validating that "language = image + compile? + run" holds for something that is not a general-purpose language

### Scale & Statelessness

- [x] **SCALE-01**: The API and workers are stateless and run as N replicas
- [x] **SCALE-02**: A worker only claims a job when it has a free sandbox slot; each worker enforces a max concurrent-live-sandbox count derived from CPU/RAM; capacity is counted in live sandboxes, not request bursts
- [x] **SCALE-03**: Queue depth and full-capacity conditions propagate back as backpressure (`429`) rather than silently dropping work
- [x] **SCALE-04**: Worker death mid-session does not leak host containers — a label-based reaper removes orphaned sandboxes and reclaims their slots
- [x] **SCALE-05**: The system is designed for autoscaling by queue depth where the **scaling unit is the worker node** (each launches its sandboxes internally and hosts N concurrent ones), and the worker fleet can scale to zero on an empty queue — not a microVM per execution. The mechanism is documented per deploy target.

### Configuration & Secrets (env-only)

- [x] **CFG-01**: All configuration is via env vars (`EXECUTOR_API_TOKEN`, `REDIS_URL`, `SOKETI_HOST/PORT/USE_TLS/APP_ID/APP_KEY/APP_SECRET`) — there are no configuration endpoints
- [x] **CFG-02**: No endpoint returns secrets, and the soketi secret is never persisted in Redis
- [x] **CFG-03**: soketi credentials are read from env by the worker (to trigger) and by the API (if it signs channel auth)
- [ ] **CFG-04**: The chosen Redis must support native pub/sub and blocking operations for the worker (a managed serverless Redis that lacks TCP `SUBSCRIBE`/`BLPOP`, e.g. Upstash, is only usable for the API, not the worker)

### Channel Authorization (upstream responsibility)

- [ ] **CHAN-01**: The README documents how the upstream app authorizes the browser's private soketi channel using the app key/secret (HMAC)
- [x] **CHAN-02**: (Optional, non-core) The API may offer a channel-auth helper behind `EXECUTOR_API_TOKEN`, clearly marked as optional

### Dev Environment

- [x] **DEV-01**: `docker compose up` brings up the whole stack locally: api + worker (DockerSocketRunner) + redis + soketi + a stub upstream app
- [x] **DEV-02**: The stub upstream app drives an end-to-end interactive execute locally
- [x] **DEV-03**: A script/README walks through a punta-a-punta interactive execute against the local stack

### Abuse Tests (built EARLY)

- [x] **TEST-01**: A fork bomb is contained by `--pids-limit` (the sandbox is killed cleanly)
- [x] **TEST-02**: An OOM program is killed by the memory cap without taking down the worker
- [x] **TEST-03**: An infinite loop is killed by the wall-clock timeout
- [x] **TEST-04**: A program blocked on stdin is killed by the idle timeout
- [x] **TEST-05**: `/stdin/close` (EOF) is delivered and a program reading to EOF terminates correctly
- [x] **TEST-06**: A giant-output program is truncated with `truncated=true` and does not exhaust memory
- [x] **TEST-07**: The abuse suite runs on Linux CI (not only macOS dev) so real cgroup OOM/CPU behavior is exercised
- [x] **TEST-08**: The abuse suite is built early — immediately after the Python end-to-end path — and gates the language fan-out

### Open Source & Documentation

- [ ] **OSS-01**: The repo ships an MIT `LICENSE`
- [ ] **OSS-02**: A `.env.example` documents every env var
- [x] **DOCS-01**: The README has a quickstart: how to run the stack locally
- [ ] **DOCS-02**: The README documents the API contract (`/v1/*` endpoints + wire events)
- [ ] **DOCS-03**: The README documents deployment per target: dev (docker compose); prod (long-lived **worker nodes** on Fly or any Linux host that launch sandboxes internally, scaled to/from zero by queue depth, with **gVisor** `--runtime=runsc` for extra isolation; native-protocol Redis + soketi; API anywhere); and the **v2** `FlyMachinesRunner` microVM-per-execution option with its latency/streaming trade-offs noted
- [ ] **DOCS-04**: The README documents how to add a new language (the package model guide)

## v2 Requirements

Deferred to a future release. Tracked but not in the current roadmap.

### Runtime & Delivery

- **V2-01**: gVisor isolation as the primary hardening upgrade — the same internal runner with `--runtime=runsc` (Docker) or k8s `RuntimeClass=gvisor`; no change to the "worker launches the sandbox internally" model
- **V2-02**: `FlyMachinesRunner` (Firecracker microVM per execution via the Fly Machines API) behind the `Runner` interface, with an interactive-streaming + latency/cost benchmark spike
- **V2-03**: Redis Streams + `XREAD BLOCK` for guaranteed stdin delivery (replacing pub/sub) — also the path if a serverless Redis without TCP pub/sub must be supported
- **V2-04**: Offline crate/CRAN vendoring so Rust/R can use third-party packages without violating `--network=none`
- **V2-05**: `fly-autoscaler` (or equivalent) scaling workers by `LLEN` queue depth, with scale-to-zero where the platform allows

## Out of Scope

Explicitly excluded. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| The upstream consumer app | It's the user's own backend; we ship only a local stub for E2E |
| End-user authentication / complex auth | Only auth is the `EXECUTOR_API_TOKEN` bearer (upstream → API) |
| Soketi channel authorization as a core feature | Upstream app's responsibility via key/secret; optional non-core helper only |
| Any secret-returning endpoint / persisting soketi secret in Redis | Security: config is env-only, secrets never travel over the wire or land in Redis |
| Internet-facing exposure | Internal-only behind the upstream app |
| Runtime dependency resolution in sandboxes | Pre-built images keep `--network=none` always-on |
| Network access from sandboxes | Untrusted code must not reach the network |
| Docker-in-Docker | Worker talks to the host runtime via mounted socket |
| Trusting input from soketi | soketi is output-only |
| Persistent / pausable sandboxes | A live session holds a slot until it expires |
| Upstash (or any non-TCP-pub/sub Redis) for the worker | The worker needs native blocking `SUBSCRIBE`/`BLPOP`; such Redis is API-only (see CFG-04) |

## Traceability

Each v1 requirement maps to exactly one phase.

| Requirement | Phase | Status |
|-------------|-------|--------|
| API-01 | Phase 3 | Complete |
| API-02 | Phase 3 | Complete |
| API-03 | Phase 3 | Complete |
| API-04 | Phase 3 | Complete |
| API-05 | Phase 3 | Complete |
| API-06 | Phase 3 | Complete |
| API-07 | Phase 3 | Complete |
| API-08 | Phase 3 | Complete |
| API-09 | Phase 3 | Complete |
| API-10 | Phase 3 | Complete |
| API-11 | Phase 3 | Complete |
| CONT-01 | Phase 1 | Pending |
| CONT-02 | Phase 1 | Pending |
| CONT-03 | Phase 1 | Pending |
| CONT-04 | Phase 1 | Pending |
| CONT-05 | Phase 1 | Pending |
| CONT-06 | Phase 1 | Pending |
| RUN-01 | Phase 1 | Pending |
| RUN-02 | Phase 2 | Complete |
| RUN-03 | Phase 2 | Complete |
| RUN-04 | Phase 2 | Complete |
| WRK-01 | Phase 3 | Complete |
| WRK-02 | Phase 3 | Complete |
| WRK-03 | Phase 3 | Complete |
| WRK-04 | Phase 3 | Complete |
| SESS-01 | Phase 3 | Complete |
| SESS-02 | Phase 3 | Complete |
| SESS-03 | Phase 3 | Complete |
| STDIN-01 | Phase 3 | Complete |
| STDIN-02 | Phase 3 | Complete |
| STDIN-03 | Phase 3 | Complete |
| STDIN-04 | Phase 1 | Pending |
| HARD-01 | Phase 2 | Complete |
| HARD-02 | Phase 2 | Complete |
| HARD-03 | Phase 2 | Complete |
| HARD-04 | Phase 2 | Complete |
| HARD-05 | Phase 2 | Complete |
| LIM-01 | Phase 2 | Complete |
| LIM-02 | Phase 2 | Complete |
| LIM-03 | Phase 2 | Complete |
| LIM-04 | Phase 2 | Complete |
| OUT-01 | Phase 3 | Complete |
| OUT-02 | Phase 3 | Complete |
| OUT-03 | Phase 3 | Complete |
| OUT-04 | Phase 2 | Complete |
| LIFE-01 | Phase 2 | Complete |
| LIFE-02 | Phase 2 | Complete |
| LANG-01 | Phase 1 | Pending |
| LANG-02 | Phase 1 | Pending |
| LANG-03 | Phase 1 | Pending |
| LANG-04 | Phase 3 | Complete |
| LANG-05 | Phase 3 | Complete |
| LANG-06 | Phase 6 | Complete |
| LANG-07 | Phase 6 | Complete |
| LANG-08 | Phase 6 | Complete |
| SCALE-01 | Phase 5 | Complete |
| SCALE-02 | Phase 5 | Complete |
| SCALE-03 | Phase 5 | Complete |
| SCALE-04 | Phase 5 | Complete |
| SCALE-05 | Phase 5 | Complete |
| CFG-01 | Phase 3 | Complete |
| CFG-02 | Phase 3 | Complete |
| CFG-03 | Phase 3 | Complete |
| CFG-04 | Phase 1 | Pending |
| CHAN-01 | Phase 7 | Pending |
| CHAN-02 | Phase 3 | Complete |
| DEV-01 | Phase 3 | Complete |
| DEV-02 | Phase 3 | Complete |
| DEV-03 | Phase 3 | Complete |
| TEST-01 | Phase 4 | Complete |
| TEST-02 | Phase 4 | Complete |
| TEST-03 | Phase 4 | Complete |
| TEST-04 | Phase 4 | Complete |
| TEST-05 | Phase 4 | Complete |
| TEST-06 | Phase 4 | Complete |
| TEST-07 | Phase 4 | Complete |
| TEST-08 | Phase 4 | Complete |
| OSS-01 | Phase 1 | Pending |
| OSS-02 | Phase 1 | Pending |
| DOCS-01 | Phase 7 | Complete |
| DOCS-02 | Phase 7 | Pending |
| DOCS-03 | Phase 7 | Pending |
| DOCS-04 | Phase 7 | Pending |

**Coverage:**
- v1 requirements: 83 total (the prior "68" header figure was stale from the pre-revision spec; the revised set has 83 REQ-IDs)
- Mapped to phases: 83 ✓
- Unmapped: 0

**Per-phase counts:**
- Phase 1 (Foundation & Wire Contract): 14 — CONT-01..06, LANG-01, LANG-02, LANG-03, RUN-01, STDIN-04, CFG-04, OSS-01, OSS-02
- Phase 2 (Sandbox Hardening & Runner): 15 — RUN-02, RUN-03, RUN-04, HARD-01..05, LIM-01..04, OUT-04, LIFE-01, LIFE-02
- Phase 3 (Interactive Python End-to-End): 33 — API-01..11, WRK-01..04, SESS-01..03, STDIN-01..03, OUT-01..03, LANG-04, LANG-05, CFG-01..03, CHAN-02, DEV-01..03
- Phase 4 (Abuse Suite & Safety Validation): 8 — TEST-01..08
- Phase 5 (Statelessness & Scale): 5 — SCALE-01..05
- Phase 6 (Language Fan-out): 3 — LANG-06, LANG-07, LANG-08
- Phase 7 (OSS Release & Deployment): 5 — DOCS-01..04, CHAN-01

---
*Requirements defined: 2026-06-02*
*Last updated: 2026-06-02 after spec revision (Hono API, polyglot monorepo, shared contract, OSS + deployment targets) — traceability rewritten for the 7-phase revised roadmap; deployment-model refinement (internal-launch worker nodes, scaling unit = node, FlyMachinesRunner → v2)*
