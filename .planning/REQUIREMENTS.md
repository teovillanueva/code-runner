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

- [x] **CONT-01**: A single canonical JSON Schema defines every wire message exchanged between the API and the worker
- [x] **CONT-02**: TypeScript types are generated from the schema and consumed by the Hono API (not hand-written)
- [x] **CONT-03**: Runtime validators (zod) are generated from the schema so API validation stays in lockstep with the contract
- [x] **CONT-04**: Go structs (with JSON tags) are generated from the schema and consumed by the worker
- [x] **CONT-05**: A CI/script drift check regenerates the artifacts and fails on any diff
- [x] **CONT-06**: The contract covers the job spec, stdin chunk, control messages (start/kill/stdin-close), and output events (stage/stdout/stderr/result)

### Worker & Runner (Go)

- [x] **RUN-01**: A `Runner`/`Sandbox` interface abstracts "create hardened sandbox, attach pipes, enforce limits, kill, cleanup" so the backend can swap (Docker → gVisor → Firecracker) without touching core logic
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
- [x] **STDIN-04**: The stdin transport sits behind an interface so Redis pub/sub (MVP) can be swapped for Redis Streams (`XREAD BLOCK`) later without core changes

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

- [x] **LANG-01**: Adding a language is adding a `languages/<lang-version>/` folder with `manifest.json` + `Dockerfile` — no changes to the Go worker or the API
- [x] **LANG-02**: `manifest.json` declares `language, version, aliases, image, entrypoint, compile (nullable), run, interactive, defaultLimits{wallTimeMs,idleMs,cpuMs,memoryMb,pids,outputKb}`
- [x] **LANG-03**: The core loads all manifests at boot and exposes the available languages; no languages are hardcoded in Go or the API
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
- [x] **CFG-04**: The chosen Redis must support native pub/sub and blocking operations for the worker (a managed serverless Redis that lacks TCP `SUBSCRIBE`/`BLPOP`, e.g. Upstash, is only usable for the API, not the worker)

### Channel Authorization (upstream responsibility)

- [x] **CHAN-01**: The README documents how the upstream app authorizes the browser's private soketi channel using the app key/secret (HMAC)
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

- [x] **OSS-01**: The repo ships an MIT `LICENSE`
- [x] **OSS-02**: A `.env.example` documents every env var
- [x] **DOCS-01**: The README has a quickstart: how to run the stack locally
- [x] **DOCS-02**: The README documents the API contract (`/v1/*` endpoints + wire events)
- [x] **DOCS-03**: The README documents deployment per target: dev (docker compose); prod (long-lived **worker nodes** on Fly or any Linux host that launch sandboxes internally, scaled to/from zero by queue depth, with **gVisor** `--runtime=runsc` for extra isolation; native-protocol Redis + soketi; API anywhere); and the **v2** `FlyMachinesRunner` microVM-per-execution option with its latency/streaming trade-offs noted
- [x] **DOCS-04**: The README documents how to add a new language (the package model guide)

### Observability (OpenTelemetry — Phase 8)

- [ ] **OBS-01**: Both the API and the worker initialize an OpenTelemetry SDK configured purely by standard `OTEL_*` env vars; when no OTLP endpoint is set, the SDK is a no-op (zero forced infrastructure, MIT/self-hostable)
- [ ] **OBS-02**: W3C trace context (`traceparent`/`tracestate`) is propagated from the API to the worker across the Redis seam by carrying it in the shared wire contract (not via HTTP headers), so one execution yields one connected distributed trace
- [ ] **OBS-03**: The worker emits phase-level spans (`claim`, `sandbox.create`, `handshake.wait`, `compile`, `run`, `publish.result`) linked to the API's `execute` span; long-lived/interactive output is represented as metrics, never per-chunk spans
- [ ] **OBS-04**: Telemetry is exported via OTLP push (traces + metrics + logs) as the default integration model, targeting an OTel Collector
- [ ] **OBS-05**: An opt-in Prometheus `/metrics` scrape endpoint is exposed on a separate admin port/surface (not behind the public bearer-auth gateway), for self-hosters who prefer pull-based metrics
- [ ] **OBS-06**: Domain metrics are emitted: queue depth, sandbox slots used/max, time-in-queue, terminal-state counts (incl. `timedOut`/`idleTimedOut`/`cpuExceeded`), sandbox create/kill latency, warmup reclaims, reaper orphans, admission/ratelimit rejections, soketi publish latency/errors
- [ ] **OBS-07**: Both services emit structured JSON logs with `trace_id`/`span_id`/`job_id` correlation fields; the API is migrated off `console.log` to a structured logger matching the worker's `slog`
- [ ] **OBS-08**: Sampling is configurable (`parentbased_traceidratio`); a commented example OTel Collector service is provided in `docker-compose.yml` and every new `OTEL_*` var is documented in `.env.example`

