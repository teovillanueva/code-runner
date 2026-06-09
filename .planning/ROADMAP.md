# Roadmap: code-runner

## Overview

code-runner is an open-source, self-hostable, Piston-style remote code execution service built as a polyglot monorepo: a thin Hono/TypeScript API gateway, a Go worker that orchestrates hardened sandboxes and keeps live interactive sessions alive, manifest-driven language packages, and a shared wire contract that is the fragile seam between the two languages. The journey is deliberately bottom-up: first lock the monorepo layout, the JSON-Schema wire contract (TS types + zod validators + Go structs + CI drift check), the manifest schema/loader, and the `Runner` interface skeleton â and **stop for human approval before any implementation**. Then build the highest-risk core (the Go runner with Docker hardening, three clocks, tree-kill, and idempotent cleanup), drive one language (Python) end-to-end through the whole interactive path (Hono API â Redis â worker session â soketi), and immediately build the abuse test suite that gates everything after it. Only once safety is proven do we fan out to Rust, R, and SQLite, harden statelessness/scale, and ship the OSS release with deployment-per-target docs.

## Phases

**Phase Numbering:**

- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [x] **Phase 1: Foundation & Wire Contract** - Monorepo layout, JSON-Schema wire contract codegen + drift check, manifest schema/loader, `Runner` interface, OSS scaffolding â ends at a human approval gate before coding
- [x] **Phase 2: Sandbox Hardening & Runner** - Go `DockerSocketRunner`: full hardening flags, three clocks, tree-kill, output caps, idempotent `sync.Once` cleanup (completed 2026-06-02)
- [x] **Phase 3: Interactive Python End-to-End** - Hono API (all `/v1/*`), Redis queue + stdin/control routing, worker session, soketi publisher, Python package, docker compose dev stack â full interactive demo works (completed 2026-06-02)
- [x] **Phase 4: Abuse Suite & Safety Validation** - Fork bomb, OOM, infinite loop, idle, EOF, giant output on Linux CI â gates the language fan-out (completed 2026-06-02)
- [x] **Phase 5: Statelessness & Scale** - Slot capacity, backpressure 429, reliable claim, dead-worker reaper, N replicas, autoscaling-by-queue-depth + scale-to-zero design, native-Redis worker constraint (completed 2026-06-02)
- [x] **Phase 6: Language Fan-out** - Rust (compile stage), R 4.4, SQLite 3 (interactive SQL shell) added as manifest + Dockerfile, zero core changes (completed 2026-06-03)
- [x] **Phase 7: OSS Release & Deployment** - README quickstart, API contract, add-a-language guide, deployment-per-target, channel-auth documentation (completed 2026-06-03)
- [x] **Phase 8: Distributed Observability** - OpenTelemetry across API + worker (env-gated, no-op when unset), W3C trace-context propagation across the Redis seam via the wire contract, phase-level spans, domain metrics via OTLP push + opt-in Prometheus `/metrics`, unified structured logs with trace correlation, example OTel Collector (completed 2026-06-03)
- [x] **Phase 9: Artifacts & Pullable Run Output** - Capture sandbox-generated artifacts (matplotlib/R plots, files) by workspace diff into env-configured S3-compatible object storage, persist a pullable `RunResult` in Redis, and expose `GET /v1/jobs/:id/output` so the API can pull full output server-side (no soketi); URL-only contract `Artifact`/`RunResult`, worker workspace-diff capture + Redis persistence, Python/R plot parity (no shims), `ArtifactStore`/`S3Store` seam, SDK helpers â enables the edalef migration. Defers blocking `/v1/run` + webhooks (completed 2026-06-03)

## Milestone v1.1 — Density / ZygoteRunner

- [ ] **Phase 10: Pre-Import Contract** - Add a `preimport` manifest field to the JSON-Schema single source of truth, regenerate the contract (TS + zod + Go) with the drift gate green, and declare Python/R pre-import sets while Rust/SQLite stay valid and Docker-routed
- [ ] **Phase 11: Zygote Agents & Per-Child Hardening** - Per-language credential-free zygote agents (Python, R) that pre-import the manifest set, accept spawn requests over a minimal pipe, and double-fork hardened children (distinct UID, PID-ns, `no_new_privs`, private `/tmp`, per-child cgroup-v2, fd scrub) with stdio wired back
- [ ] **Phase 12: Go ZygoteRunner & Warm Pool** - A Go `ZygoteRunner` satisfying the existing `Runner`/`Sandbox` interface exactly as `DockerSocketRunner` does (Create/Stdin/Stdout/Stderr/Wait/Kill/Cleanup/Compile, cgroup CPUReader, `sync.Once` cleanup, full-tree kill, three clocks), fed by a warm per-language parent pool with idle reaping and crash respawn
- [ ] **Phase 13: Tiered Routing, Deploy & Gating** - A manifest-driven `TieredRunner` routing Python/R to zygote and Rust/SQLite to Docker, all four languages working end-to-end, Fly deploy config granting the privileged pool its caps, and a safe-default config flag gating zygote to Fly/prod (off -> Docker in dev + CI)
- [ ] **Phase 14: Zygote Safety, Density & Pool Observability** - The Phase-4-parity abuse suite + sibling-isolation + density + no-leak tests passing on the zygote path (the milestone gate), plus pool metrics emitted through the existing OpenTelemetry instrumentation so dashboards stay runner-agnostic

