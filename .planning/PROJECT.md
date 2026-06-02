# code-runner

## What This Is

An **open-source (MIT), self-hostable** remote code execution service (Piston-style): it receives code, runs it in an isolated hardened sandbox with **live interactive stdin**, and streams output in real time. It is dockerized and horizontally scalable.

It is an **internal service** — never exposed to the internet directly. In front of it sits the user's own backend (any stack) that consumes it by authenticating with a bearer token. Real-time output reaches the browser via **soketi** (Pusher-compatible): the worker publishes output events; soketi is **output-only** toward the client. All trusted input (code, stdin, control) enters through our API; nothing trusted enters via soketi.

It is a **polyglot monorepo by design** — each component uses the right tool: a thin Hono/TypeScript HTTP gateway, a Go worker that orchestrates sandboxes and keeps sessions alive, manifest-driven language packages, and a shared wire contract.

## Core Value

Run untrusted code in a hardened, resource-bounded sandbox with a live interactive stdin session and reliable real-time output — without ever leaking a container, a subscription, or a session slot — and make it trivially self-hostable and extensible (add a language = add a folder + an image).

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
- **Stateless** API + workers → N replicas. Capacity counted in concurrent live sandboxes (a session holds a slot until it expires). Design for autoscaling by queue depth + scale-to-zero.
- **No Docker-in-Docker.** Worker → host runtime via mounted socket (dev). **Runner behind an interface** so the sandbox backend can swap: `DockerSocketRunner` (dev) → `gVisorRunner` (k8s `RuntimeClass=gvisor`) → `FlyMachinesRunner` (Firecracker) without touching logic.
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
| Runner behind an interface (`Runner`) | Swap Docker → gVisor → Firecracker without core changes | — Pending |
| `FlyMachinesRunner` = worker calls Fly Machines API to create one ephemeral Firecracker Machine per execution | Cleanest mapping to the Runner interface; ship Docker runner first, benchmark interactive streaming later | — Pending |
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
*Last updated: 2026-06-02 after spec revision (Hono API, polyglot monorepo, shared contract, OSS + deployment targets)*
