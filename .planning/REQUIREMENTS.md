# Requirements: code-runner

**Defined:** 2026-06-02
**Core Value:** Run untrusted code in a hardened, resource-bounded sandbox with a live interactive stdin session and reliable real-time output — without ever leaking a container, a subscription, or a session slot.

## v1 Requirements

Requirements for the initial release. Each maps to roadmap phases.

### Executor API (internal contract)

- [ ] **API-01**: TS API can submit a job via `POST /execute` with `{jobId, channel, language, version, files[], limits?}` and receive `202 {jobId, status:"queued"}` before the process starts
- [ ] **API-02**: TS API can start a queued job's process via `POST /run/:jobId/start` (only after the client has subscribed)
- [ ] **API-03**: TS API can write to a running process's stdin via `POST /run/:jobId/stdin {chunk}`
- [ ] **API-04**: TS API can signal EOF on stdin via `POST /run/:jobId/stdin/close`
- [ ] **API-05**: TS API can terminate a session via `POST /run/:jobId/kill`
- [ ] **API-06**: API rejects requests for unknown `jobId`, unknown `language`/`version`, or malformed payloads with clear error responses
- [ ] **API-07**: API enforces a stdin rate limit per job and returns `429` when the pending-stdin byte cap is exceeded (backpressure)
- [ ] **API-08**: Executor API trusts only requests on the private network (shared-secret/bearer between TS API and Executor, configurable)

### Job Queue (Redis)

- [ ] **QUEUE-01**: Submitted jobs are enqueued in Redis and consumed by workers, decoupling reception from execution
- [ ] **QUEUE-02**: A worker only claims a job when it has a free sandbox slot (capacity-aware backpressure)
- [ ] **QUEUE-03**: Queue depth and full-capacity conditions propagate back as `429`/backpressure rather than silently dropping work

### Interactive Session & Handshake

- [ ] **SESS-01**: A job follows the start-handshake: `/execute` creates `jobId`+`channel` and returns before the process starts; the process only starts on `/run/:jobId/start` (no early prompt is lost)
- [ ] **SESS-02**: The sandbox keeps the process alive with stdin/stdout/stderr pipes open, awaiting interactive input (not batch-ephemeral)
- [ ] **SESS-03**: Batch (no-stdin) execution works as the degenerate case of the interactive model
- [ ] **SESS-04**: A warm-up timeout reclaims the slot if `/start` never arrives after `/execute`

### stdin Routing

- [ ] **STDIN-01**: stdin is routed without service discovery: Executor `PUBLISH`es to `stdin:<jobId>` and the owning worker (subscribed only to its live jobs) writes it to the process pipe
- [ ] **STDIN-02**: stdin/close publishes a control signal that closes the process's stdin (EOF) exactly once
- [ ] **STDIN-03**: Control messages (start/kill) route to the owning worker via a per-job control channel
- [ ] **STDIN-04**: stdin transport sits behind an interface so Redis pub/sub (MVP) can be swapped for Redis Streams later without core changes

### Runner / Sandbox

- [ ] **RUN-01**: A `Runner`/`Sandbox` interface abstracts "create hardened sandbox, attach pipes, enforce limits, kill, cleanup" so the runtime can change without touching core logic
- [ ] **RUN-02**: The Docker-hardened runner launches an ephemeral container per execution by talking to the host container runtime via a mounted socket (no Docker-in-Docker)
- [ ] **RUN-03**: The runner attaches and demuxes stdout/stderr separately and forwards them to the session
- [ ] **RUN-04**: `kill` destroys the whole container (process tree), never just a PID

### Sandbox Hardening

- [ ] **HARD-01**: Every sandbox runs with `--network=none`
- [ ] **HARD-02**: Every sandbox runs `--read-only` with a size-capped tmpfs `/tmp` workspace
- [ ] **HARD-03**: Memory is capped with `--memory` == `--memory-swap` (no swap)
- [ ] **HARD-04**: Every sandbox runs with `--pids-limit` and `--cpus` set
- [ ] **HARD-05**: Every sandbox runs with `--cap-drop=ALL`, `--security-opt=no-new-privileges`, and a restrictive seccomp profile

### Resource Limits (three clocks + caps)

- [ ] **LIM-01**: A wall-clock timeout kills the entire session unconditionally when exceeded
- [ ] **LIM-02**: An idle timeout kills the sandbox when no stdout and no stdin is received within the window
- [ ] **LIM-03**: A CPU (cgroup) limit kills the sandbox on accumulated real compute, defeating use of interactive mode to smuggle heavy work past the wall clock
- [ ] **LIM-04**: stdout/stderr bytes are capped — output is truncated and `truncated=true` is reported
- [ ] **LIM-05**: Pending-stdin bytes are capped to apply backpressure (surfaced as `429` at the API)

### Real-time Output (soketi)

- [ ] **OUT-01**: The worker publishes `stage {phase: queued|compiling|running}` events to soketi on `private-run-<jobId>`
- [ ] **OUT-02**: The worker streams `stdout {chunk}` and `stderr {chunk}` events to soketi
- [ ] **OUT-03**: The worker publishes a terminal `result {exitCode, signal, timedOut, idleTimedOut, truncated, durationMs}` event
- [ ] **OUT-04**: Output is published via the Pusher HTTP API, batched and chunked to stay within soketi event-size limits