## Milestone v1.2 — Input Files & Content-Addressed Blobs

- [x] **Phase 15: Multi-file Input (inline)** - Multiple input files per `/v1/execute` (text + binary via `FileInput.encoding` base64, subdir paths under `/workspace`), worker-side path sanitization (host-escape-only), `MAX_FILES_BYTES` 413 cap, base64/path 400 validation, artifact-exclude by full relative path, Node SDK Buffer/text passthrough — zero new infra, independently shippable (completed 2026-06-09)
- [x] **Phase 16: Content-Addressed Blob Store (CAS)** - A `Blob` interface over an S3-compatible store (reusing Phase-9 artifact-store plumbing), `POST /v1/blobs/check` issuing presigned PUTs to OUR store (bytes go client->store, never through the gateway), sha256 verify on finalize + worker pull, a `FileInput.ref` variant, Redis monotonic idle-TTL + per-run lease + grace-window GC, worker streaming blob->tar->sandbox, Node SDK `blobs.upload` + transparent inline-vs-CAS routing, BYO-bucket via env, minio inert in compose

## Phase Details

### Phase 1: Foundation & Wire Contract

**Goal**: Establish the polyglot monorepo, the single-source-of-truth wire contract, the manifest model, and the swap seams â then **stop and get human approval on the layout + contract + manifest schema before any implementation proceeds**.
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

- [x] 01-01-PLAN.md â Go internal/keys (contract mirror) + internal/manifest loader/validator/resolver + sample python manifest; verify committed contract & OSS scaffolding
- [x] 01-02-PLAN.md â Runner/Sandbox interface + StdinTransport interface (stubs) and native-Redis-for-worker constraint (config + docs)
- [x] 01-03-PLAN.md â Worker boot entrypoint (loads manifests, lists languages, wires stubs) + shared TS manifest loader so the API is non-hardcoded

### Phase 2: Sandbox Hardening & Runner

**Goal**: Build the highest-risk component â a hardened Go `DockerSocketRunner` that creates an ephemeral container per execution, enforces the three clocks and resource caps, tree-kills, and tears down idempotently â so safety is baked in, never retrofitted.
**Mode:** mvp
**Depends on**: Phase 1
**Requirements**: RUN-02, RUN-03, RUN-04, HARD-01, HARD-02, HARD-03, HARD-04, HARD-05, LIM-01, LIM-02, LIM-03, LIM-04, OUT-04, LIFE-01, LIFE-02
**Success Criteria** (what must be TRUE):

  1. `DockerSocketRunner` launches an ephemeral container per execution via the mounted host socket (no Docker-in-Docker) and demuxes stdout/stderr separately to the session.
  2. Every sandbox runs fully hardened: `--network=none`, `--read-only` + size-capped tmpfs `/tmp`, `--memory==--memory-swap`, `--pids-limit`, `--cpus`, `--cap-drop=ALL`, `--security-opt=no-new-privileges`, restrictive seccomp, non-root user.
  3. Three independent clocks each kill the sandbox: wall-clock (unconditional), idle (no stdout/stdin in the window), and CPU/cgroup (accumulated compute), so compute hidden behind stdin reads is still caught.
  4. `kill` destroys the whole container (process tree), never a single PID, and the worker keeps draining stdout/stderr while truncating at the byte cap so the process never blocks (`truncated=true` reported).
  5. The worker triggers soketi directly via the Pusher protocol using env credentials, batched/chunked under soketi's event-size limit.
  6. A single idempotent `sync.Once` teardown runs exactly once across every terminal path (wall/idle/CPU/kill/exit/output-cap), unsubscribing, closing pipes, removing the container, and freeing the slot â no leaked containers, subscriptions, or slots.

**Plans**: 4 plans
Plans:

- [x] 02-01-PLAN.md â Phase 2 deps + restrictive seccomp profile + testable safety logic (three clocks, output pump, sync.Once teardown) in internal/session
- [x] 02-02-PLAN.md â soketi/Pusher publisher (stage/stdout/stderr/result, env creds, <10KB chunking, monotonic seq) in internal/publisher
- [x] 02-03-PLAN.md â DockerSocketRunner over the moby SDK: full hardening + attach/demux + tree-kill + idempotent cleanup + cgroup-v2 CPU reader
- [x] 02-04-PLAN.md â Guarded Docker integration tests: hardening inspect, three clocks, truncation, stdin round-trip, no-leak label check + make target

### Phase 3: Interactive Python End-to-End

