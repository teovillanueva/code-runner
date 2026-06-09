# code-runner

## What This Is

An **open-source (MIT), self-hostable** remote code execution service (Piston-style): it receives code, runs it in an isolated hardened sandbox with **live interactive stdin**, and streams output in real time. It is dockerized and horizontally scalable.

It is an **internal service** — never exposed to the internet directly. In front of it sits the user's own backend (any stack) that consumes it by authenticating with a bearer token. Real-time output reaches the browser via **soketi** (Pusher-compatible): the worker publishes output events; soketi is **output-only** toward the client. All trusted input (code, stdin, control) enters through our API; nothing trusted enters via soketi.

It is a **polyglot monorepo by design** — each component uses the right tool: a thin Hono/TypeScript HTTP gateway, a Go worker that orchestrates sandboxes and keeps sessions alive, manifest-driven language packages, and a shared wire contract.

## Core Value

Run untrusted code in a hardened, resource-bounded sandbox with a live interactive stdin session and reliable real-time output — without ever leaking a container, a subscription, or a session slot — and make it trivially self-hostable and extensible (add a language = add a folder + an image).

## Current Milestone: v1.2 Input Files & Content-Addressed Blobs

**Goal:** Let callers send arbitrary input files alongside the code — text *and* binary, in subdirectories — and dedupe large/shared files across runs via a content-addressed (sha256) blob store, without breaking the thin-gateway or host-escape-only security posture.

**Target features:**
- **Multi-file input (inline):** `FileInput.encoding` (`utf8` default | `base64`) so binary files (xlsx/parquet/images/zip) ship inline; subdirectory paths in `FileInput.name` (e.g. `data/input.csv`) materialized under `/workspace`; worker-side path sanitization (no escape); a configurable `MAX_FILES_BYTES` body cap; Node SDK Buffer/text passthrough.
- **Content-addressed blob store (CAS):** a `Blob` interface over an S3-compatible store (reusing the Phase-9 artifact-store plumbing where it fits); `POST /v1/blobs/check` → missing hashes + presigned PUT URLs to *our* store; sha256 verify on finalize and on worker pull; a `FileInput.ref` variant; Redis-tracked idle TTL (monotonic, touch-on-use) + per-run lease so GC never deletes an in-use blob; worker streams blobs into the sandbox; Node SDK `blobs.upload` + transparent inline-vs-CAS routing; BYO-bucket via env; minio shipped inert in compose.

**Locked decisions:**
- **`encoding` is additive and default-`utf8`** — existing callers (text `content`) are unchanged; `base64` unlocks binary. Subdir paths are sanitized with `path.Clean` anchored at `/` so traversal collapses inside `/workspace`; sanitization is enforced in the **worker** regardless of API validation (host-escape-only threat model).
- **code-runner OWNS the CAS store** (not the consumer) — required for global dedup to mean anything. The presigned URL is *issued by code-runner* and points at our own store; the worker pulls only from that known host → **no SSRF**. Bytes go client→store directly, never through the Hono gateway (keeps it thin).
- **TTL is the blob's, not the job's** — a reference can only *extend* the TTL (monotonic, touch-on-use); a live run *leases* its blobs so GC skips them; GC uses a grace window. Mirrors the existing Lua-guarded slot-accounting pattern.
- **Single trusted backend caller** (bearer token, never internet-exposed) → global CAS dedup is safe; **no per-tenant namespacing** in v1.2 (deferred to if/when mutually-distrusting end users reference hashes directly).

**Layering:** Phase 15 (inline) is zero-new-infra and independently shippable. Phase 16 (CAS) needs an object-store bucket — implemented + tested locally (minio), with deploy steps documented; prod storage is not half-provisioned autonomously. See the design discussion captured in `.planning/decisions/` (input-files + CAS).

## Requirements

### Validated

(None yet — ship to validate)

### Active

<!-- All hypotheses until shipped and validated. Detailed REQ-IDs live in REQUIREMENTS.md. -->

