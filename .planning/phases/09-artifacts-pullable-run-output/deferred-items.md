# Deferred Items — Phase 09

Out-of-scope discoveries logged during execution. NOT fixed by the discovering plan.

## From 09-05 (executor)

- **`apps/api/src/routes/execute.ts:44` — TS2739**: literal `limits` object is missing `maxArtifacts`/`maxArtifactBytes` (added to the `Limits` contract type in an earlier wave). Pre-existing, unrelated to R9 (the output route). `vitest` (esbuild) passes; only `tsc --noEmit` flags it. Out of scope for the Redis-only output route — should be fixed by whichever plan owns `execute.ts` limit resolution.
  - **RESOLVED (orchestrator, post-merge wave-4 integration fix):** `mergeLimits()` now propagates `maxArtifacts: overrides.maxArtifacts ?? defaults.maxArtifacts` and `maxArtifactBytes: overrides.maxArtifactBytes ?? defaults.maxArtifactBytes` (R3 "resolved Limits carry the defaults, overridable"). `tsc --noEmit` exit 0; API suite 69/69.

## From 09-06 (SDK + React surface)

- **apps/api integration tests time out under `pnpm -r test`** (ratelimit/redis-backed suites: `Test timed out in 30000ms`). Cause: these suites need a live Redis that is not running in the executor worktree. `apps/api` was NOT modified by plan 09-06 (`git diff --name-only HEAD <base> -- apps/api` is empty), so this is unrelated to the SDK/React changes. The two packages touched (`@teovilla/code-runner-sdk-node`, `@teovilla/code-runner-react`) pass cleanly in isolation. Re-run `apps/api` tests against the compose Redis stack.
