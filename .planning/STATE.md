---
gsd_state_version: 1.0
milestone: v1.2
milestone_name: Input Files & Content-Addressed Blobs
status: Phase 16 Plan 02 executed — CAS edge (API presign + Node SDK + infra + docs); BLOB-02/03/04/10/11/12 satisfied; all gates green
stopped_at: Phase 16 Plan 02 complete — /v1/blobs/check+finalize (API presigns PUT URLs, local crypto, Redis liveness via verbatim monotonic-touch Lua), SDK blobs.upload + transparent inline-vs-CAS routing, compose/.env blob wiring (BYO-bucket, unconfigured=>501), CAS docs. pnpm -r test / make contract-check / go build ./... all green. Prod blob bucket+env documented (NOT provisioned).
last_updated: "2026-06-09T12:10:00.000Z"
last_activity: 2026-06-09 — executed Phase 16 Plan 02 (CAS edge: API presign + SDK + infra + docs)
progress:
  total_phases: 16
  completed_phases: 11
  total_plans: 39
  completed_plans: 39
  percent: 69
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-06-02)

**Core value:** Run untrusted code in a hardened, resource-bounded sandbox with a live interactive stdin session and reliable real-time output — without ever leaking a container, a subscription, or a session slot — and make it trivially self-hostable and extensible.
**Current focus:** Milestone v1.2 (Input Files & Content-Addressed Blobs) — roadmap created. Two phases: **Phase 15 Multi-file Input (inline)** (FILES-01..08, zero new infra, independently shippable) → **Phase 16 Content-Addressed Blob Store (CAS)** (BLOB-01..12, needs a bucket). Phase 15 ships inline binary (base64) + subdir input under `/workspace` with worker-side path sanitization; Phase 16 adds a code-runner-owned sha256 CAS with presigned uploads (bytes client→store, no SSRF), `FileInput.ref`, Redis monotonic idle-TTL + per-run lease + grace-window GC, and transparent SDK inline-vs-CAS routing. v1.1 (Density / ZygoteRunner) SHIPPED.

## Current Position

Phase: 16 — Content-Addressed Blob Store (CAS) — IN PROGRESS (2/2 plans executed)
Plan: 16-01 (CAS core) + 16-02 (CAS edge) shipped
Status: Phase 16 Plan 02 executed — CAS edge (API presign + Node SDK + infra + docs); BLOB-02/03/04/10/11/12 satisfied; all gates green
Last activity: 2026-06-09 — executed Phase 16 Plan 02 (CAS edge: API presign + SDK + infra + docs)

## Performance Metrics

**Velocity:**

- Total plans completed: 36 (across 9 phases)
- Average duration: — min
- Total execution time: — hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 08-distributed-observability | 6 | - | - |
| 09-artifacts-pullable-run-output | 7 | - | - |

**Recent Trend:**

- Last 5 plans: —
- Trend: —

*Updated after each plan completion*
| Phase 02-sandbox-hardening-runner P01 | 25 | 3 tasks | 9 files |
| Phase 02-sandbox-hardening-runner P02 | 10m | 2 tasks | 3 files |
| Phase 02-sandbox-hardening-runner P03 | 8 | 3 tasks | 4 files |
| Phase 03-interactive-python-e2e P03 | 12 | 2 tasks | 2 files |
| Phase 03-interactive-python-e2e P02 | 21 | 3 tasks | 9 files |
| Phase 05-statelessness-scale P03 | 20m | 2 tasks | 6 files |
| Phase 05-statelessness-scale P01 | 20m | 3 tasks | 8 files |
| Phase 05 P02 | 12m | 2 tasks | 5 files |
| Phase 06-language-fan-out P01 | 7 | 2 tasks | 9 files |
| Phase 06-language-fan-out P04 | 18 | 2 tasks | 3 files |
| Phase 06-language-fan-out P03 | 15m | 2 tasks | 4 files |
| Phase 06-language-fan-out P02 | 14m | 2 tasks | 4 files |

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Phase 1]: Shared wire contract via JSON Schema codegen (TS types + zod validators + Go structs) with a CI drift check — one source of truth for the fragile polyglot seam.
- [Phase 1]: Phase 1 ends at a human approval gate — propose the monorepo layout + wire contract + manifest schema and wait for explicit OK before any implementation.
- [Phase 1/5]: Prod worker Redis must speak native pub/sub + blocking ops; Upstash is API-only (no TCP blocking SUBSCRIBE/BLPOP/XREAD BLOCK). Recommend a single native managed Redis/Valkey shared by API + worker.
- [Phase 3]: Hono on Node (`@hono/node-server`), `ioredis` (TS) + `go-redis` (Go); constant-time bearer auth.
- [Phase 4]: Abuse suite is built early (right after Python E2E) and gates the language fan-out; must run on Linux CI for real cgroup OOM/CPU behavior.
- [01-02]: runner.Result defined inline (not aliasing wire.ResultEvent) to keep Phase 2 decoupled from the wire schema for runner-internal fields.
- [01-02]: Config.RequiresNativeRedis() is a method (not a constant) to remain testable and support future URL-based validation.
- [01-03]: manifest.ts uses .ts extensions + allowImportingTsExtensions to enable direct Node.js execution via --experimental-strip-types without a build step.
- [01-03]: Zod schema infers string[] for run field; cast to Manifest is safe since zod validates min(1) constraint at runtime.
- [Phase ?]: CPUUsageFunc injection: internal/session has no docker/docker import; plan 02-03 injects the real cgroup reader via func(ctx)(int,error)
- [Phase ?]: tools/tools.go with //go:build tools pins Phase 2 deps before production code imports them, avoiding go.mod contention across wave-2 plans
- [Phase ?]: sync.Once + done channel teardown: terminate() writes Result then closes done; clock goroutines select on done to preserve single-receiver invariant
- [Phase ?]: maxEventBytes = 8 KB for soketi chunking
- [Phase ?]: triggerer interface enables unit testing without live soketi
- [Phase ?]: Stdout+Stderr share per-job seq (one ordered stream)
- [Phase ?]: DockerSocketRunner import cycle resolution
- [Phase ?]: Subscribe stdin/ctrl BEFORE publishing queued stage to eliminate start-handshake race window
- [Phase ?]: Admission gate placed after manifest resolution: invalid requests get 400 not 429; LLEN-based queue depth is the authoritative MVP gate for 429