- [ ] **Hono/TS API gateway** — the single trusted entry point: bearer-token auth (constant-time), validation, enqueue jobs, publish stdin, expose job status + languages. Contract: `POST /v1/execute`, `/v1/jobs/:id/start`, `/v1/jobs/:id/stdin`, `/v1/jobs/:id/stdin/close`, `/v1/jobs/:id/kill`, `GET /v1/jobs/:id`, `GET /v1/languages`
- [ ] **Go worker** — consumes the Redis queue, launches ephemeral hardened sandboxes via a `Runner` interface, subscribes to `stdin:<jobId>`, keeps the session alive, publishes output to soketi, enforces the three clocks. Communicates with the API **only via Redis + soketi**; never calls the API
- [ ] **`packages/contract`** — the wire JSON spoken between API and worker defined **once** (JSON Schema single source of truth) → generated TS types + Go structs + a CI drift check (the fragile polyglot seam)
- [ ] **Manifest-driven language packages** — `languages/<lang-version>/{manifest.json, Dockerfile}`; core loads manifests at boot; nothing hardcoded in Go or the API
- [ ] Four initial languages: Python 3.12, Rust, R 4.4, SQLite 3 (interactive SQL shell)
- [ ] Live interactive session with start-handshake (queued → subscribe → start) and three independent clocks (wall, idle, CPU/cgroup)
- [ ] Full per-execution sandbox hardening + output/stdin caps + stdin rate limit
- [ ] Deterministic, idempotent lifecycle/cleanup — no leaked containers, subscriptions, or slots
- [ ] Config entirely via env vars (`EXECUTOR_API_TOKEN`, `REDIS_URL`, `SOKETI_*`); no config endpoints, no secret-returning endpoints, no secrets persisted in Redis
- [ ] `docker compose up` dev stack: api + worker + redis + soketi + stub upstream app
- [ ] **Abuse test suite built EARLY** (right after Python E2E, gating language fan-out): fork bomb, OOM, infinite loop (wall), stdin-blocked (idle), EOF (stdin/close), giant output (truncation) — on Linux CI
- [ ] OSS deliverables: MIT `LICENSE`, `.env.example`, README quickstart, "add a language" guide, deployment-per-target guide

### Out of Scope

- Building the upstream consumer app — it's the user's own backend; we ship only a **stub** for local E2E
- End-user authentication / complex auth — the only auth is the `EXECUTOR_API_TOKEN` bearer between upstream and our API
- Authorizing the soketi private channel as a core feature — that's the **upstream app's responsibility** using the app key/secret (documented; an optional non-core helper may be offered)
- Any endpoint that returns secrets, or persisting the soketi secret in Redis
- Exposing the service to the internet — internal-only behind the upstream app + private network/TLS
- Runtime dependency resolution inside sandboxes — images are pre-built with libs baked in (enables `--network=none` always)
- Trusting input from soketi — soketi is output-only
- Docker-in-Docker — the worker talks to the host container runtime via a mounted socket (dev)

## Context

- **Data flow:** upstream app (external) ──token+TLS──► API (Hono) ──► Redis (queue) ──► Worker pool (Go, N replicas) → hardened sandbox. stdout/stderr → worker triggers soketi directly. stdin → API `PUBLISH stdin:<jobId>` on Redis → owning worker → process pipe. The upstream app authorizes the browser's private soketi channel.
- **Decoupling:** the worker is decoupled from the API; they communicate **only** via Redis (jobs + stdin) and soketi (output). No service discovery — the worker subscribes only to its own live jobs.
- **Extensibility is central** (Piston model): manifest declares `language, version, aliases, image, entrypoint, compile (nullable), run, interactive, defaultLimits{wallTimeMs, idleMs, cpuMs, memoryMb, pids, outputKb}`.
- **The interactive session is the hard part:** the sandbox is NOT batch-ephemeral; it keeps the process alive with pipes open, governed by three clocks. SQLite 3 (SQL against an ephemeral in-memory DB via the `sqlite3` shell on stdin) deliberately stress-tests whether the "language = image + compile? + run" abstraction holds for something that isn't a general-purpose language.
- **Initial languages:** Python 3.12 (`python main.py`, numpy/pandas/requests baked in), Rust (`rustc -O main.rs -o /tmp/prog` then run the binary), R 4.4 (`Rscript main.R`, common libs), SQLite 3 (`.sql` file + interactive shell).
- **Build order (per user, with explicit approval gate):** (1) propose monorepo layout + shared wire contract + manifest schema and **wait for OK before coding**; (2) implement Python end-to-end (interactive stdin + start-handshake + three clocks) and validate with abuse tests; (3) add Rust, R, SQLite reusing the same package model. Atomic commits per milestone.

## Constraints

