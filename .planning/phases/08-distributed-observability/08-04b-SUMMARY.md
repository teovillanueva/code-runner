---
phase: 08-distributed-observability
plan: 04b
subsystem: observability
tags: [opentelemetry, otel-js, metrics, counter, pino, hono, low-cardinality, tdd]

# Dependency graph
requires:
  - phase: 08-distributed-observability
    plan: 02
    provides: Worker trace-extract + logging conventions (cross-lang parity for code_runner.* namespace)
  - phase: 08-distributed-observability
    plan: 03
    provides: Env-gated OTel NodeSDK (PeriodicExportingMetricReader + OTLP metric exporter) and the pino getLogger()/jobContext logger this plan reuses
provides:
  - API meter "code-runner-api" with admission/ratelimit rejection counters (code_runner.admission.rejected, code_runner.ratelimit.rejected)
  - admission-429 + ratelimit-429 increment low-cardinality counters (reason attr only; never job_id)
  - Full console.*→pino migration across apps/api/src (redis.ts + channelAuth.ts last two sites done)
affects: [08-05-end-to-end-trace-verification, distributed-observability]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "API rejection counters live in a small named-export metrics.ts (mirrors admission.ts), bound to the global API MeterProvider via metrics.getMeter — true no-op when telemetry.ts installed no provider (OBS-01)"
    - "Counter increment co-located with the 429 helper/branch that owns it: admissionRejections.add(1) inside admission.ts admissionError() (429-only path) keeps execute.ts (08-03) untouched; ratelimitRejections.add(1,{reason}) at each ratelimit.ts 429 branch"
    - "Metric attributes are a fixed low-cardinality enum (reason: frame_rate|byte_cap); job_id is asserted-absent on every data point (T-08-10b)"
    - "Metric assertions via the metrics namespace re-exported from @opentelemetry/sdk-node (InMemoryMetricExporter + PeriodicExportingMetricReader + MeterProvider, forceFlush-driven) — same A2 namespace path 08-03 verified, since sdk-metrics is not a top-level dep"

key-files:
  created:
    - apps/api/src/metrics.ts
    - apps/api/test/metrics.test.ts
    - .planning/phases/08-distributed-observability/08-04b-SUMMARY.md
  modified:
    - apps/api/src/admission.ts
    - apps/api/src/ratelimit.ts
    - apps/api/src/redis.ts
    - apps/api/src/channelAuth.ts

key-decisions:
  - "D-05: OTel counters for admission-429 + ratelimit-429 rejections (instrument-type mapping = Counter)"
  - "D-06: code_runner.* dotted namespace + {request} unit on both API rejection counters (code_runner.admission.rejected / code_runner.ratelimit.rejected)"
  - "D-02: console.*→pino migration finished for the remaining API paths — redis.ts and channelAuth.ts now use getLogger(); no console.* remains anywhere in apps/api/src"

patterns-established:
  - "admission counter placed in admissionError() (the 429-only response builder) rather than atCapacity() (the boolean check called on every request) — increments exactly once per rejection without a separate branch and without editing execute.ts"
  - "channelAuth.ts/redis.ts pino logs carry only err.message (string-coerced), never the Pusher client object or connection string (T-08-12 secret allow-list)"

requirements-completed: [OBS-06, OBS-07]

# Metrics
duration: 9min
completed: 2026-06-03
---

# Phase 8 Plan 04b: API Metrics Breadth + pino Finish Summary

**The TS half of OBS-06/07 operational breadth: a new `code-runner-api` meter emitting `code_runner.admission.rejected` and `code_runner.ratelimit.rejected{reason}` low-cardinality counters wired into the existing 429 paths, plus the last two `console.error` sites (redis.ts, channelAuth.ts) migrated to pino — so no `console.*` remains anywhere in `apps/api/src`.**

## Performance

- **Duration:** ~9 min
- **Completed:** 2026-06-03
- **Tasks:** 1 (TDD: RED → GREEN)
- **Files:** 3 created, 4 modified

## Accomplishments