**Goal**: Drive one language (Python 3.12) through the entire interactive path â Hono API, Redis routing, worker session with start-handshake, soketi output â so `/execute â subscribe â /start â stdin â result` works end-to-end against the local stack.
**Mode:** mvp
**Depends on**: Phase 2
**Requirements**: API-01, API-02, API-03, API-04, API-05, API-06, API-07, API-08, API-09, API-10, API-11, WRK-01, WRK-02, WRK-03, WRK-04, SESS-01, SESS-02, SESS-03, STDIN-01, STDIN-02, STDIN-03, OUT-01, OUT-02, OUT-03, LANG-04, LANG-05, CFG-01, CFG-02, CFG-03, CHAN-02, DEV-01, DEV-02, DEV-03
**Success Criteria** (what must be TRUE):

  1. The Hono API serves all endpoints â `POST /v1/execute` (returns `202 {jobId, channel, status:"queued"}` before the process starts), `/start`, `/stdin`, `/stdin/close`, `/kill`, `GET /v1/jobs/:id`, `GET /v1/languages` â with constant-time `EXECUTOR_API_TOKEN` auth, contract-derived validation with clear errors, and a per-job stdin rate-limit + pending-byte cap returning `429`; it talks to the worker only via Redis.
  2. The start-handshake holds: the process only starts on `/start` after the client subscribes, so no early prompt is lost; batch (no-stdin) runs as the degenerate case; a warm-up timeout reclaims the slot if `/start` never arrives.
  3. stdin and control route without service discovery: the API `PUBLISH`es `stdin:<jobId>` and per-job control, and only the owning worker (subscribed to its live jobs) writes to the pipe; `/stdin/close` delivers EOF exactly once.
  4. The worker keeps the Python process alive with pipes open and publishes `stage`, `stdout`/`stderr`, and a terminal `result` event on `private-run-<jobId>`; per-request `limits` override the manifest `defaultLimits`.
  5. The Python 3.12 package runs `python main.py` with numpy/pandas/requests baked in, driven entirely by its manifest with no hardcoding.
  6. `docker compose up` brings up api + worker + redis + soketi + a stub upstream app; the stub and a documented script drive a full interactive execute end-to-end; all config is env-only (no config or secret-returning endpoints, soketi secret never persisted in Redis).

**Plans**: 6 plans
Plans:

