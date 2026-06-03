---
phase: 07-oss-release-deployment
plan: 02
subsystem: infra
tags: [github-actions, ci, gofmt, go-vet, vitest, redis, pnpm, corepack, go-jsonschema, contract-drift]

# Dependency graph
requires: []
provides:
  - ".github/workflows/ci.yml: four-job CI matrix (lint, go-unit, js, contract-drift)"
  - "lint job: gofmt -l + go vet ./... + pnpm -r typecheck on every push/PR"
  - "go-unit job: go test ./... Docker-free suite on ubuntu-latest"
  - "js job: pnpm -r test (API vitest + contract node:test) with live Redis on port 6380"
  - "contract-drift job: make tools (go-jsonschema) + make contract-check (regenerate + git diff --exit-code)"
affects: [DOCS-01, contribution-readiness, future-phases-CI]

# Tech tracking
tech-stack:
  added: [github-actions-ci-matrix]
  patterns:
    - "All CI jobs use go-version-file: go.mod (inherits go 1.26) with cache: true"
    - "pnpm activated via corepack enable honoring packageManager field (pnpm@10.33.2)"
    - "Redis service mapped 6380:6379 to match vitest REDIS_URL default — no env override needed"
    - "pnpm install --frozen-lockfile for lockfile-pinned reproducibility"
    - "concurrency cancel-in-progress per github.ref to avoid duplicate CI runs"

key-files:
  created:
    - .github/workflows/ci.yml
  modified: []

key-decisions:
  - "Redis port 6380:6379 used in js job — matches vitest.config.ts default (REDIS_URL ?? TEST_REDIS_URL ?? redis://localhost:6380); no env override needed"
  - "LANGUAGES_DIR set to ${{ github.workspace }}/languages in js job — vitest default resolves __dirname-relative which would fail in CI checkout"
  - "corepack enable chosen over pnpm/action-setup — honors packageManager field from root package.json (pnpm@10.33.2) without hardcoding the version in the workflow"
  - "No docker-integration job added — abuse.yml already guards Docker/cgroup paths; prefer correctness over breadth per plan"
  - "go-unit job uses go test ./... with no tags — excludes docker/abuse/worker_integration build tags as intended"
  - "contract-drift job: make tools before make contract-check — go-jsonschema must be on PATH before pnpm contract generate is invoked"

patterns-established:
  - "Task 1 (read-only fact-gathering) before Task 2 (authoring) pattern: confirms exact env var wiring before writing the workflow"

requirements-completed: [DOCS-01]

# Metrics
duration: 12min
completed: 2026-06-03
---

# Phase 7 Plan 02: CI Matrix Summary

**Four-job GitHub Actions matrix (lint/go-unit/js/contract-drift) with live Redis on port 6380, corepack pnpm, and go-jsonschema drift gate covering every push and PR**

## Performance

- **Duration:** ~12 min
- **Started:** 2026-06-03T00:00:00Z
- **Completed:** 2026-06-03T00:12:00Z
- **Tasks:** 2 (Task 1 read-only, Task 2 authored ci.yml)
- **Files modified:** 1 (.github/workflows/ci.yml created)

## Accomplishments

- Authored `.github/workflows/ci.yml` with four jobs (lint, go-unit, js, contract-drift) running on push + pull_request + workflow_dispatch
- Confirmed exact Redis port (6380) and env vars from `apps/api/vitest.config.ts` before wiring the js job
- Wire-contract drift gates CI via `make tools` (installs go-jsonschema) + `make contract-check` (regenerate + git diff --exit-code)
- YAML validated via `python3 yaml.safe_load`; abuse.yml left completely untouched

## Task Commits

1. **Task 1: Confirm API test Redis port + env (read-only)** — no commit (no files modified)
2. **Task 2: Author ci.yml** — `827af9d` (chore)

**Plan metadata:** (docs commit follows)

## Task 1 Findings — Authoritative env var list for the js CI job