## v1.1 Requirements — Density / ZygoteRunner

Make the `ZygoteRunner` production-ready on main. Tiered coverage (Python + R on zygote for ~2.7× density; Rust + SQLite stay on Docker), Fly-only privileged pools. Builds on validated spikes 005/006 and `.planning/decisions/FAST-FOLLOW-zygote-runner.md`.

### ZygoteRunner Core (Go)

- [ ] **ZYG-01**: `ZygoteRunner` implements `Runner.Create`, returning a hardened forked-child `Sandbox` from a warm per-language parent
- [ ] **ZYG-02**: `zygoteSandbox` implements every `Sandbox` method (Stdin/Stdout/Stderr/Wait/Kill/Cleanup/Compile) with semantics identical to `dockerSandbox`
- [ ] **ZYG-03**: `Kill` terminates the entire child process tree (the child's PID-ns init), not just one PID
- [ ] **ZYG-04**: `Cleanup` is idempotent (`sync.Once`) and leaks no pipe, fd, cgroup leaf, parent, or slot on any exit path (normal, error, panic)
- [ ] **ZYG-05**: `CPUReader` reads the child's cgroup-v2 `cpu.stat` so the session CPU clock works for zygote children
- [ ] **ZYG-06**: the wall / idle / CPU clocks govern zygote children exactly as they do docker sandboxes

### Per-Child Hardening

- [ ] **ZHARD-01**: each child runs under a distinct UID
- [ ] **ZHARD-02**: each child gets its own PID namespace (double-fork) and sees only itself in `/proc`
- [ ] **ZHARD-03**: `no_new_privs` is set; a child cannot gain privileges
- [ ] **ZHARD-04**: each child gets a private `/tmp` tmpfs and cannot read a sibling's `/tmp`
- [ ] **ZHARD-05**: each child is placed in its own cgroup-v2 sub-cgroup with `memory.max` + `pids.max`
- [ ] **ZHARD-06**: each child scrubs inherited fds > 2 before executing user code (defense in depth)

### Per-Language Zygote Agents

- [ ] **AGENT-01**: a Python zygote agent pre-imports the manifest set, listens for spawn requests, and forks hardened children running user code
- [ ] **AGENT-02**: an R zygote agent provides the same behavior for R
- [ ] **AGENT-03**: agents are credential-free — they hold no Redis/soketi/queue FDs and talk to the worker over a minimal pipe/socket only
- [ ] **AGENT-04**: a spawn request carries the user's files + run argv + limits; the child runs them with stdin/stdout/stderr wired back to the worker

### Warm Parent Pool

- [ ] **POOL-01**: the worker maintains a warm parent pool, one parent per `(language, version)`
- [ ] **POOL-02**: parents are pre-warmed so `Create` is fork-fast (no cold image start per job)
- [ ] **POOL-03**: idle parents are reaped after a configurable idle window to reclaim RAM
- [ ] **POOL-04**: a dead/crashed parent is detected and respawned; in-flight jobs fail cleanly without leaking slots

### Tiered Routing

- [ ] **TIER-01**: a `TieredRunner` selects `ZygoteRunner` for zygote-opted manifests and `DockerSocketRunner` otherwise
- [ ] **TIER-02**: routing is manifest-driven — no language-name branching in worker logic
- [ ] **TIER-03**: all four languages (Python, R, Rust, SQLite) run end-to-end through the `TieredRunner`
- [ ] **TIER-04**: `DockerSocketRunner` remains the fallback when zygote is disabled or unavailable

### Pre-Import Contract

- [ ] **PRE-01**: a `preimport` field is added to the manifest JSON schema (single source of truth); the contract is regenerated (TS + zod + Go) and the drift gate passes
- [ ] **PRE-02**: the Python 3.12 manifest declares its `preimport` set
- [ ] **PRE-03**: the R 4.4 manifest declares its `preimport` set
- [ ] **PRE-04**: a manifest without `preimport` (Rust, SQLite) remains valid and routes to Docker

### Zygote Safety Tests

- [ ] **ZTEST-01**: the abuse suite (fork bomb, OOM, infinite loop, idle, EOF, giant output) passes for the zygote path with Phase 4 parity
- [ ] **ZTEST-02**: isolation tests prove a child cannot read a sibling's memory, `/proc`, `/tmp`, or inherited FDs
- [ ] **ZTEST-03**: density is verified — Python reaches materially higher concurrency than Docker on the same node
- [ ] **ZTEST-04**: no slot/parent/child leaks across many sequential and concurrent jobs

### Deploy & Gating

- [ ] **ZDEP-01**: `ZygoteRunner` is gated to the Fly/production runtime; dev + CI use `DockerSocketRunner` (config switch)
- [ ] **ZDEP-02**: the Fly deploy config grants the pool the required privilege (`CAP_SYS_ADMIN`, `CAP_SETUID`, host cgroups)
- [ ] **ZDEP-03**: enabling/disabling zygote is a config flag with a safe default (off → Docker)

### Pool Observability

- [ ] **ZOBS-01**: pool metrics are emitted via the existing OpenTelemetry instrumentation — per-language parent occupancy, warm/idle parent counts, fork (spawn) latency, parent reap/respawn counts
- [ ] **ZOBS-02**: each terminal/error path on the zygote runner increments the same domain counters as the docker path (terminal-state, kill latency) so dashboards stay runner-agnostic

## v1.2 Requirements — Input Files & Content-Addressed Blobs

Let callers ship arbitrary input files (text + binary, in subdirectories) alongside the code, and dedupe large/shared files across runs via a content-addressed (sha256) blob store — without breaking the thin-gateway or host-escape-only security posture. Two layers: **Phase 15** (inline multi-file, zero new infra) and **Phase 16** (CAS blob store). Design locked in PROJECT.md "Current Milestone: v1.2".

### Multi-file Input — Inline (Phase 15)

- [x] **FILES-01**: A caller can submit multiple input files in a single `/v1/execute` request and they all materialize in the sandbox workspace before the run starts
- [x] **FILES-02**: `FileInput` gains an `encoding` field (`utf8` default | `base64`); `base64` lets binary files (xlsx, parquet, images, zip) be sent inline, and omitting the field is fully backward-compatible with existing text callers
- [x] **FILES-03**: A caller can place a file in a subdirectory via a relative path in `name` (e.g. `data/input.csv`); the worker creates the parent directories under `/workspace`
- [x] **FILES-04**: The worker sanitizes every file path so it cannot escape `/workspace` (rejects absolute paths; collapses `..` traversal), enforced in the worker regardless of any API-side validation
- [x] **FILES-05**: Captured artifacts (`collectOutput`) exclude every input file by its full relative path, not just its basename, so a subdir input is never echoed back as an artifact
- [x] **FILES-06**: The API rejects an over-large request (sum of decoded input bytes) with HTTP 413, governed by a configurable `MAX_FILES_BYTES`
- [x] **FILES-07**: The API rejects invalid base64 content and escaping/absolute paths with HTTP 400 before the job is enqueued
- [x] **FILES-08**: The Node SDK accepts both text and binary (`Buffer`) input files and sets the correct `encoding` transparently

### Content-Addressed Blob Store — CAS (Phase 16)

- [x] **BLOB-01**: A `Blob` store interface backs large/shared input files, with an S3-compatible implementation (reusing the existing artifact-store plumbing where it fits); minio ships in docker-compose under an inert profile so `docker compose up` stays a no-op _(16-01)_
- [x] **BLOB-02**: `POST /v1/blobs/check` accepts a list of sha256 hashes and returns which are missing, each with a presigned PUT URL pointing at code-runner's own store _(16-02)_
- [x] **BLOB-03**: Uploaded blob bytes travel client→store directly via the presigned URL and never pass through the Hono gateway (keeps the gateway thin) _(16-02)_
- [x] **BLOB-04**: sha256 is verified before a referenced blob is used — verification is authoritative at the **worker on pull** (locked architecture, 16-CONTEXT moved it off finalize); `/v1/blobs/finalize` records liveness only. A mismatch fails the job cleanly with no run _(16-01 worker verify + 16-02 finalize)_
- [x] **BLOB-05**: `FileInput` supports a `ref` variant (`{ name, ref: "sha256:…" }`) referencing an already-uploaded blob, usable alongside inline files in the same request _(16-01 contract + 16-02 API XOR)_
- [x] **BLOB-06**: The worker streams a referenced blob from the store into the sandbox workspace without buffering the whole file in worker RAM, and re-verifies its sha256 before the run uses it _(16-01)_
- [x] **BLOB-07**: Blob liveness is tracked in Redis as an idle TTL that is bumped on use (touch-on-use) and only ever extended (monotonic), so a frequently-referenced blob never expires mid-use _(16-01 worker + 16-02 API touch)_
- [x] **BLOB-08**: A run leases/pins every blob it references for its duration so GC never deletes an in-use blob; GC applies a grace window before reclaiming an expired blob _(16-01)_
- [x] **BLOB-09**: The worker pulls blobs ONLY from code-runner's own store at a known host — never from an arbitrary consumer-supplied URL — eliminating the SSRF surface _(16-01)_
- [x] **BLOB-10**: The Node SDK exposes `client.blobs.upload(buffer, { ttlSeconds })` that hashes the buffer, runs the existence check, and uploads only the missing bytes _(16-02)_
- [x] **BLOB-11**: The Node SDK `execute()` transparently routes each file inline-vs-CAS by a size threshold so callers don't have to manage blobs manually _(16-02)_
- [x] **BLOB-12**: Operators can point code-runner at their own S3 bucket (BYO-bucket via env) while code-runner still owns the CAS key layout + the Redis liveness index _(16-01 config + 16-02 infra)_

## v2 Requirements

Deferred to a future release. Tracked but not in the current roadmap.

### Runtime & Delivery

- **V2-01**: gVisor isolation as the primary hardening upgrade — the same internal runner with `--runtime=runsc` (Docker) or k8s `RuntimeClass=gvisor`; no change to the "worker launches the sandbox internally" model
- **V2-02**: `FlyMachinesRunner` (Firecracker microVM per execution via the Fly Machines API) behind the `Runner` interface, with an interactive-streaming + latency/cost benchmark spike
- **V2-03**: Redis Streams + `XREAD BLOCK` for guaranteed stdin delivery (replacing pub/sub) — also the path if a serverless Redis without TCP pub/sub must be supported
- **V2-04**: Offline crate/CRAN vendoring so Rust/R can use third-party packages without violating `--network=none`
- **V2-05**: `fly-autoscaler` (or equivalent) scaling workers by `LLEN` queue depth, with scale-to-zero where the platform allows

### Density (deferred from v1.1)

- **V2-06**: a density regression gate in CI — an automated test that fails if Python sandbox density drops below a threshold (requires a Linux CI runner with cgroup-v2 access)
- **V2-07**: language-affinity autoscaling — route jobs to workers already warm for that language and emit per-language scale metrics so the autoscaler pays one parent base per node, not N (ties to SCALE / Phase 5 D5)

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
| CONT-01 | Phase 1 | Complete |
| CONT-02 | Phase 1 | Complete |
| CONT-03 | Phase 1 | Complete |
| CONT-04 | Phase 1 | Complete |
| CONT-05 | Phase 1 | Complete |
| CONT-06 | Phase 1 | Complete |
| RUN-01 | Phase 1 | Complete |
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
| STDIN-04 | Phase 1 | Complete |
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
| LANG-01 | Phase 1 | Complete |
| LANG-02 | Phase 1 | Complete |
| LANG-03 | Phase 1 | Complete |
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
| CFG-04 | Phase 1 | Complete |
| CHAN-01 | Phase 7 | Complete |
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
| OSS-01 | Phase 1 | Complete |
| OSS-02 | Phase 1 | Complete |
| DOCS-01 | Phase 7 | Complete |
| DOCS-02 | Phase 7 | Complete |
| DOCS-03 | Phase 7 | Complete |
| DOCS-04 | Phase 7 | Complete |
| OBS-01 | Phase 8 | Planned |
| OBS-02 | Phase 8 | Planned |
| OBS-03 | Phase 8 | Planned |
| OBS-04 | Phase 8 | Planned |
| OBS-05 | Phase 8 | Planned |
| OBS-06 | Phase 8 | Planned |
| OBS-07 | Phase 8 | Planned |
| OBS-08 | Phase 8 | Planned |

### v1.1 — Density / ZygoteRunner

Each v1.1 requirement maps to exactly one phase. v1.0 rows above are unchanged (Complete/Planned).

| Requirement | Phase | Status |
|-------------|-------|--------|
| PRE-01 | Phase 10 | Planned |
| PRE-02 | Phase 10 | Planned |
| PRE-03 | Phase 10 | Planned |
| PRE-04 | Phase 10 | Planned |
| AGENT-01 | Phase 11 | Planned |
| AGENT-02 | Phase 11 | Planned |
| AGENT-03 | Phase 11 | Planned |
| AGENT-04 | Phase 11 | Planned |
| ZHARD-01 | Phase 11 | Planned |
| ZHARD-02 | Phase 11 | Planned |
| ZHARD-03 | Phase 11 | Planned |
| ZHARD-04 | Phase 11 | Planned |
| ZHARD-05 | Phase 11 | Planned |
| ZHARD-06 | Phase 11 | Planned |
| ZYG-01 | Phase 12 | Planned |
| ZYG-02 | Phase 12 | Planned |
| ZYG-03 | Phase 12 | Planned |
| ZYG-04 | Phase 12 | Planned |
| ZYG-05 | Phase 12 | Planned |
| ZYG-06 | Phase 12 | Planned |
| POOL-01 | Phase 12 | Planned |
| POOL-02 | Phase 12 | Planned |
| POOL-03 | Phase 12 | Planned |
| POOL-04 | Phase 12 | Planned |
| TIER-01 | Phase 13 | Planned |
| TIER-02 | Phase 13 | Planned |
| TIER-03 | Phase 13 | Planned |
| TIER-04 | Phase 13 | Planned |
| ZDEP-01 | Phase 13 | Planned |
| ZDEP-02 | Phase 13 | Planned |
| ZDEP-03 | Phase 13 | Planned |
| ZTEST-01 | Phase 14 | Planned |
| ZTEST-02 | Phase 14 | Planned |
| ZTEST-03 | Phase 14 | Planned |
| ZTEST-04 | Phase 14 | Planned |
| ZOBS-01 | Phase 14 | Planned |
| ZOBS-02 | Phase 14 | Planned |

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
- Phase 8 (Distributed Observability): 8 — OBS-01..08

**v1.1 coverage:**
- v1.1 requirements: 37 total (ZYG x6, ZHARD x6, AGENT x4, POOL x4, TIER x4, PRE x4, ZTEST x4, ZDEP x3, ZOBS x2)
- Mapped to phases: 37 ✓
- Unmapped: 0
- Deferred (not mapped): V2-06 (density regression CI gate), V2-07 (language-affinity autoscaling)

**v1.1 per-phase counts:**
- Phase 10 (Pre-Import Contract): 4 — PRE-01..04
- Phase 11 (Zygote Agents & Per-Child Hardening): 10 — AGENT-01..04, ZHARD-01..06
- Phase 12 (Go ZygoteRunner & Warm Pool): 10 — ZYG-01..06, POOL-01..04
- Phase 13 (Tiered Routing, Deploy & Gating): 7 — TIER-01..04, ZDEP-01..03
- Phase 14 (Zygote Safety, Density & Pool Observability): 6 — ZTEST-01..04, ZOBS-01..02

### v1.2 — Input Files & Content-Addressed Blobs

Each v1.2 requirement maps to exactly one phase. v1.0 and v1.1 rows above are unchanged.

| Requirement | Phase | Status |
|-------------|-------|--------|
| FILES-01 | Phase 15 | Planned |
| FILES-02 | Phase 15 | Planned |
| FILES-03 | Phase 15 | Planned |
| FILES-04 | Phase 15 | Planned |
| FILES-05 | Phase 15 | Planned |
| FILES-06 | Phase 15 | Planned |
| FILES-07 | Phase 15 | Planned |
| FILES-08 | Phase 15 | Planned |
| BLOB-01 | Phase 16 | Done |
| BLOB-02 | Phase 16 | Done |
| BLOB-03 | Phase 16 | Done |
| BLOB-04 | Phase 16 | Done |
| BLOB-05 | Phase 16 | Done |
| BLOB-06 | Phase 16 | Done |
| BLOB-07 | Phase 16 | Done |
| BLOB-08 | Phase 16 | Done |
| BLOB-09 | Phase 16 | Done |
| BLOB-10 | Phase 16 | Done |
| BLOB-11 | Phase 16 | Done |
| BLOB-12 | Phase 16 | Done |

**v1.2 coverage:**
- v1.2 requirements: 20 total (FILES x8, BLOB x12)
- Mapped to phases: 20 ✓
- Unmapped: 0

**v1.2 per-phase counts:**
- Phase 15 (Multi-file Input (inline)): 8 — FILES-01..08
- Phase 16 (Content-Addressed Blob Store (CAS)): 12 — BLOB-01..12

---
*Requirements defined: 2026-06-02*
*Last updated: 2026-06-09 — added v1.2 (Input Files & Content-Addressed Blobs) traceability: 20 requirements mapped to Phases 15–16 (FILES-01..08 → Phase 15, BLOB-01..12 → Phase 16); v1.0 + v1.1 rows unchanged.*
