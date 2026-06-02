---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: verifying
stopped_at: "Completed 01-03-PLAN.md — worker boot + TS manifest loader. Phase 1 complete. Commits: 8789d4c, cb5e93b."
last_updated: "2026-06-02T23:03:11.039Z"
last_activity: 2026-06-02
progress:
  total_phases: 7
  completed_phases: 4
  total_plans: 14
  completed_plans: 14
  percent: 57
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-06-02)

**Core value:** Run untrusted code in a hardened, resource-bounded sandbox with a live interactive stdin session and reliable real-time output — without ever leaking a container, a subscription, or a session slot — and make it trivially self-hostable and extensible.
**Current focus:** Phase 1 — Foundation & Wire Contract

## Current Position

Phase: 1 of 7 (Foundation & Wire Contract)
Plan: 03 of 03 in current phase (Phase 1) — COMPLETE
Status: Phase complete — ready for verification
Last activity: 2026-06-02

Progress: [██████████] 100%

## Performance Metrics

**Velocity:**

- Total plans completed: 0
- Average duration: — min
- Total execution time: 0.0 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| - | - | - | - |

**Recent Trend:**

- Last 5 plans: —
- Trend: —

*Updated after each plan completion*
| Phase 02-sandbox-hardening-runner P01 | 25 | 3 tasks | 9 files |
| Phase 02-sandbox-hardening-runner P02 | 10m | 2 tasks | 3 files |
| Phase 02-sandbox-hardening-runner P03 | 8 | 3 tasks | 4 files |
| Phase 03-interactive-python-e2e P03 | 12 | 2 tasks | 2 files |
| Phase 03-interactive-python-e2e P02 | 21 | 3 tasks | 9 files |

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

### Pending Todos

[From .planning/todos/pending/ — ideas captured during sessions]

None yet.

### Blockers/Concerns

[Issues that affect future work]

- Phase 1 cannot proceed to implementation until the human approves the layout + contract + manifest schema (explicit gate).
- FlyMachinesRunner interactive-streaming fit + per-execution latency/cost is unvalidated (v2; benchmark before relying on it).

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| Runtime | gVisorRunner / FlyMachinesRunner behind the Runner interface | v2 | 2026-06-02 |
| Transport | Redis Streams + `XREAD BLOCK` guaranteed stdin delivery | v2 | 2026-06-02 |
| Packages | Offline crate/CRAN vendoring for Rust/R third-party libs | v2 | 2026-06-02 |
| Scale | `fly-autoscaler` queue-depth scaling + scale-to-zero impl | v2 | 2026-06-02 |

## Session Continuity

Last session: 2026-06-02T23:03:11.030Z
Stopped at: Completed 01-03-PLAN.md — worker boot + TS manifest loader. Phase 1 complete. Commits: 8789d4c, cb5e93b.
Resume file: None