- [x] 03-01-PLAN.md â Go Redis layer: go-redis client, real pub/sub StdinTransport (stdin:/ctrl:), job store + BRPOP queue consumer (Wave 1)
- [x] 03-02-PLAN.md â Worker run loop: claim â create â start-handshake/warm-up â session.RunInteractive (publisher sinks + stdin routing) â single teardown; guarded full-Python integration test (Wave 2)
- [x] 03-03-PLAN.md â Python 3.12 image: python:3.12-slim + numpy/pandas/requests, non-root, PYTHONUNBUFFERED unbuffered streaming (Wave 1)
- [x] 03-04-PLAN.md â Hono API: all /v1/* endpoints, constant-time bearer auth, generated-zod validation, jobId+channel gen, LPUSH+spec/status, PUBLISH stdin/control, 429 rate/byte-cap, optional channel-auth (Wave 1)
- [x] 03-05-PLAN.md â docker-compose + stub upstream + scripts/e2e.sh + README quickstart; blocking human-verify of the live interactive demo (Wave 3)

**UI hint**: yes

### Phase 4: Abuse Suite & Safety Validation

**Goal**: Build the abuse test suite immediately after the Python E2E path and run it on Linux CI so real cgroup OOM/CPU behavior is exercised â this suite is the verification backbone and **gates the language fan-out**.
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

- [x] 04-01-PLAN.md â Adversarial abuse suite (fork bomb, OOM, infinite loop, idle, EOF, giant output + CPU-evasion) through the full worker path, build-tagged + `make abuse`, run green on cgroup v2
- [x] 04-02-PLAN.md â Linux CI workflow (.github/workflows/abuse.yml) running `make abuse` on ubuntu-latest + README gate note tying the language fan-out to a green abuse run

### Phase 5: Statelessness & Scale

**Goal**: Make the API and workers safely horizontally scalable â slot-bounded capacity, backpressure instead of dropped work, reliable claim, a reaper that prevents container/slot leaks on worker death, and a documented autoscaling/scale-to-zero design under the native-Redis-for-worker constraint.
**Mode:** mvp
**Depends on**: Phase 4
**Requirements**: SCALE-01, SCALE-02, SCALE-03, SCALE-04, SCALE-05
**Success Criteria** (what must be TRUE):

  1. The API and workers run as N stateless replicas with no shared in-process state.
  2. A worker claims a job only when it has a free sandbox slot; capacity is counted in concurrent live sandboxes (derived from CPU/RAM), not request bursts, using a reliable claim mechanism.
  3. Queue depth and full-capacity conditions surface as backpressure (`429`) rather than silently dropping work.
  4. Worker death mid-session leaves no orphaned host containers: a label-based reaper removes them and reclaims their slots.
  5. The repo documents the autoscaling-by-queue-depth and scale-to-zero mechanism per deploy target â where the scaling unit is the **worker node** (which launches its sandboxes internally and hosts N concurrent ones), not a microVM per execution â including the requirement that the worker's Redis speak native pub/sub + blocking ops (Upstash is API-only, not worker-viable); the `FlyMachinesRunner` microVM-per-execution model is noted as a v2 option.

**Plans**: 3 plans
Plans:

- [x] 05-01-PLAN.md â Worker slot semaphore (acquire-before-claim) + ephemeral workerId + Redis heartbeat/owned-jobs substrate + new keys/config + concurrency-cap test
- [x] 05-02-PLAN.md â Label-based dead-worker reaper (removes orphaned containers + anonymous volumes, frees slots, marks jobs error) wired into every worker + integration test
- [x] 05-03-PLAN.md â API job-admission 429 (queue-depth backpressure) + autoscale-by-queue-depth/scale-to-zero docs (docs/scaling.md + README) + docker compose --scale worker=2 smoke

### Phase 6: Language Fan-out

**Goal**: Prove the manifest extensibility invariant by adding Rust, R, and SQLite as folder + image with zero core changes â including the SQLite case that stress-tests whether "language = image + compile? + run" holds for something that is not a general-purpose language.
**Mode:** mvp
**Depends on**: Phase 5
**Requirements**: LANG-06, LANG-07, LANG-08
**Success Criteria** (what must be TRUE):

  1. The Rust package compiles with `rustc -O main.rs -o /tmp/prog` (a compile stage with its own limits) then runs the produced binary â added with no changes to the worker or API.
  2. The R 4.4 package runs `Rscript main.R` with common libs baked in, driven by its manifest.
  3. The SQLite 3 package runs SQL against an ephemeral in-memory DB, supporting both a `.sql` file and an interactive `sqlite3` shell reading from stdin.
  4. Each language is added purely as `languages/<lang-version>/{manifest.json, Dockerfile}` and the abuse suite (Phase 4 gate) passes for the new images.

**Plans**: 4 plans
Plans:

- [x] 06-01-PLAN.md â Task 0: generic manifest-driven compile stage (extend Sandbox seam + worker run path + stub + generic gate test + Wave-2 Makefile targets) â the ONLY core change
- [x] 06-02-PLAN.md â Rust 1.83 package (rustc -O compile + produced-binary run) + e2e compile/run + compile-error test
- [x] 06-03-PLAN.md â R 4.4 package (Rscript main.R, common libs, unbuffered streaming, null compile) + e2e Rscript + interactive stdin test
- [x] 06-04-PLAN.md â SQLite 3 package (alpine+sqlite, :memory: SQL, .sql file + interactive shell, clean EOF) + e2e file + interactive SELECT test

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

- [x] 07-01-PLAN.md â Complete README: quickstart, full /v1/* API contract + output events, deployment-per-target, add-a-language guide, channel-auth (DOCS-01..04, CHAN-01)
- [x] 07-02-PLAN.md â Broader CI matrix (.github/workflows/ci.yml): lint, go-unit, js+redis, contract-drift

**UI hint**: yes

### Phase 8: Distributed Observability

**Goal**: Make code-runner observable end-to-end across the polyglot seam â OpenTelemetry traces, metrics, and structured logs in both the Hono API and the Go worker â with a bring-your-own OTel stack model that is entirely env-gated (no-op when unset), so self-hosters can wire any backend without forced infrastructure.
**Mode:** mvp
**Depends on**: Phase 7
**Requirements**: OBS-01, OBS-02, OBS-03, OBS-04, OBS-05, OBS-06, OBS-07, OBS-08
**Success Criteria** (what must be TRUE):

  1. Both the API and worker initialize an OpenTelemetry SDK driven only by standard `OTEL_*` env vars; when no exporter endpoint is configured the SDK is a no-op with no behavioral or measurable startup change.
  2. A single execution emits one connected distributed trace: the API's `/v1/execute` span and the worker's execution spans share one trace via W3C trace context carried through the wire contract across Redis (not HTTP), with worker phases (`claim`, `sandbox.create`, `handshake.wait`, `compile`, `run`, `publish.result`) appearing as spans, and per-chunk output represented as metrics rather than spans.
  3. Domain metrics are emitted (queue depth, sandbox slots used/max, time-in-queue, terminal-state counts incl. `timedOut`/`idleTimedOut`/`cpuExceeded`, sandbox create/kill latency, warmup reclaims, reaper orphans, admission/ratelimit rejections, soketi publish latency/errors) and are exported both via OTLP push (default) and a scrapeable Prometheus `/metrics` endpoint (opt-in, separate admin port).
  4. Both services emit structured JSON logs carrying `trace_id`/`span_id`/`job_id` correlation fields; the API is migrated off `console.log` to match the worker's `slog`.
  5. The wire contract is extended (schema + regenerated TS/zod/Go via `pnpm contract`, never hand-edited) to carry W3C trace context, and `make contract-check` stays green.
  6. A commented example OTel Collector service is provided in `docker-compose.yml` and `.env.example` documents every new `OTEL_*` var as the BYO-stack integration point.

**Plans**: 6 plans

- [x] 08-01-PLAN.md â Wire contract traceparent/tracestate carrier + cross-language trace_id round-trip test scaffold (OBS-02)
- [x] 08-02-PLAN.md â Worker OTel init (no-op gate) + stdout slog correlation + traceparent extract/linked phase spans + terminal counter (OBS-01,02,03,04,07)
- [x] 08-03-PLAN.md â API NodeSDK bootstrap (--import load-order) + execute span + traceparent inject + pino logging (OBS-01,02,04,07)
- [x] 08-04-PLAN.md â Full OBS-06 domain metrics (worker gauges/histograms/counters + API rejection counters); finish console->pino (OBS-06,07)
- [x] 08-05-PLAN.md â SDK Node caller propagation + compose observability profile + Jaeger/collector + .env.example + E2E connected-trace proof (OBS-02,04,07,08)

> Scope: OBS-05 (Prometheus /metrics pull endpoint + admin port) DROPPED this phase per CONTEXT D-04  OTLP push only — worker stays HTTP-server-free.

### Phase 9: Artifacts & Pullable Run Output

**Goal:** Let a consumer (the edalef backend) capture artifacts (images/files) generated by sandboxed user code and pull the full run output server-side â without subscribing to soketi â so the synchronous grading flow gets `stdout`+`stderr`+`exitCode`+`artifacts` in one place. The locked design (see `09-SPEC.md` + `09-CONTEXT.md`; the original draft was overridden during discuss-phase):

1. **Contract (URL-only)** â add `Artifact {name, mimeType, bytes, url}` (NO inline base64, NO discriminator), a persisted `RunResult {exitCode, signal, timedOut, idleTimedOut, truncated, durationMs, stdout, stderr, artifacts, artifactsTruncated}`, `Limits.maxArtifacts`/`maxArtifactBytes`, an opt-in `ExecuteRequest.collectOutput`, and an `events.artifact`; regenerate TS + zod + Go.
2. **Worker** â capture by **workspace diff** (new files in cwd `/workspace`, excluding input files / `.compile_ready` / compile outputs) via a `DockerSandbox.ReadArtifacts` extension + `CopyFromContainer`, read BEFORE the idempotent `sync.Once` teardown, apply caps (20 files / 4 MB), upload to the `ArtifactStore`, attach presigned-URL `Artifact`s, emit a metadata-only `artifact` soketi event; accumulate stdout/stderr (reusing the `outputKb` budget + shared `truncated` flag) and persist the `RunResult` to a new Redis key with an env-configurable TTL.
3. **`ArtifactStore` seam** â single shipped `S3Store` (minio-go) reading `AWS_*` (with `ARTIFACT_S3_*` overrides), uploading under `artifacts/<jobId>/`, returning presigned GET URLs (default 24 h), and setting a bucket lifecycle expiration on the `artifacts/` prefix at boot; config fail-fast when object TTL < presigned-URL TTL. Object storage is required for artifacts (graceful no-artifacts fallback when unconfigured).
4. **API** â new `GET /v1/jobs/:id/output` returning the persisted `RunResult` (404 unknown/non-collected/expired, 401 unauthenticated, Redis-only).
5. **Images (no shims)** â install matplotlib (`MPLBACKEND=Agg`) in the Python image; reconcile the R graphics stack + `R_DEFAULT_PACKAGES` so `png()` works; users `savefig`/`png()` to a relative path.
6. **SDKs** â Node `getOutput(id): RunResult`; React `useCodeRunnerJob` exposes `artifacts[]` at job end.
7. **Infra/deploy** â MinIO in dev compose + `.env.example` documentation; Fly provisions Tigris (`fly storage create`) wired to the worker's `S3Store` (API needs no S3 creds).

Artifacts travel as presigned URLs in the pulled `RunResult` and a metadata-only soketi event â never bytes over soketi. Network-egress anti-cheat is already covered by the existing `--network=none` sandbox. **Out of scope (deferred):** the blocking `POST /v1/run` endpoint, env-gated webhooks, inline base64 storage, and per-language `plt.show()` shims.
**Requirements**: R1â€“R16 (locked in 09-SPEC.md)
**Depends on:** Phases 1â€“7 (sandbox + contract + manifests + SDKs). Does **not** depend on Phase 8 (observability) â can be planned and executed independently.
**Plans:** 7/7 plans complete

Plans:
**Wave 1**

- [x] 09-01-PLAN.md â Contract: URL-only Artifact + RunResult + Limits caps + collectOutput + jobOutput key/artifact event mirror + zod round-trip test (R1, R2, R3, R8) [Wave 1 â BLOCKING seam]

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 09-02-PLAN.md â ArtifactStore seam + S3Store (minio-go) + config three TTLs + Validate() fail-fast + boot wiring (R7, R15) [Wave 2]
- [x] 09-03-PLAN.md â Python matplotlib (MPLBACKEND=Agg) + R graphics stack / R_DEFAULT_PACKAGES reconcile, no shims (R10, R11) [Wave 2]

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 09-04-PLAN.md â Worker workspace-diff capture before Cleanup + caps + upload + artifact event + Sinks accumulation + RunResult persist + tests (R4, R5, R6, R8) [Wave 3]

**Wave 4** *(blocked on Wave 3 completion)*

- [x] 09-05-PLAN.md â API GET /v1/jobs/:id/output (Redis-only, 200/404/401) + route test (R9) [Wave 4]
- [x] 09-06-PLAN.md â Node SDK getOutput + React hook artifacts[] from soketi artifact events (R12, R13) [Wave 4]
- [x] 09-07-PLAN.md â MinIO dev compose + .env.example docs + Fly/Tigris worker wiring + deploy-fly.md (R14, R16) [Wave 4]

## Phase Details — Milestone v1.1

### Phase 10: Pre-Import Contract

**Goal**: Add the `preimport` manifest field to the JSON-Schema single source of truth and regenerate the polyglot contract so every downstream reader (TS, zod, Go) agrees on the pre-import seam — then declare the Python and R pre-import sets while keeping Rust/SQLite valid and Docker-routed. This is the schema seam everything else in the milestone reads, so it ships first and low-risk.
**Mode:** mvp
**Depends on**: Phase 9 (v1.0 complete)
**Requirements**: PRE-01, PRE-02, PRE-03, PRE-04
**Success Criteria** (what must be TRUE):

  1. The manifest JSON Schema gains an optional `preimport` field (a list of modules/packages); the contract is regenerated via `pnpm contract` (TS types + zod validators + Go structs) and `make contract-check` stays green with no hand-edits to `packages/contract/gen/**`.
  2. The Python 3.12 manifest declares its `preimport` set (the science stack the spike pre-imported) and loads cleanly through both the Go and TS manifest loaders at boot.
  3. The R 4.4 manifest declares its `preimport` set and loads cleanly through both loaders.
  4. A manifest with no `preimport` field (Rust, SQLite) still validates and is treated as zygote-ineligible — the absence of the field is the routing signal, with no language-name branching.

**Plans**: TBD

### Phase 11: Zygote Agents & Per-Child Hardening

**Goal**: Build the language-side mechanism, liftable from the validated spike scripts (005/006): a per-language zygote agent that *is* the language runtime with the manifest set pre-imported, listens for spawn requests over a minimal pipe, and double-forks a hardened child per session that runs user code with full per-child isolation and stdio wired back. The agent holds no worker credentials (design rule #1, proven necessary in spike 006).
**Mode:** mvp
**Depends on**: Phase 10
**Requirements**: AGENT-01, AGENT-02, AGENT-03, AGENT-04, ZHARD-01, ZHARD-02, ZHARD-03, ZHARD-04, ZHARD-05, ZHARD-06
**Success Criteria** (what must be TRUE):

  1. A Python zygote agent pre-imports the manifest `preimport` set once, then on each spawn request forks a child that runs the supplied user files + run argv + limits; an R zygote agent provides the identical behavior for R.
  2. The agent is credential-free — it holds no Redis/soketi/queue FDs and communicates with the worker only over a minimal pipe/socket — and each spawned child scrubs inherited fds > 2 before executing user code (defense in depth, since `fork()` inherits FDs and `CLOEXEC` does not help without `exec()`).
  3. Each child runs under a distinct UID with `no_new_privs` set, in its own PID namespace (created via double-fork so it sees only itself in `/proc`), and with a private `/tmp` tmpfs it cannot escape to read a sibling's `/tmp`.
  4. Each child is placed in its own cgroup-v2 sub-cgroup with `memory.max` + `pids.max` so one child's OOM or fork-bomb cannot starve siblings.
  5. A spawn request carries the user's files + run argv + limits and the child runs them with stdin/stdout/stderr wired back through the agent to the worker, behaving like a normal interactive sandbox process.

**Plans**: TBD

### Phase 12: Go ZygoteRunner & Warm Pool

**Goal**: Build the Go side behind the existing `Runner`/`Sandbox` interface — a `ZygoteRunner` that forks a hardened child sandbox from a warm per-language parent and satisfies every interface method with semantics identical to `DockerSocketRunner`, fed by a warm parent pool (one per language/version) with idle reaping and crash respawn so `Create` is fork-fast and the three clocks govern zygote children exactly as they govern docker sandboxes.
**Mode:** mvp
**Depends on**: Phase 11
**Requirements**: ZYG-01, ZYG-02, ZYG-03, ZYG-04, ZYG-05, ZYG-06, POOL-01, POOL-02, POOL-03, POOL-04
**Success Criteria** (what must be TRUE):

  1. `ZygoteRunner.Create` returns a hardened forked-child `zygoteSandbox` from a warm per-language parent, and `zygoteSandbox` implements every `Sandbox` method (Stdin/Stdout/Stderr/Wait/Kill/Cleanup/Compile) with semantics identical to `dockerSandbox`.
  2. `Kill` terminates the entire child process tree (the child's PID-ns init), never just one PID, and `CPUReader` reads the child's cgroup-v2 `cpu.stat` so the session CPU clock works for zygote children.
  3. The wall, idle, and CPU clocks govern zygote children exactly as they do docker sandboxes (a CPU-bound child hidden behind stdin reads is still caught), enforced by the existing `internal/session` contract unchanged.
  4. `Cleanup` is idempotent (`sync.Once`) and leaks no pipe, fd, cgroup leaf, parent, or slot on any exit path (normal, error, panic).
  5. The worker maintains a warm parent pool (one parent per `(language, version)`) that is pre-warmed so `Create` is fork-fast with no cold image start per job; idle parents are reaped after a configurable window to reclaim RAM; a dead/crashed parent is detected and respawned, and in-flight jobs on a dead parent fail cleanly without leaking slots.

**Plans**: TBD

### Phase 13: Tiered Routing, Deploy & Gating

**Goal**: Wire the zygote path into the worker as a tier — a manifest-driven `TieredRunner` that routes Python/R to the `ZygoteRunner` and Rust/SQLite to `DockerSocketRunner`, with all four languages working end-to-end — then make it deployable and safe: a Fly deploy config granting the privileged pool its required capabilities (gated to the Fly/prod runtime where Firecracker is the host boundary) and a config flag that keeps dev + CI on Docker by default.
**Mode:** mvp
**Depends on**: Phase 12
**Requirements**: TIER-01, TIER-02, TIER-03, TIER-04, ZDEP-01, ZDEP-02, ZDEP-03
**Success Criteria** (what must be TRUE):

  1. A `TieredRunner` selects `ZygoteRunner` for zygote-opted manifests (those declaring `preimport`) and `DockerSocketRunner` otherwise, with routing driven entirely by the manifest — no language-name branching in worker logic.
  2. All four languages (Python, R, Rust, SQLite) run end-to-end through the `TieredRunner`: Python and R via the zygote path, Rust and SQLite via Docker, each passing its existing interactive/compile path.
  3. `DockerSocketRunner` remains the fallback whenever zygote is disabled or unavailable, so Python/R still execute correctly with the zygote flag off.
  4. `ZygoteRunner` is gated to the Fly/production runtime via a config flag with a safe default (off → Docker); dev and CI run `DockerSocketRunner`, and enabling/disabling zygote requires no code change.
  5. The Fly deploy config grants the zygote pool the privilege it needs to rebuild per-child isolation (`CAP_SYS_ADMIN`, `CAP_SETUID`, writable host cgroups), documented as acceptable only because Firecracker — not container caps — is the host boundary on Fly.

**Plans**: TBD

### Phase 14: Zygote Safety, Density & Pool Observability

**Goal**: Prove the zygote path is production-ready and operable before the milestone closes — the verification backbone that mirrors how Phase 4 gated v1.0. The full abuse suite passes on the zygote path at Phase-4 parity, isolation tests prove a child cannot reach a sibling's memory/`/proc`/`/tmp`/FDs, density is measured as materially higher than Docker on the same node, no leaks occur across many sequential and concurrent jobs, and pool metrics flow through the existing OpenTelemetry instrumentation so dashboards stay runner-agnostic.
**Mode:** mvp
**Depends on**: Phase 13
**Requirements**: ZTEST-01, ZTEST-02, ZTEST-03, ZTEST-04, ZOBS-01, ZOBS-02
**Success Criteria** (what must be TRUE):

  1. The abuse suite (fork bomb, OOM, infinite loop, idle timeout, EOF, giant-output truncation) passes for the zygote path with Phase 4 parity — each adversarial case is contained without harming the parent, the pool, or sibling sessions.
  2. Isolation tests prove a zygote child cannot read a sibling's memory, `/proc`, or `/tmp`, nor use an inherited Redis/soketi/queue FD (rule #1 + per-child hardening verified end-to-end through the real runner, not just the spike).
  3. Density is verified — Python reaches materially higher concurrency on the zygote path than on Docker on the same node — and no slot, parent, or child leaks occur across many sequential and concurrent jobs.
  4. Pool metrics are emitted via the existing OpenTelemetry instrumentation: per-language parent occupancy, warm/idle parent counts, fork (spawn) latency, and parent reap/respawn counts.
  5. Every terminal/error path on the zygote runner increments the same domain counters as the docker path (terminal-state, kill latency) so observability dashboards remain runner-agnostic.

**Plans**: TBD

## Phase Details — Milestone v1.2

### Phase 15: Multi-file Input (inline)

**Goal**: Let a trusted caller ship multiple input files — text *and* binary, in subdirectories — inline in a single `/v1/execute` request, materialized safely under `/workspace` before the run starts, with zero new infrastructure. This is the additive, backward-compatible, independently-shippable layer: existing text-`content` callers are unchanged, `base64` unlocks binary, and the host-escape-only threat model is enforced in the worker regardless of API validation.
**Mode:** mvp
**Depends on**: Phase 14 (v1.1 complete)
**Requirements**: FILES-01, FILES-02, FILES-03, FILES-04, FILES-05, FILES-06, FILES-07, FILES-08
**Success Criteria** (what must be TRUE):

  1. A caller submits several input files in one `/v1/execute` and they all materialize in the sandbox workspace before the run starts; omitting `encoding` is fully backward-compatible with existing text-`content` callers.
  2. A caller sends a `.xlsx` (or parquet/zip/image) as `FileInput.encoding: "base64"` and the Python run reads it from `/workspace`; a file named `data/input.csv` lands at `/workspace/data/input.csv` with the parent directory created.
  3. A path that tries to escape (`/etc/passwd`, `../../x`) never writes outside `/workspace` — the worker sanitizes every path (`path.Clean` anchored at `/` so traversal collapses inside the workspace) independently of any API validation (host-escape-only posture).
  4. The API rejects an over-large request (sum of decoded input bytes) with HTTP 413 governed by a configurable `MAX_FILES_BYTES`, and rejects invalid base64 or escaping/absolute paths with HTTP 400 before the job is enqueued.
  5. A subdirectory input is never echoed back as a captured artifact — `collectOutput` excludes every input file by its full relative path (not just basename); the Node SDK accepts both text and `Buffer` inputs and sets `encoding` transparently.

**Plans**: TBD

> Constraints: The contract is the fragile seam — `FileInput.encoding` + subdir paths are added by editing `packages/contract/schema/wire.schema.json`, then `pnpm contract` regenerates TS+zod+Go and `make contract-check` must stay green (never hand-edit `packages/contract/gen/**`). Path sanitization is enforced **in the worker** (anchored `path.Clean`), not just the API. Worker changes: base64 decode, parent-dir tar entries, and fixing `buildArtifactExcludeSet` to key on the full relative path. Zero new infrastructure — this phase is independently shippable.

### Phase 16: Content-Addressed Blob Store (CAS)

**Goal**: Dedupe large/shared input files across runs via a content-addressed (sha256) blob store that code-runner OWNS, so a caller uploads a big file once and references it by hash thereafter — without breaking the thin gateway (bytes go client→store directly, never through Hono) or the host-escape-only / no-SSRF posture (the worker pulls only from our own known store). Liveness is the blob's, not the job's: a reference can only *extend* the TTL (monotonic, touch-on-use), a live run *leases* its blobs so GC skips them, and GC uses a grace window — mirroring the existing Lua-guarded slot-accounting pattern.
**Mode:** mvp
**Depends on**: Phase 15 (extends the `FileInput` shape established there with a `ref` variant)
**Requirements**: BLOB-01, BLOB-02, BLOB-03, BLOB-04, BLOB-05, BLOB-06, BLOB-07, BLOB-08, BLOB-09, BLOB-10, BLOB-11, BLOB-12
**Success Criteria** (what must be TRUE):

  1. A caller `POST`s a list of sha256 hashes to `/v1/blobs/check` and gets back which are missing, each with a presigned PUT URL pointing at code-runner's own store; the bytes travel client→store directly via that URL and never pass through the Hono gateway.
  2. The store verifies `sha256(bytes) == hash` when the upload finalizes (rejecting a mismatch), and the worker re-verifies the sha256 of a referenced blob before the run uses it — pulling ONLY from code-runner's own store at a known host, never an arbitrary consumer-supplied URL (no SSRF).
  3. A caller references an already-uploaded blob via `FileInput.ref` (`{ name, ref: "sha256:…" }`) alongside inline files in the same request, and the worker streams that blob from the store into the sandbox workspace without buffering the whole file in worker RAM.
  4. Blob liveness is tracked in Redis as a monotonic, touch-on-use idle TTL (a reference only ever extends it); a live run leases every blob it references so GC never deletes an in-use blob, and GC applies a grace window before reclaiming an expired one.
  5. The Node SDK exposes `client.blobs.upload(buffer, { ttlSeconds })` (hash → existence check → upload only missing bytes) and `execute()` transparently routes each file inline-vs-CAS by a size threshold; operators can point code-runner at their own S3 bucket via env while code-runner still owns the CAS key layout + the Redis liveness index, with minio shipped inert in docker-compose.

**Plans**: TBD

> Constraints: The contract is the fragile seam — the `FileInput.ref` variant and `/v1/blobs/check` request/response are added in `packages/contract/schema/wire.schema.json` → `pnpm contract` → `make contract-check` green (never hand-edit `gen/**`). No-SSRF is non-negotiable: the worker pulls only from our own store at a known host; the presigned URL is issued by code-runner. The gateway stays thin — blob bytes never transit Hono. Reuse the Phase-9 `internal/artifactstore` (`S3Store`/minio-go) plumbing where it fits; the Redis TTL/lease mirrors the existing Lua-guarded slot-accounting pattern. This phase needs an object-store bucket (minio locally; BYO-bucket in prod) — it is NOT zero-infra like Phase 15.

## Progress

**Execution Order:**
Phases execute in numeric order: 1 -> 2 -> 3 -> 4 -> 5 -> 6 -> 7 -> 8 -> 9 -> 10 -> 11 -> 12 -> 13 -> 14 -> 15 -> 16

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundation & Wire Contract | 3/3 | Complete   | 2026-06-02 |
| 2. Sandbox Hardening & Runner | 4/4 | Complete   | 2026-06-02 |
| 3. Interactive Python End-to-End | 5/5 | Complete   | 2026-06-02 |
| 4. Abuse Suite & Safety Validation | 2/2 | Complete   | 2026-06-02 |
| 5. Statelessness & Scale | 3/3 | Complete   | 2026-06-02 |
| 6. Language Fan-out | 4/4 | Complete   | 2026-06-03 |
| 7. OSS Release & Deployment | 2/2 | Complete   | 2026-06-03 |
| 8. Distributed Observability | 6/6 | Complete   | 2026-06-03 |
| 9. Artifacts & Pullable Run Output | 7/7 | Complete   | 2026-06-03 |
| 10. Pre-Import Contract | 0/? | Not started | - |
| 11. Zygote Agents & Per-Child Hardening | 0/? | Not started | - |
| 12. Go ZygoteRunner & Warm Pool | 0/? | Not started | - |
| 13. Tiered Routing, Deploy & Gating | 0/? | Not started | - |
| 14. Zygote Safety, Density & Pool Observability | 0/? | Not started | - |
| 15. Multi-file Input (inline) | 1/1 | Complete   | 2026-06-09 |
| 16. Content-Addressed Blob Store (CAS) | 2/2 | Complete   | 2026-06-09 |