- `apps/api/src/metrics.ts` (new) — module-level `meter = metrics.getMeter("code-runner-api")` and two exported counters: `admissionRejections` (`code_runner.admission.rejected`, unit `{request}`) and `ratelimitRejections` (`code_runner.ratelimit.rejected`, unit `{request}`). Reads from the global API MeterProvider, so it is a true no-op when `telemetry.ts` installed no provider (OBS-01 inherited from 08-03).
- `admission.ts` — `admissionRejections.add(1)` placed **inside `admissionError()`**, the helper invoked only on the 429 admission-rejection path. This increments exactly once per rejected request and keeps `execute.ts` (08-03's file) **completely untouched** — clean file ownership across the parallel wave.
- `ratelimit.ts` — `ratelimitRejections.add(1, { reason })` at both 429 branches: `reason: "frame_rate"` (stdin frame-rate window) and `reason: "byte_cap"` (pending-byte cap). Only the low-cardinality `reason` enum is attached; never `job_id`.
- `redis.ts` + `channelAuth.ts` — the two remaining `console.error` sites (flagged in `deferred-items.md` from 08-03) migrated to `getLogger().error({ err: <message> }, ...)`. Both log only `err.message` (string-coerced) — never the connection string, the Pusher client, or the `SOKETI_APP_SECRET` it holds (T-08-12). `grep -rn 'console\.\(log\|error\)' apps/api/src/` now returns **zero matches**.
- `apps/api/test/metrics.test.ts` (new) — installs an `InMemoryMetricExporter`-backed `MeterProvider` (via the `@opentelemetry/sdk-node` `metrics` namespace, the A2 path) as the global API provider, exercises both counters, `forceFlush`es, and asserts: admission counter increments by 2 with unit `{request}` and no attributes; ratelimit counter aggregates `frame_rate`→2 / `byte_cap`→1 and carries **only** the `reason` key (asserts `Object.keys === ["reason"]`, i.e. no `job_id`) — the T-08-10b cardinality guard.
- Full API suite green: **61/61** (was 59 — +2 new metric tests) with Redis on `127.0.0.1:6379`.

## Task Commits

1. **Task 1: API rejection counters + finish console.*→pino migration** — `d4d2e37` (feat)

RED→GREEN: the metrics test was written first and failed with `Failed to load url ../src/metrics.ts` (module absent), then made to pass by creating `metrics.ts` and wiring the counters.

## Files Created/Modified

- `apps/api/src/metrics.ts` (new) — meter + `admissionRejections`/`ratelimitRejections` counters; `RatelimitReason` type (`frame_rate`|`byte_cap`).
- `apps/api/test/metrics.test.ts` (new) — InMemory metric assertions for both counters + cardinality guard.
- `apps/api/src/admission.ts` — import `admissionRejections`; `.add(1)` inside `admissionError()`.
- `apps/api/src/ratelimit.ts` — import `ratelimitRejections`; `.add(1,{reason})` at the frame-rate and byte-cap 429 branches.
- `apps/api/src/redis.ts` — `console.error` → `getLogger().error({err: err.message}, "redis connection error")`.
- `apps/api/src/channelAuth.ts` — `console.error` → `getLogger().error({err: <message>}, "channel-auth pusher error")`.

## Decisions Made

Implemented D-02, D-05, D-06 exactly as specified. No new dependencies were needed — the OTel JS API (`@opentelemetry/api` `metrics`) and the metric exporter/reader (`@opentelemetry/sdk-node` `metrics` namespace) were already present from 08-03, so `package.json`/`pnpm-lock.yaml` are unchanged on the source side (lockfile only regenerated identically by the worktree `pnpm install`, no diff).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing critical functionality] channelAuth.ts included in the console.*→pino migration**
- **Found during:** Task 1 (acceptance criterion `grep -rn 'console\.\(log\|error\)' apps/api/src/` must return NO matches).
- **Issue:** The task name lists `control.ts / jobs.ts / redis.ts` as the migration targets, but the plan's behavior + acceptance criterion require **no `console.*` anywhere in `apps/api/src`**. `control.ts` and `jobs.ts` already had zero `console.*`; the only two live sites were `redis.ts:19` and `channelAuth.ts:75` (both flagged in `deferred-items.md`). Migrating only `redis.ts` would have left `channelAuth.ts` failing the "anywhere" criterion.
- **Fix:** Migrated `channelAuth.ts:75` to pino as well (logging only `err.message` per T-08-12), satisfying the zero-`console.*` acceptance criterion.
- **Files modified:** `apps/api/src/channelAuth.ts`.
- **Committed in:** `d4d2e37`.

---

**Total deviations:** 1 auto-fixed (Rule 2 — completing the migration to meet the plan's own acceptance criterion). No architectural changes; no behavior/status-code changes to any `/v1/*` route (telemetry + logging strictly additive).

## Out-of-Scope (Deferred)

None new. The two `deferred-items.md` entries from 08-03 (the `redis.ts` + `channelAuth.ts` `console.error` sites) are **resolved** by this plan — the API is now fully on pino.

## Known Stubs

None. `metrics.ts` emits real counters; when no MeterProvider is installed (OTEL unset) the API's default no-op meter records nothing — the designed OBS-01 no-op, not a stub.

## Threat Flags

None. No new network surface — metric export is the existing outbound-only OTLP push (08-03's `PeriodicExportingMetricReader`). T-08-10b mitigated: counters carry only the low-cardinality `reason` enum, asserted free of `job_id`. T-08-12 mitigated: `redis.ts`/`channelAuth.ts` pino logs emit only `err.message`; no secret/connection-string/Pusher-client logged.

## User Setup Required

None for default operation (no-op without OTEL env). With `OTEL_EXPORTER_OTLP_ENDPOINT` set, the two counters export over OTLP alongside 08-03's traces/logs.

## Next Phase Readiness

- The API now emits admission + ratelimit rejection counters over the same OTLP pipeline as its traces/logs; 08-05 can verify the metric stream end-to-end alongside the cross-language trace.
- File ownership stayed clean: this plan touched **only** `apps/api` files (metrics.ts/admission.ts/ratelimit.ts/redis.ts/channelAuth.ts/test) — no `execute.ts` (08-03) and no Go file (08-04 parallel agent). No blockers.
- STATE.md / ROADMAP.md intentionally NOT modified — the orchestrator owns those after the wave.

## Self-Check: PASSED

All created files exist on disk (`apps/api/src/metrics.ts`, `apps/api/test/metrics.test.ts`, this SUMMARY) and the task commit `d4d2e37` is present in branch history. `grep` confirms zero `console.*` in `apps/api/src`. Full suite 61/61 green.
