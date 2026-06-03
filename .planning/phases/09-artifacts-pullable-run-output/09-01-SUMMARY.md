---
phase: 09-artifacts-pullable-run-output
plan: 01
subsystem: contract
tags: [wire-contract, schema, codegen, redis-keys, soketi-events, api]
requires: []
provides:
  - "wire.Artifact (URL-only) in TS types + zod + Go gen"
  - "wire.RunResult in TS types + zod + Go gen"
  - "Limits/LimitsOverride.maxArtifacts (20) + maxArtifactBytes (4194304)"
  - "ExecuteRequest.collectOutput + JobSpec.collectOutput (opt-in boolean)"
  - "keys.jobOutput(id) / JobOutputKey(id) -> job:<id>:output"
  - "events.artifact / EventArtifact -> artifact"
  - "API JobSpec builder copies req.collectOutput (?? false) into the persisted spec"
affects:
  - "plan 04 (worker reads spec.CollectOutput, writes RunResult to JobOutputKey, emits EventArtifact)"
  - "plan 05 (GET /v1/jobs/:id/output reads keys.jobOutput)"
  - "plan 06 (SDKs consume RunResult / Artifact types)"
  - "plan 07 (any consumer of the artifact contract surface)"
tech-stack:
  added: []
  patterns:
    - "schema-first codegen (edit wire.schema.json -> make contract -> never hand-edit gen/**)"
    - "lockstep key/event mirror between packages/contract/src/index.ts and internal/keys/keys.go"
    - "explicit-default JSON seam value (?? false) for unambiguous cross-language reads"
key-files:
  created:
    - packages/contract/test/artifact.test.ts
    - apps/api/test/execute-collect-output.test.ts
  modified:
    - packages/contract/schema/wire.schema.json
    - packages/contract/gen/go/wire/wire.gen.go
    - packages/contract/gen/ts/types.ts
    - packages/contract/gen/ts/schemas.ts
    - packages/contract/src/index.ts
    - internal/keys/keys.go
    - apps/api/src/routes/execute.ts
    - go.mod
decisions:
  - "D-01 honored: Artifact is URL-only { name, mimeType, bytes, url }, all required, no oneOf/discriminator, no base64."
  - "D-09 honored: persisted RunResult key is job:<id>:output (jobOutput/JobOutputKey)."
  - "collectOutput persisted as an explicit boolean (req.collectOutput ?? false) so the Go worker's spec.CollectOutput read is never ambiguous across the JSON seam."
  - "Task 3 imports the generated zod schemas from gen/ts/schemas.ts directly (not the index.ts entrypoint) because the experimental-strip-types node:test loader cannot resolve index.ts's gen/ts/types.js (.js) re-export."
metrics:
  duration: ~20m
  completed: 2026-06-03
---

# Phase 9 Plan 01: Phase-9 Wire-Contract Surface Summary

Added the URL-only `Artifact`, persisted `RunResult`, artifact caps, and the opt-in `collectOutput` flag to the wire contract (regenerated TS+zod+Go), mirrored the `job:<id>:output` key + `artifact` event in both languages, and wired `collectOutput` from the request into the persisted `JobSpec` — the foundation every other Phase-9 plan compiles against.

## What Was Built

- **Schema (Task 1):** `Artifact { name, mimeType, bytes, url }` (URL-only, no `oneOf`/base64) and `RunResult` (`ResultEvent` fields + `stdout`/`stderr`/`artifacts[]`/`artifactsTruncated`) added to `wire.schema.json`. `Limits`/`LimitsOverride` gained `maxArtifacts` (default 20) and `maxArtifactBytes` (default 4194304); `ExecuteRequest`/`JobSpec` gained an optional `collectOutput` boolean. Regenerated TS types, zod validators, and Go structs via `make contract`; `make contract-check` is green.
- **Keys/events (Task 2):** `keys.jobOutput(id)` + `events.artifact` (TS) mirrored byte-identically by `JobOutputKey(id)` + `EventArtifact` (Go). Strings: `job:<id>:output` and `artifact`.
- **Contract test (Task 3):** `artifact.test.ts` round-trips a valid `Artifact`, rejects an `Artifact` missing `url`, and round-trips a `RunResult` carrying ≥1 artifact through the generated zod validators (`ArtifactSchema`, `RunResultSchema`).
- **API seam (Task 4):** `execute.ts` JobSpec builder now sets `collectOutput: req.collectOutput ?? false`. `execute-collect-output.test.ts` proves the persisted spec carries `true` when the request sets the flag and an explicit `false` when omitted, with the 202 response shape and other spec fields unchanged.

