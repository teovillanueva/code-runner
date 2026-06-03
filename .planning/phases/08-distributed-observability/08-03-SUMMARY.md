---
phase: 08-distributed-observability
plan: 03
subsystem: observability
tags: [opentelemetry, nodesdk, otel-js, w3c-traceparent, pino, hono, asynclocalstorage, tdd, no-op-gate]

# Dependency graph
requires:
  - phase: 08-distributed-observability
    plan: 01
    provides: Optional W3C traceparent/tracestate carrier on JobSpec (TS+zod+Go)
provides:
  - Env-gated OTel NodeSDK bootstrap for apps/api loaded via `node --import` before ioredis (ESM load-order fix)
  - Manual `execute` span at POST /v1/execute that injects a W3C traceparent into the JobSpec before the LPUSH
  - pino structured logging with AsyncLocalStorage job_id correlation; console.* removed from API request/job paths
affects: [08-02-worker-trace-extract, 08-05-end-to-end-trace-verification, distributed-observability]

# Tech tracking
tech-stack:
  added:
    - "@opentelemetry/api 1.9.1, api-logs 0.218.0, sdk-node 0.218.0"
    - "@opentelemetry/exporter-{trace,metrics,logs}-otlp-proto 0.218.0"
    - "@opentelemetry/instrumentation-{http,ioredis 0.66.0,pino 0.64.0}"
    - "@hono/otel 1.1.2, pino 10.3.1"
    - "@opentelemetry/context-async-hooks 2.7.1 (devDependency — test context manager)"
  patterns:
    - "Telemetry bootstrap lives in a SEPARATE telemetry.ts loaded via `node --import ./src/telemetry.ts` BEFORE server.ts so the ioredis ESM instrumentation hook registers before ioredis is imported"
    - "NodeSDK construction is gated on OTEL_EXPORTER_OTLP_ENDPOINT — true no-op when unset (no exporter, no localhost:4318 connection)"
    - "job_id correlation via an exported AsyncLocalStorage + pino mixin; trace_id/span_id are injected by instrumentation-pino, keeping logger.ts OTel-decoupled"
    - "execute span wraps spec-build+enqueue; propagation.inject writes spec.traceparent BEFORE the pipeline LPUSH (additive, no-op when no SDK)"

key-files:
  created:
    - apps/api/src/telemetry.ts
    - apps/api/src/logger.ts
    - apps/api/test/telemetry.test.ts
    - .planning/phases/08-distributed-observability/08-03-SUMMARY.md
    - .planning/phases/08-distributed-observability/deferred-items.md
  modified:
    - apps/api/src/routes/execute.ts
    - apps/api/src/app.ts
    - apps/api/src/server.ts
    - apps/api/test/execute.test.ts
    - apps/api/package.json
    - apps/api/Dockerfile
    - pnpm-lock.yaml

key-decisions:
  - "D-01: curated NodeSDK instrumentations (HTTP + ioredis + pino) + @hono/otel middleware + explicit manual execute span — NOT the auto-instrumentations kitchen-sink bundle"
  - "D-02: pino replaces console.* in API request/job paths; instrumentation-pino auto-injects trace_id/span_id"
  - "D-03: stdout JSON always + OTLP logs when configured (logRecordProcessors); job_id flows via AsyncLocalStorage"
  - "D-04: no second/admin HTTP listener (OBS-05 dropped); SDK bootstrap at startup only"

patterns-established:
  - "Verified A1 (@hono/otel export = httpInstrumentationMiddleware) and A2 (PeriodicExportingMetricReader/BatchLogRecordProcessor are re-exported from sdk-node's `.metrics`/`.logs` NAMESPACES, not `/metrics` `/logs` subpaths) against the installed 0.218.0 surface"
  - "Test seam createLogger(dest) shares production loggerOptions() so stdout assertions are deterministic without monkey-patching fd 1"

requirements-completed: [OBS-01, OBS-04, OBS-07]

# Metrics
duration: 33min
completed: 2026-06-03
---

# Phase 8 Plan 03: API Trace Inject + pino Migration Summary

**The TS half of the one connected trace: an env-gated OTel NodeSDK loaded via `node --import` (ioredis load-order fix), an `execute` span at POST /v1/execute that injects a W3C traceparent into the JobSpec before the LPUSH, and the API request/job paths migrated off console.* to pino with job_id + trace correlation.**

## Performance

- **Duration:** ~33 min
- **Completed:** 2026-06-03
- **Tasks:** 2 (both TDD: RED → GREEN)
- **Files:** 5 created, 7 modified

## Accomplishments

