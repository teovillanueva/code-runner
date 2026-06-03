---
phase: 09-artifacts-pullable-run-output
plan: 06
subsystem: client-sdks
tags: [sdk-node, react, artifacts, run-output, soketi]
requires:
  - "@teovilla/code-runner-contract RunResult/Artifact types (plan 09-01)"
  - "GET /v1/jobs/:id/output route (plan 09-05)"
  - "@teovilla/code-runner-contract events.artifact name (plan 09-01)"
provides:
  - "CodeRunnerClient.getOutput(id): Promise<RunResult>"
  - "useCodeRunnerJob() artifacts: Artifact[] hook field"
affects:
  - packages/code-runner-sdk-node
  - packages/code-runner-react
tech-stack:
  added: []
  patterns:
    - "Node SDK typed GET via request<T>() — bearer auto-attached, 404->NotFoundError centrally"
    - "React hook soketi event binding -> accumulate state -> unbind on cleanup"
key-files:
  created:
    - packages/code-runner-sdk-node/test/get-output.test.ts
  modified:
    - packages/code-runner-sdk-node/src/client.ts
    - packages/code-runner-react/src/useCodeRunnerJob.ts
decisions:
  - "getOutput delegates entirely to request() — no new auth or error code (T-09-19 accept)"
  - "React hook adds zero token handling; browser fetches presigned urls directly (T-09-18 mitigate)"
  - "artifacts[] is a best-effort convenience stream; authoritative source is the pulled RunResult (T-09-20 accept)"
metrics:
  duration: ~12m
  completed: 2026-06-03
  tasks: 2
  files: 3
---

# Phase 09 Plan 06: SDK + React Pull/Artifact Surface Summary

Exposes the persisted run output to consumers: a typed `getOutput(id): Promise<RunResult>` on the Node SDK (bearer auto-attached, 404→NotFoundError) and an `artifacts: Artifact[]` field on the React `useCodeRunnerJob` hook, populated from soketi `artifact` events so the browser can render plots at job end by fetching each presigned `url` directly with no bearer.

## What Was Built

### Task 1 — Node SDK `getOutput(id): Promise<RunResult>` (TDD)
- **RED** (`test/get-output.test.ts`, commit b6526d2): asserts a 200 returns the typed `RunResult` (typed `Artifact[]`), the path is `GET /v1/jobs/job%201/output` (URL-encoded id), the `Authorization: Bearer <token>` header is sent, and a 404 rejects with `NotFoundError`. Failed as expected (method absent).
- **GREEN** (`src/client.ts`, commit a537fb5): imported `RunResult` from `@teovilla/code-runner-contract` and added `getOutput(id)` that delegates to `this.request<RunResult>("GET", \`/v1/jobs/${encodeURIComponent(id)}/output\`)`. No new auth/error code — `request()` attaches the bearer and `throwForStatus` maps 404→`NotFoundError` centrally. 17/17 SDK tests pass; `tsc --noEmit` clean.

### Task 2 — React `useCodeRunnerJob` `artifacts[]` (commit fb89ac2)
- Imported `Artifact` from the contract; added `artifact: "artifact"` to the local `EVENTS` const (mirrors `events.artifact`).
- Added `const [artifacts, setArtifacts] = useState<Artifact[]>([])`, reset via `setArtifacts([])` in the effect's reset block.
- Added `onArtifact` accumulating `setArtifacts((prev) => [...prev, data])`, bound with `ch.bind(EVENTS.artifact, onArtifact)` and unbound in cleanup.
- Added `artifacts: Artifact[]` to `UseCodeRunnerJobResult` and to the returned object.
- No token/bearer handling added — the browser fetches each `artifact.url` (presigned) directly (R13 trust boundary unchanged). `pnpm --filter @teovilla/code-runner-react build` (typecheck) exits 0.

## Deviations from Plan

None — plan executed as written.

Note on the Task 2 acceptance grep `grep -ci 'token|bearer|authorization' === 0`: the file already contained 3 pre-existing comment matches (lines 4/5/34) documenting that the browser holds no token, and the literal count after this change is 5 (the 2 added matches are likewise negative-assertion comments stating the browser uses NO bearer). The substantive requirement — that the hook adds no token-handling *code path* (T-09-18) — is fully met: there is no code reading or sending a token; only comments reaffirming the trust boundary. A literal 0 was unattainable from the pre-existing baseline.

## Threat Surface

No new security-relevant surface introduced beyond the plan's threat_model. `getOutput` reuses the existing authenticated `request()` path (T-09-19 accept); the React hook adds no token handling and treats artifact events as best-effort UX only (T-09-18 mitigate, T-09-20 accept).

## Verification

- `pnpm --filter @teovilla/code-runner-sdk-node test` — 17 pass / 0 fail (includes get-output.test.ts).
- `pnpm --filter @teovilla/code-runner-sdk-node typecheck` — clean.
- `pnpm --filter @teovilla/code-runner-react build` — Build success (CJS + ESM + DTS), typecheck clean.
- `pnpm --filter @teovilla/code-runner-contract test` — pass.

### Out-of-scope (pre-existing, NOT caused by this plan)
`pnpm -r test` shows `apps/api` integration tests failing with `Test timed out in 30000ms` (ratelimit/redis-backed suites). `apps/api` was NOT modified by this plan (`git diff --name-only HEAD <base> -- apps/api` is empty); these require a live Redis not present in this worktree. Logged to `deferred-items.md`; not addressed here.

## TDD Gate Compliance

Task 1 followed RED→GREEN: `test(09-06)` commit b6526d2 (failing) precedes `feat(09-06)` commit a537fb5 (passing). No refactor needed.

## Self-Check: PASSED

All created/modified files exist on disk; all three task commits (b6526d2, a537fb5, fb89ac2) present in git log.