- **Stack (definitive)**: `apps/api` = **Hono (TypeScript)** thin gateway; `apps/worker` = **Go** (native container/process ecosystem); `packages/contract` = shared wire contract; `languages/` = language packages.
- **Redis** for the job queue + stdin pub/sub channel. **soketi** for real-time output (worker triggers directly via the Pusher protocol).
- **Auth/config by env vars, not endpoints**: `EXECUTOR_API_TOKEN` (constant-time bearer in Hono middleware), `REDIS_URL`, `SOKETI_HOST/PORT/USE_TLS/APP_ID/APP_KEY/APP_SECRET`. soketi creds read by the worker (to trigger) and the API (if it signs channel auth).
- **Stateless** API + workers → N replicas. Capacity counted in concurrent live sandboxes (a session holds a slot until it expires). Autoscaling is by **queue depth**, where the **scaling unit is the worker node** (each hosts N concurrent sandboxes) and the worker fleet can scale to zero on an empty queue — not a microVM per execution.
- **No Docker-in-Docker.** The worker **always launches the sandbox internally** via the local container runtime (mounted socket) — that model never changes. **Runner behind an interface** so only the backend swaps: `DockerSocketRunner` (dev + prod default) → the same runner with `--runtime=runsc` (**gVisor**, the primary hardening upgrade, still internal) → `FlyMachinesRunner` (microVM-per-execution via the Fly API) as a **v2** option with seconds-of-latency + unproven interactive-streaming trade-offs.
- **Extensibility**: add a language = folder + pre-built image, zero core changes; no languages hardcoded.
- **Open source**: MIT, self-hostable, `.env.example`, README quickstart + add-a-language guide.
- **Reliability**: stdin via Redis pub/sub for MVP; Redis Streams + `XREAD BLOCK` documented as the guaranteed-delivery upgrade.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Hono/TS for the API, Go for the worker | Thin trusted gateway in TS; Go for container/process orchestration — right tool per component | — Pending |
| Shared wire contract via **JSON Schema codegen** (TS types + Go structs) | The poliglota seam is the fragile point; one source of truth + CI drift check beats a prose "canonical doc" | — Pending |
| Codegen tools: `json-schema-to-typescript` + `json-schema-to-zod` (validators) + `omissis/go-jsonschema` | Generated validators (not hand-written) keep Hono validation in lockstep with the schema; maintained Go generator | — Pending |
| **Prod Redis must speak native protocol (pub/sub + blocking ops)** — Upstash is NOT viable for the worker | Research verdict: Upstash has no TCP blocking SUBSCRIBE/BLPOP; the API is Upstash-safe but the worker isn't. Recommend a single native managed Redis/Valkey shared by both. ⚠️ Spec named Upstash — flagged for override | ⚠️ Revisit |
| Runner behind an interface (`Runner`) | Swap the sandbox backend (Docker socket / gVisor runtime / Fly) without changing the "worker launches it internally" model | — Pending |
| Core sandbox model = worker launches it **internally** via `Runner`; prod = long-lived **worker nodes** scaled to/from zero by queue depth, with gVisor (`--runtime=runsc`) for extra isolation | Lowest latency and the only proven path for interactive stdin (local pipes); the scaling unit is the worker node (hosts N sandboxes), not the execution — like Piston/Judge0 | — Pending |
| `FlyMachinesRunner` (microVM-per-execution via the Fly Machines API) deferred to **v2** | Gives per-exec Firecracker isolation but costs seconds of create latency and has unproven interactive streaming; unnecessary when gVisor on a worker node already gives strong isolation | ⚠️ v2 |
| Channel auth is the upstream app's job (optional non-core helper) | Keeps the core minimal; documented HMAC pattern; helper behind the token is optional | — Pending |
| Abuse tests built EARLY (after Python E2E, before fan-out) | Per user; safety guarantees are the verification backbone and must gate language additions | — Pending |
| Hono on Node (`@hono/node-server`), not Bun | Maximizes self-hostability for an OSS project | — Pending |
| Redis client: `ioredis` (TS), `go-redis` (Go) | ioredis stable; node-redis v6 RESP3-default churn avoided | — Pending |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd:complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-06-09 — started milestone v1.2 (Input Files & Content-Addressed Blobs): inline multi-file input (binary via base64 + subdirs) and a content-addressed sha256 blob store with presigned upload, Redis-tracked TTL/lease, and worker streaming pull. v1.1 (Density / ZygoteRunner) shipped.*