- `telemetry.ts` constructs the NodeSDK **only** when `OTEL_EXPORTER_OTLP_ENDPOINT` is set (OBS-01). With it unset the module is a true no-op — no exporter, no connection attempt to localhost:4318 — proven by `telemetry.test.ts`'s `isTelemetryStarted() === false` assertion in the unset vitest env.
- The SDK is loaded via `node --import ./src/telemetry.ts` ahead of `server.ts` in the `dev`/`start` scripts AND the Dockerfile `CMD`, so the ioredis ESM instrumentation hook registers **before** `redis.ts` imports ioredis (RESEARCH Pitfall 1). Verified `node --import` preserves load order under both `--experimental-strip-types` and `tsx`.
- `logger.ts` exports a pino singleton plus an `AsyncLocalStorage<{jobId}>` (`jobContext`); a pino `mixin` reads `jobContext.getStore()?.jobId` so every log within a job carries `job_id`. trace_id/span_id come from `instrumentation-pino`, keeping the logger OTel-decoupled.
- `POST /v1/execute` now wraps spec-build + enqueue in `tracer.startActiveSpan("execute", ...)` inside `jobContext.run({ jobId }, ...)`, calls `propagation.inject(context.active(), carrier)`, and sets `spec.traceparent` (+ `tracestate`) **before** the pipeline `LPUSH`. The execute test asserts the injected traceparent's trace_id equals the recorded `execute` span's trace_id (the cross-language seam 08-02 extracts).
- `console.log`/`console.error` removed from `server.ts`, `app.ts` onError, and `routes/execute.ts`; `@hono/otel` `httpInstrumentationMiddleware()` registered for the inbound request span.
- Full API suite green: **59/59** with Redis reachable on 127.0.0.1:6379 (`pnpm --filter @code-runner/api test`).

## Task Commits

1. **Task 1: Env-gated NodeSDK bootstrap + pino logger + load-order fix** — `8b180c5` (feat)
2. **Task 2: execute span + traceparent injection; migrate paths to pino** — `5498ddd` (feat)

Each task followed RED→GREEN (failing test committed conceptually within the task, then made to pass before commit).

## Files Created/Modified

- `apps/api/src/telemetry.ts` (new) — Env-gated NodeSDK: OTLP proto trace exporter, `PeriodicExportingMetricReader(OTLPMetricExporter)`, `BatchLogRecordProcessor(OTLPLogExporter)`, instrumentations `[Http, IORedis, Pino]`, SIGTERM shutdown, `isTelemetryStarted()` gate introspection.
- `apps/api/src/logger.ts` (new) — pino singleton (`getLogger`), exported `jobContext` AsyncLocalStorage + job_id mixin (`loggerOptions`), `createLogger(dest)` test seam.
- `apps/api/src/routes/execute.ts` — `execute` span + `propagation.inject` into `spec.traceparent` before LPUSH; handler body in `jobContext.run`.
- `apps/api/src/app.ts` — `@hono/otel` middleware; onError → `getLogger().error`.
- `apps/api/src/server.ts` — boot/listen/fatal logs → pino (SDK NOT imported here).
- `apps/api/test/telemetry.test.ts` (new) — no-op gate + job_id correlation + no-secret.
- `apps/api/test/execute.test.ts` — extended with in-memory span exporter + W3C propagator + ALS context manager; traceparent↔span trace_id assertion.
- `apps/api/package.json` — OTel/pino deps at RESEARCH-pinned versions; `dev`/`start` scripts gain `--import ./src/telemetry.ts`; `context-async-hooks` devDep.
- `apps/api/Dockerfile` — `CMD` loads telemetry via `--import` before `src/server.ts`.
- `pnpm-lock.yaml` — regenerated for the new deps.

## Decisions Made

Implemented D-01..D-04 exactly as specified (curated instrumentations + manual execute span; pino replacing console.*; stdout-always + OTLP-when-configured; no admin port). Resolved the two flagged assumptions against the installed 0.218.0 surface:

- **A1 → confirmed:** `@hono/otel@1.1.2` exports `httpInstrumentationMiddleware`.
- **A2 → corrected:** `PeriodicExportingMetricReader` and `BatchLogRecordProcessor` are NOT at `@opentelemetry/sdk-node/metrics` / `/logs` subpaths (those `ERR_MODULE_NOT_FOUND`). They are re-exported from the sdk-node root via the `metrics` and `logs` **namespaces** — imported as `import { metrics as sdkMetrics, logs as sdkLogs } from "@opentelemetry/sdk-node"` then `sdkMetrics.PeriodicExportingMetricReader` / `sdkLogs.BatchLogRecordProcessor`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Worktree zod resolution escaped to a stray $HOME/node_modules/zod@3.23.8**
- **Found during:** Task 1 (running the `typecheck` acceptance criterion).
- **Issue:** In the deeply-nested git worktree, the contract's bare `import { z } from "zod"` (consumed as raw TS) resolved — via Node's upward `node_modules` walk — to a stray global `/Users/teovillanueva/node_modules/zod@3.23.8` instead of the project's `zod@3.25.76`. zod 3.23.8 lacks the `~standard`/`~validate` Standard-Schema members that `@hono/zod-validator@0.8.0`'s `zod/v3` `ZodType` requires, producing 8 phantom `TS2769` errors in `execute.ts`/`control.ts` (files the task hadn't even modified). The main checkout passed only because its shallower path hit the project zod first.
- **Fix:** Ran a full `pnpm install` in the worktree so the contract package's own `node_modules/zod` symlink materialized, shadowing `$HOME`. Resolution then correctly pinned `zod@3.25.76`; typecheck went 8 → 0 errors. No source code changed; environmental/resolution fix only.
- **Verification:** `node -e "require.resolve('zod')"` from `packages/contract` resolves to the worktree's `zod@3.25.76`; `tsc --noEmit` clean.
- **Committed in:** `8b180c5` (the resulting `pnpm-lock.yaml` is part of the Task 1 commit).

**2. [Rule 3 - Blocking] A2 import-path correction (sdk-node namespaces)**
- **Found during:** Task 1 implementation.
- **Issue:** RESEARCH Pattern 1 imported `PeriodicExportingMetricReader`/`BatchLogRecordProcessor` from `@opentelemetry/sdk-node/metrics` and `/logs` subpaths; those subpaths do not exist in 0.218.0 (`ERR_MODULE_NOT_FOUND`).
- **Fix:** Imported the sdk-node `metrics`/`logs` namespaces from the package root and referenced the classes off them (the documented A2-verification path).
- **Files modified:** `apps/api/src/telemetry.ts`.
- **Committed in:** `8b180c5`.

---

**Total deviations:** 2 auto-fixed (both Rule 3 — blocking tooling/resolution). No architectural changes; no behavior/auth/status-code changes to `/v1/*` (telemetry strictly additive).

## Out-of-Scope (Deferred)

Logged to `.planning/phases/08-distributed-observability/deferred-items.md`:
- Two remaining `console.error` calls in `apps/api/src/redis.ts:19` (ioredis connection handler) and `apps/api/src/channelAuth.ts:75` (optional Pusher helper). Not in this plan's "request/job paths" scope; a future plan can migrate them.

## Issues Encountered

- Briefly ran `git stash`/`git stash pop` while probing the typecheck cause — immediately reverted with `git stash pop`; `git stash list` confirmed empty and the working tree intact (no cross-worktree contamination). Avoided thereafter per the worktree stash prohibition.
- `pnpm --filter @code-runner/api test` with `REDIS_URL` unset falls back to vitest.config's `redis://localhost:6380` default and the 14 Redis-dependent tests time out (nothing listens on 6380). With `REDIS_URL=redis://127.0.0.1:6379` (the running instance, per the run-environment note) all 59 pass. The 6380 default is the project's deliberate compose test-stack convention and was intentionally left unchanged (out of scope; changing it could break CI/compose).

## Known Stubs

None. `telemetry.ts` is intentionally a no-op when OTEL is unset (the OBS-01 contract), not a stub — the gate is the designed behavior and is asserted by test.

## Threat Flags

None. No new network surface introduced beyond the documented outbound-only OTLP egress (already in the plan's threat register). T-08-07 mitigated: pino logs allow-listed fields only (`job_id`; trace fields via instrumentation); onError logs `err.message` only; no secret or user code/stdin logged (asserted).

## User Setup Required

None for default operation (no-op without OTEL env). To enable telemetry: set `OTEL_EXPORTER_OTLP_ENDPOINT` (+ optionally `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`) and point at an OTLP/HTTP collector on :4318.

## Next Phase Readiness

- The API now emits one `execute` span carrying a W3C traceparent into the JobSpec; 08-02 (worker) extracts and links to it. 08-05 verifies the shared trace_id end-to-end.
- No blockers. STATE.md/ROADMAP.md intentionally NOT modified (orchestrator owns those after the wave).

## Self-Check: PASSED

All created files exist on disk (telemetry.ts, logger.ts, telemetry.test.ts, 08-03-SUMMARY.md, deferred-items.md) and all three commits (8b180c5, 5498ddd, 892c17d) are present in branch history. Working tree clean.
