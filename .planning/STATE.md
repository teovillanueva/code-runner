---
gsd_state_version: 1.0
milestone: v1.1
milestone_name: Density / ZygoteRunner
status: shipped
last_updated: "2026-06-09T00:00:00.000Z"
last_activity: 2026-06-09
progress:
  total_phases: 5
  completed_phases: 5
  total_plans: 0
  completed_plans: 0
  percent: 100
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-06-02)

**Core value:** Run untrusted code in a hardened, resource-bounded sandbox with a live interactive stdin session and reliable real-time output — without ever leaking a container, a subscription, or a session slot — and make it trivially self-hostable and extensible.
**Current focus:** Milestone v1.1 (Density / ZygoteRunner) — SHIPPED. All 5 phases (10–14) implemented, tested, documented, on `main`; new worker image (sha-2ddd3b2) deployed to Fly prod with the zygote tier enabled. See `.planning/decisions/ZYGOTE-SHIPPED.md` for the full status + the one remaining ops follow-up (golden-snapshot re-bake).

## Current Position

Phase: v1.1 SHIPPED (Phases 10–14 complete, executed directly with gsd-executor subagents per phase, not the per-phase PLAN.md flow)
Plan: —
Status: On main + deployed to Fly prod (worker sha-2ddd3b2, zygote enabled). Python = zygote tier; R/Rust/SQLite = Docker tier; Docker fallback guarantees all 4 work even if a pool fails.
Last activity: 2026-06-09 — deployed worker to prod, refreshed agent-baked python image on all 3 pool nodes, confirmed cgroup delegation works on Fly; restored scale-to-zero

Milestone v1.1 phase map:
- Phase 10: Pre-Import Contract — `preimport` manifest field + contract regen + Python/R sets (PRE-01..04)
- Phase 11: Zygote Agents & Per-Child Hardening — credential-free Python/R agents + double-fork hardening (AGENT-*, ZHARD-*)
- Phase 12: Go ZygoteRunner & Warm Pool — Runner-interface ZygoteRunner + warm parent pool (ZYG-*, POOL-*)
- Phase 13: Tiered Routing, Deploy & Gating — TieredRunner, all 4 langs E2E, Fly privileged pool, off→Docker default (TIER-*, ZDEP-*)
- Phase 14: Zygote Safety, Density & Pool Observability — abuse parity + isolation + density + no-leak gate + pool metrics (ZTEST-*, ZOBS-*)

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

Last session: 2026-06-08
Stopped at: v1.1 roadmap created — Phases 10–14 appended to ROADMAP.md, 37 v1.1 requirements mapped 100% in REQUIREMENTS.md (V2-06/V2-07 deferred). v1.0 phases 1–9 untouched.
Resume file: Next — `/gsd:plan-phase 10`