### Lifecycle / Cleanup

- [ ] **LIFE-01**: On any terminal event (any timeout, result, or kill) the worker unsubscribes `stdin:<jobId>`, closes pipes, removes the sandbox, and frees the slot
- [ ] **LIFE-02**: Cleanup is idempotent and runs exactly once across all terminal paths (no double-cleanup, no leaked containers/subscriptions/slots)

### Language Packages (manifest-driven)

- [ ] **LANG-01**: Adding a language is adding a `languages/<lang-version>/` folder with `manifest.json` + `Dockerfile` — no Go core changes
- [ ] **LANG-02**: `manifest.json` declares `language, version, aliases, image, entrypoint, compile (nullable), run, defaultLimits{wallTimeMs,idleMs,cpuMs,memoryMb,pids,outputKb}, interactive`
- [ ] **LANG-03**: The core loads all manifests at boot and exposes the available languages; no languages are hardcoded in Go
- [ ] **LANG-04**: Per-request `limits` override a manifest's `defaultLimits`
- [ ] **LANG-05**: Python 3.12 package runs `python main.py` with numpy/pandas/requests baked into the image
- [ ] **LANG-06**: Rust package compiles with `rustc -O` (compile stage with its own limits) then runs the produced binary
- [ ] **LANG-07**: R 4.4 package runs `Rscript main.R` with common libs baked in
- [ ] **LANG-08**: SQLite 3 package runs SQL against an ephemeral in-memory DB, supporting both a `.sql` file and an interactive `sqlite3` shell reading from stdin

### Scale & Statelessness

- [ ] **SCALE-01**: Executor API and workers are stateless and run as N replicas
- [ ] **SCALE-02**: Each worker enforces a max number of concurrent live sandboxes based on CPU/RAM; capacity is counted in live sandboxes, not request bursts
- [ ] **SCALE-03**: Worker death mid-session does not leak host containers — a reaper cleans up orphaned labeled sandboxes

### Dev Environment

- [ ] **DEV-01**: `docker compose up` brings up the whole stack locally: executor, worker(s), redis, soketi, and a TS API stub
- [ ] **DEV-02**: A TS API stub/mock implements enough of the public API's contract to drive an end-to-end interactive execute locally
- [ ] **DEV-03**: A script/README walks through a punta-a-punta interactive execute against the local stack

### Abuse Tests

- [ ] **TEST-01**: A fork bomb is contained by `--pids-limit` (test proves the sandbox survives/kills cleanly)
- [ ] **TEST-02**: An OOM program is killed by the memory cap without taking down the worker
- [ ] **TEST-03**: An infinite loop is killed by the wall-clock timeout
- [ ] **TEST-04**: A program blocked on stdin is killed by the idle timeout
- [ ] **TEST-05**: stdin/close (EOF) is delivered and the program reading to EOF terminates correctly
- [ ] **TEST-06**: A giant-output program is truncated with `truncated=true` and does not exhaust memory
- [ ] **TEST-07**: Abuse tests run on Linux CI (not only macOS dev) to exercise real cgroup behavior

### Documentation

- [ ] **DOCS-01**: README documents how to run the stack locally
- [ ] **DOCS-02**: README documents the Executor internal API contract
- [ ] **DOCS-03**: README documents how to add a new language (the package model guide)

## v2 Requirements

Deferred to a future release. Tracked but not in the current roadmap.

### Runtime & Delivery

- **V2-01**: gVisor (`runsc`) runner implementation behind the same `Runner` interface
- **V2-02**: Redis Streams + `XREAD BLOCK` for guaranteed stdin delivery (replacing pub/sub)
- **V2-03**: PTY allocation option for languages where pipe unbuffering proves unmanageable
- **V2-04**: Offline crate/CRAN vendoring so Rust/R can use third-party packages without violating `network=none`

## Out of Scope

Explicitly excluded. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| Public TypeScript API | Already exists; we only respect its contract + ship a local stub |
| End-user authentication | Done by the TS API |
| Pusher channel authorization | Done by the TS API |
| Internet-facing exposure | Service is internal-only behind the private network |
| Runtime dependency resolution in sandboxes | Piston model — images are pre-built with libs baked in |
| Network access from sandboxes | Untrusted code must not reach the network (`--network=none`) |
| Docker-in-Docker | Workers talk to the host runtime via mounted socket |
| Trusting input from soketi | soketi is output-only; nothing trusted enters via the realtime channel |
| Persistent / pausable sandboxes | A live session holds a slot until it expires; no long-lived persistence |

## Traceability

Populated during roadmap creation — each v1 requirement maps to exactly one phase.

| Requirement | Phase | Status |
|-------------|-------|--------|
| (all v1 REQ-IDs) | TBD | Pending |

**Coverage:**
- v1 requirements: 51 total
- Mapped to phases: 0 (roadmap pending)
- Unmapped: 51 ⚠️ (resolved by roadmapper)

---
*Requirements defined: 2026-06-02*
*Last updated: 2026-06-02 after initial definition*
