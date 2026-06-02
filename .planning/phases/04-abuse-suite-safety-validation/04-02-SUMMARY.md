---
phase: 04-abuse-suite-safety-validation
plan: 02
subsystem: ci-gate
tags: [ci, github-actions, abuse-suite, safety, readme]
dependency_graph:
  requires: [04-01]
  provides: [linux-ci-abuse-gate, fan-out-gate-documentation]
  affects: [.github/workflows/abuse.yml, README.md]
tech_stack:
  added: [github-actions, redis-service-container]
  patterns: [cgroup-v2-ci-gate, required-status-check, fan-out-gate-documentation]
key_files:
  created:
    - .github/workflows/abuse.yml
  modified:
    - README.md
decisions:
  - "Use services.redis (not docker run step) for Redis — cleaner lifecycle, health-check readiness, no manual wait"
  - "Map container port 6379 to host 6381 (6381:6379) to match harness default redis://localhost:6381 without extra env"
  - "Set explicit job env TEST_REDIS_URL=redis://localhost:6381 in addition to port mapping for clarity and observability"
  - "go-version-file: go.mod so workflow tracks go.mod pin automatically without manual maintenance"
  - "workflow_dispatch added for manual re-runs without a push/PR"
  - "concurrency group cancel-in-progress to avoid redundant runs on rapid pushes"
metrics:
  duration: "~5 minutes"
  completed: "2026-06-03"
  tasks: 2
  files: 2
---

# Phase 4 Plan 02: Linux Abuse CI Gate & README Safety Gate Note Summary

GitHub Actions workflow (`abuse.yml`) that runs the abuse suite on ubuntu-latest (real Linux cgroup v2) with redis:7 service + `executor/python:3.12` image, gating pull requests; README Safety Gate section documenting the fan-out dependency (TEST-07, TEST-08).

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | Author the Linux abuse CI workflow | 8026d1b | `.github/workflows/abuse.yml` |
| 2 | README Safety Gate section + YAML sanity | db44a20 | `README.md` |

## What Was Built

### Task 1: .github/workflows/abuse.yml

A 58-line GitHub Actions workflow with:
- **Triggers**: `push` (to `main`), `pull_request`, `workflow_dispatch`
- **Concurrency**: `abuse-${{ github.ref }}` group with `cancel-in-progress: true`
- **Job**: `abuse` on `ubuntu-latest`, `timeout-minutes: 20`
- **Redis service**: `redis:7` with health-check, ports mapped `6381:6379`
- **Env**: `TEST_REDIS_URL: redis://localhost:6381`
- **Steps**: checkout@v4, setup-go@v5 (go-version-file: go.mod), `make python-image`, `make abuse`

### Task 2: README Safety Gate section

Added "Safety Gate" section under Architecture in README.md documenting:
- Abuse suite is the required gate before language fan-out (Phase 6)
- CI workflow runs on every PR and push to main
- New languages must pass the gate before merging
- Repo owners should enable "abuse / abuse" as a required branch-protection status check
- Local run instructions: `make python-image`, `docker run -d -p 6381:6379 redis:7`, `make abuse`

## YAML Validation Result

Validated with `python3 -c "import yaml; yaml.safe_load(...)"`:

```
workflow valid: 4 steps
Triggers: ['push', 'pull_request', 'workflow_dispatch']
Services: ['redis']
Env vars: ['TEST_REDIS_URL']
Concurrency group: abuse-${{ github.ref }}
Timeout minutes: 20
ALL ASSERTIONS PASSED
```

Note: PyYAML parses `on:` as boolean `True` (YAML 1.1 spec behavior) — this is expected and does not indicate a syntax error. GitHub Actions correctly interprets `on:` for workflow triggers. `actionlint` was not installed; the YAML was manually audited against the GitHub Actions schema.

**Note**: The workflow cannot be executed locally (no GitHub runner). It is authored, lint-validated via PyYAML, and will run on push to GitHub.

## Redis Env Var / Port Confirmation

Both `internal/worker/abuse_test.go` (line 62-65) and `internal/worker/integration_test.go` (line 55-58) use the same `dialTestRedis` helper:

```go
rawURL := os.Getenv("TEST_REDIS_URL")
if rawURL == "" {
    rawURL = "redis://localhost:6381"  // default port 6381
}
```

The workflow sets `TEST_REDIS_URL: redis://localhost:6381` in the job `env:` block and maps the Redis service container's port 6379 to host port 6381 (`ports: ["6381:6379"]`). This matches exactly — the harness will connect to `localhost:6381` whether or not the env var is set (both the explicit env and the port mapping agree).

## Deviations from Plan

None — plan executed exactly as written.

## Known Stubs

None.

## Threat Flags

None — no new network endpoints, auth paths, file access patterns, or schema changes introduced. The workflow file only defines CI configuration.

## Self-Check: PASSED

- `.github/workflows/abuse.yml` exists and passes YAML validation
- `README.md` contains "abuse" and "fan-out" / "language" matches
- Commits 8026d1b and db44a20 exist in git log