## Verification

- `make contract-check` — green (regenerates gen/** and `git diff --exit-code` finds no drift after commit).
- `go build ./...` — passes (no broken Go consumers of `wire`/`keys`).
- `pnpm --filter @teovilla/code-runner-contract test` — 10/10 pass (3 new artifact cases).
- `pnpm --filter @code-runner/api test` — 64/64 pass (3 new collectOutput cases; existing execute/jobs/traceparent unchanged).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Test infra: contract test import path**
- **Found during:** Task 3
- **Issue:** Importing the zod schemas from the package entrypoint `../src/index.ts` failed under the `node --experimental-strip-types --test` runner with `ERR_MODULE_NOT_FOUND: gen/ts/types.js` — `index.ts` re-exports the generated TS with `.js` specifiers that only resolve after a `tsup` build, which the test harness does not run.
- **Fix:** Import `ArtifactSchema`/`RunResultSchema` directly from `../gen/ts/schemas.ts` (which imports only `zod`), matching how `manifest.test.ts` imports `../src/manifest.ts` directly rather than via the entrypoint.
- **Files modified:** packages/contract/test/artifact.test.ts
- **Commit:** 80c1289

**2. [Rule 3 - Blocking] Test infra: Redis required for the API verify gate**
- **Found during:** Task 4
- **Issue:** The worktree had no Redis reachable at `redis://localhost:6380` (the vitest default), so all `/v1/execute` route tests — pre-existing and new — returned 500 (the spec/status pipeline write fails). This is an environment gap, not a code regression.
- **Fix:** Started an ephemeral `redis:7-alpine` container on port 6380 for the duration of the test run, then stopped it. No repository change. With Redis up, the full API suite (64 tests) and the new collectOutput tests pass.
- **Files modified:** none (environment only)
- **Commit:** n/a

### Plan-text corrections (not code deviations)

- The plan's Task 4 verify command referenced the API package as `@teovilla/code-runner-api`; the actual package name is `@code-runner/api`. Ran the suite under the correct name.
- `make contract` runs `go mod tidy`, which reclassified `go.opentelemetry.io/otel/metric` from indirect to direct in `go.mod` (an already-present dependency). Included in the Task 1 commit as part of the contract regen output; benign.

## TDD Gate Compliance

Tasks 3 and 4 are `tdd="true"`. The behavior under test (the `Artifact`/`RunResult` zod schemas in Task 3; the `collectOutput` field type in Task 4) is produced by Task 1's schema regen, which legitimately lands before the tests. The tests therefore pass on first run against real generated code — not against a stub — so no separate RED commit was warranted for these tasks (the contract surface they exercise was committed in 8d74421 before the test commits). Commit types reflect the change: Task 3 is a `test(...)` commit (test-only), Task 4 is a `feat(...)` commit (the `execute.ts` field + its test).

## Known Stubs

None. All four fields/types are fully wired to real generated code; the `collectOutput` request→spec copy is live end-to-end on the API side. The worker-side read of `spec.CollectOutput` is intentionally deferred to plan 04 (documented in the plan objective), not a stub.

## Self-Check: PASSED

- All 6 plan files present (4 modified + 2 created), gen/** + go.mod regenerated.
- All 4 task commits present: 8d74421, e4e0686, 80c1289, e159007.
