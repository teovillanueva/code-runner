---
phase: 09-artifacts-pullable-run-output
plan: 05
subsystem: api
tags: [api, hono, redis, pull-output, r9]
requires:
  - "09-01: contract keys.jobOutput + RunResult type"
  - "09-04: worker persists RunResult at job:<id>:output (jobstore.ReadRunResult / write side)"
provides:
  - "GET /v1/jobs/:id/output — bearer-authenticated, Redis-only pull of the persisted RunResult (R9)"
affects:
  - "edalef backend consumers: can now fetch full output + artifacts without subscribing to soketi"
tech-stack:
  added: []
  patterns:
    - "Clone the GET /v1/jobs/:id status route against a different contract key (single-absence-check 404, central /v1/* bearer, Redis-GET-only)"
key-files:
  created:
    - "apps/api/test/output-route.test.ts"
  modified:
    - "apps/api/src/routes/jobs.ts"
decisions:
  - "Route returns 404 (not 403) for unknown/not-collected/expired — absence is indistinguishable so callers can't probe which job IDs exist (D-09 / T-09-15)"
  - "No per-handler auth — central app.use('/v1/*', bearerAuth) already covers the new route (R9 401, T-09-16)"
  - "Handler does NOT validate the stored value against RunResultSchema — it returns whatever JSON the worker persisted, mirroring the status route exactly (thin gateway)"
metrics:
  duration: "~7 min"
  completed: "2026-06-03"
  tasks: 1
  files: 2
---

# Phase 9 Plan 5: Pullable Run Output Route Summary

Bearer-authenticated, Redis-only `GET /v1/jobs/:id/output` that returns the persisted `RunResult` (200), 404 for any absent key (unknown / not-collected / expired), 401 without a valid bearer, and 500 on malformed stored JSON — never touching the worker (R9, API-11).

## What Was Built

A second handler inside the existing `registerJobsRoutes(app)` in `apps/api/src/routes/jobs.ts`, byte-for-byte cloned from the `GET /v1/jobs/:id` status route but reading `keys.jobOutput(jobId)` (`job:<id>:output`) instead of `keys.jobStatus(jobId)`:

- **200**: `redis.get(keys.jobOutput(jobId))` present → `JSON.parse` → `c.json(runResult, 200)`.
- **404**: key absent → `{ error: "Job output not found: <id>" }`. A single absence check covers unknown id, non-collected job, and a job past its result TTL — all collapse to "absent", so a caller cannot probe which IDs exist (T-09-15).
- **401**: enforced by the existing central `app.use("/v1/*", bearerAuth)` middleware — no per-handler auth code (T-09-16, R9).
- **500**: malformed (non-JSON) stored value → `{ error: "Internal error: malformed job output" }`.
- **Redis-only**: the handler calls `redis.get` only and never reaches the worker (API-11 invariant preserved, T-09-17).

No new route file — the handler lives in `registerJobsRoutes`. No contract change (consumes `keys.jobOutput` + `RunResult` from Wave 1).

## How It Was Verified

TDD RED→GREEN:

- **RED** (`7b09abc`): `apps/api/test/output-route.test.ts` seeds `job:<id>:output` in a live test Redis via ioredis and drives the route through `app.request` with a `Bearer` header (mirrors `execute-collect-output.test.ts`). With the route absent, the 200/404/500 cases hit Hono's default plaintext 404 and failed as expected; the 401 cases passed (central middleware fires before route resolution).
- **GREEN** (`de5b6bf`): handler added; `output-route.test.ts` → 5/5 pass.
- **Full API suite**: `vitest run` → **69/69 pass** (10 files) with a live Redis on `redis://localhost:6380` (the vitest default `TEST_REDIS_URL`).

Test environment note: this worktree had no Redis on the test port. An ephemeral `redis:7-alpine` was started on `6380` for the run and stopped afterward (no repo artifact). The route/status tests need a live Redis exactly as the existing `execute-collect-output.test.ts` does; with Redis down, body-shape assertions skip but 401 still asserts.

## Deviations from Plan

### Adjustments

**1. [Rule 3 - Blocking] Corrected the test filter package name**
- **Found during:** Task 1 verification.
- **Issue:** The plan's `<verify>` used `pnpm --filter @teovilla/code-runner-api test`, but the API package is named `@code-runner/api` (`apps/api/package.json`). The plan filter matched no project.
- **Fix:** Ran `pnpm --filter @code-runner/api test` / `pnpm exec vitest run` directly. No source change.
- **Files modified:** none.

No other deviations — the route was cloned exactly as specified.

## Deferred Issues

**Pre-existing `tsc` error in `apps/api/src/routes/execute.ts:44` (out of scope).**
`pnpm exec tsc --noEmit` reports `TS2739`: the literal `limits` object in `execute.ts` is missing `maxArtifacts`/`maxArtifactBytes`, which were added to the `Limits` contract type in an earlier wave. This is unrelated to the R9 output route — `jobs.ts` and `output-route.test.ts` are both `tsc`-clean, and the vitest suite (esbuild, no typecheck) passes 69/69. Logged to `.planning/phases/09-artifacts-pullable-run-output/deferred-items.md`; should be fixed by whichever plan owns `execute.ts` limit resolution. Not fixed here per the scope boundary.

## Known Stubs

None — the route returns live persisted data from Redis.

## Threat Surface

Covered by the plan's threat register; no new surface introduced beyond the planned route:
- T-09-15 (info disclosure): 404-not-403 absence indistinguishability — implemented via single absence check.
- T-09-16 (spoofing): central `/v1/*` bearer — asserted 401 (no token + invalid token).
- T-09-17 (EoP / coupling): Redis-GET-only — source confirms no worker call.

## Self-Check: PASSED

- `apps/api/src/routes/jobs.ts` — FOUND (contains `jobOutput`, path `/v1/jobs/:id/output`)
- `apps/api/test/output-route.test.ts` — FOUND
- commit `7b09abc` (RED test) — in git log
- commit `de5b6bf` (GREEN feat) — in git log
- TDD gate: `test(...)` then `feat(...)` commit present — compliant.