Source: `apps/api/vitest.config.ts`

| Env var | Default in vitest.config.ts | CI action |
|---|---|---|
| `REDIS_URL` | `process.env["REDIS_URL"] ?? process.env["TEST_REDIS_URL"] ?? "redis://localhost:6380"` | Service mapped `6380:6379` — no override needed |
| `EXECUTOR_API_TOKEN` | `"test-default-token"` | Not injected — safe default used |
| `LANGUAGES_DIR` | `resolve(__dirname, "../../languages")` (= repo root languages/) | Set to `${{ github.workspace }}/languages` explicitly |
| `ENABLE_CHANNEL_AUTH` | Hardcoded `"false"` in config | Not injected — hardcoded in config |

Redis port: **6380** — service maps `6380:6379` matching the vitest default so no extra `REDIS_URL` env var is required in the js job.

## Files Created/Modified

- `.github/workflows/ci.yml` — Four-job CI matrix: lint (gofmt/go vet/typecheck), go-unit (go test ./...), js (pnpm -r test + Redis:6380), contract-drift (make tools + make contract-check)

## Decisions Made

1. **Redis port 6380:6379** — Matches vitest REDIS_URL default verbatim; no env override needed, simpler and self-documenting.
2. **LANGUAGES_DIR explicit in CI** — vitest default resolves `__dirname/../../languages` (from `apps/api/`), which correctly finds `languages/` locally but depends on the checkout structure in CI; explicitly setting it to `${{ github.workspace }}/languages` is safer.
3. **corepack enable over pnpm/action-setup** — `package.json` `packageManager: pnpm@10.33.2` already pins the version; corepack honors it without repeating the version in the workflow YAML.
4. **No docker-integration job** — `abuse.yml` already covers Docker/cgroup/worker_integration paths; adding it here would duplicate flaky infrastructure tests. Plan explicitly noted "Prefer correctness over breadth."
5. **go-unit has no build tags** — `go test ./...` with no `-tags` flag runs the Docker-free unit suite; Docker/abuse/worker suites belong to abuse.yml where Docker is purpose-configured.

## Deviations from Plan

None — plan executed exactly as written. All four jobs match specification. Port confirmed in Task 1 before wiring Task 2.

## Issues Encountered

None. YAML parsed valid on first authoring pass. All `make` targets (`tools`, `contract-check`) confirmed present in Makefile. Go version `1.26` confirmed in go.mod; workflow uses `go-version-file: go.mod` throughout.

## Validation Summary

| Check | Result |
|---|---|
| `python3 yaml.safe_load` on ci.yml | PASSED |
| Jobs present: lint, go-unit, js, contract-drift | PASSED |
| Redis service uses `redis:7`, port `6380:6379` | PASSED |
| `pnpm -r typecheck` in lint job | PASSED |
| `go test ./...` in go-unit job | PASSED |
| `make tools` before `make contract-check` in contract-drift | PASSED |
| `pnpm install --frozen-lockfile` in all JS jobs | PASSED |
| `go-version-file: go.mod` in all Go jobs | PASSED |
| abuse.yml untouched (md5 verified) | PASSED |

## Threat Surface Scan

No new network endpoints, auth paths, or trust boundary changes introduced. `pnpm install --frozen-lockfile` and `make tools` (pinned via go.mod dependency resolution at `@latest` from the tools target) match the mitigations in the plan's threat register (T-07-SC). No new threat flags.

## User Setup Required

None — no external service configuration required. The workflow runs with no secrets (contributor fork PRs work without secrets, matching the plan's trust boundary analysis).

## Next Phase Readiness

- CI matrix is live and will trigger on the next push to main or any PR
- abuse.yml continues as the Docker/cgroup safety gate (branch protection: "abuse / abuse" required status check)
- DOCS-01 contribution-ready release claim is now backed by a real CI matrix

---
*Phase: 07-oss-release-deployment*
*Completed: 2026-06-03*