### Roadmap Evolution

- v1.2 added (2026-06-09): **Input Files & Content-Addressed Blobs** — Phase 15 (inline multi-file input: binary via base64 + subdirs under `/workspace`, worker-side path sanitization, `MAX_FILES_BYTES` cap; zero new infra, independently shippable) and Phase 16 (content-addressed sha256 blob store code-runner owns: `/v1/blobs/check` presigned uploads with bytes going client→store directly, `FileInput.ref`, Redis monotonic idle-TTL + per-run lease + grace-window GC, worker streaming pull with sha256 re-verify, no-SSRF, BYO-bucket, minio inert in compose). FILES-01..08 → Phase 15, BLOB-01..12 → Phase 16; 20 requirements, 100% mapped.
- Phase 9 added (2026-06-03): Artifacts & Pullable Run Output — capture sandbox artifacts (images/files) into a persisted `RunResult` + `GET /v1/jobs/:id/output` pull endpoint, Python/R plot image parity, SDK helpers. Motivated by the edalef migration (server-side grading needs full output + plots without subscribing to soketi). Blocking `POST /v1/run` and env-gated webhooks explicitly deferred. Independent of Phase 8.

### Pending Todos

[From .planning/todos/pending/ — ideas captured during sessions]

None yet.

### Blockers/Concerns

[Issues that affect future work]

- FlyMachinesRunner interactive-streaming fit + per-execution latency/cost is unvalidated (v2; benchmark before relying on it).
- Phase 9 `09-HUMAN-UAT.md` was left `partial` (4 live-stack manual checks marked `pending`) when the planning was committed. The feature nonetheless shipped and is covered by committed automated tests (`internal/worker/worker_artifacts_test.go`, `internal/artifactstore/s3_test.go`, `packages/contract/test/artifact.test.ts`, `packages/code-runner-sdk-node/test/get-output.test.ts`, `apps/api/test/execute-collect-output.test.ts`, `apps/api/test/output-route.test.ts`) plus the S3 round-trip proven live during execution. Re-run the 4 manual UAT checks against a full compose stack if you want the formal sign-off.

### Quick Tasks Completed

| # | Description | Date | Commit | Directory |
|---|-------------|------|--------|-----------|
| 260603-u2w | Scientific stack (scipy/statsmodels/sklearn/seaborn/cvxopt/picos/swiglpk + lpSolve/ggplot2) + plot auto-capture to /workspace for python-3.12 & r-4.4 images | 2026-06-03 | 4c9e472 | [260603-u2w-customizar-imagenes-python-3-12-y-r-4-4-](./quick/260603-u2w-customizar-imagenes-python-3-12-y-r-4-4-/) |

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| Runtime | gVisorRunner / FlyMachinesRunner behind the Runner interface | v2 | 2026-06-02 |
| Transport | Redis Streams + `XREAD BLOCK` guaranteed stdin delivery | v2 | 2026-06-02 |
| Packages | Offline crate/CRAN vendoring for Rust/R third-party libs | v2 | 2026-06-02 |
| Scale | `fly-autoscaler` queue-depth scaling + scale-to-zero impl | v2 | 2026-06-02 |

## Session Continuity

Last session: 2026-06-09T09:25:05.454Z
Stopped at: v1.2 roadmap created — Phases 15–16 appended to ROADMAP.md (summary checklist + Phase Details — Milestone v1.2 + progress table + execution order), 20 v1.2 requirements (FILES-01..08, BLOB-01..12) mapped 100% in REQUIREMENTS.md. v1.0 phases 1–9 and v1.1 phases 10–14 untouched.
Resume file: None
