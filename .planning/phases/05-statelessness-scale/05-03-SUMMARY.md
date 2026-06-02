---
phase: 05-statelessness-scale
plan: "03"
subsystem: api-admission-scaling
tags: [backpressure, 429, admission-gate, scaling, docs, SCALE-01, SCALE-03, SCALE-05]
dependency_graph:
  requires: []
  provides: [queue-depth-admission-gate, scaling-design-doc]
  affects: [apps/api/src/routes/execute.ts, docs/scaling.md, README.md]
tech_stack:
  added: []
  patterns: [LLEN-based admission gate, fly-autoscaler queue-depth scaling]
key_files:
  created:
    - apps/api/src/admission.ts
    - apps/api/test/admission.test.ts
    - docs/scaling.md
  modified:
    - apps/api/src/config.ts
    - apps/api/src/routes/execute.ts
    - README.md
decisions:
  - "Admission gate placed AFTER manifest resolution: invalid language/version requests get 400 (not 429); only valid requests that would enqueue are checked against the queue depth."
  - "atCapacity() does a single LLEN read (O(1)); admissionError() returns depth+cap in the error message for operational visibility per T-05-08 (non-sensitive operational info)."
  - "docs/scaling.md is honest about FlyMachinesRunner being v2 and fly-autoscaler keeping warm floor >= 1."
  - "Worker service in docker-compose.yml has no container_name and no host-port bindings — confirmed scalable via docker compose up --scale worker=2."
metrics:
  duration: "~20 minutes"
  completed: "2026-06-03"
  tasks_completed: 2
  files_changed: 6
---

# Phase 05 Plan 03: Job-Admission 429 + Scaling Docs + Multi-Worker Smoke Summary

Job-admission 429 gate (LLEN-based backpressure on POST /execute) + honest autoscaling design docs (fly-autoscaler + scale-to-zero) + docker compose --scale worker=2 smoke-validated.

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | Job-admission 429 gate + config + wire into /execute | 98dc744 | apps/api/src/admission.ts, apps/api/src/config.ts, apps/api/src/routes/execute.ts, apps/api/test/admission.test.ts |
| 2 | docs/scaling.md + README scaling section + multi-worker smoke | 1f4fc58 | docs/scaling.md, README.md |

## Test Output (pasted — admission tests against live Redis 7)

```
> @code-runner/api@0.1.0 test
> vitest run


 RUN  v3.2.6 /Users/teovillanueva/code-runner/apps/api

 ✓ test/execute.test.ts (18 tests) 221ms
 ✓ test/ratelimit.test.ts (5 tests) 122ms
 ✓ test/control.test.ts (11 tests) 27ms
 ✓ test/admission.test.ts (4 tests) 28ms
 ✓ test/auth.test.ts (11 tests) 2ms

 Test Files  5 passed (5)
      Tests  49 passed (49)
   Start at  01:19:14
   Duration  609ms (transform 84ms, setup 0ms, collect 54ms, tests 399ms, environment 0ms, prepare 51ms)
```

**Admission test breakdown (4 passing):**
- over-capacity → 429 with "capacity" message + retryAfterMs
- over-capacity → no spec/status written to Redis (pipeline blocked)
- under-capacity → 202 + jobId + queue grows by 1
- distinct from stdin rate-limit 429 (different route + trigger)

**Typecheck:** `tsc --noEmit` — clean (no errors).

## Multi-Worker Smoke Evidence (SCALE-01)

**Command:** `docker compose up -d --scale worker=2 --no-build`

**Result:**
```
Container code-runner-worker-1   Started
Container code-runner-worker-2   Started
```

**`docker compose ps` output:**
```
NAME                   COMMAND          SERVICE   STATUS            PORTS
code-runner-api-1      ...              api       running (healthy) 8080/tcp
code-runner-redis-1    ...              redis     running (healthy) 6379/tcp
code-runner-soketi-1   ...              soketi    running (healthy) 6001/tcp
code-runner-worker-1   "/app/worker"    worker    running
code-runner-worker-2   "/app/worker"    worker    running
```

Both worker containers are `running`. The worker service has no `container_name` or host-port bindings — confirmed scalable. Boot logs show both workers connect to the same Redis and soketi (`redis://redis:6379`); stdin frames route only to the owning worker via `SUBSCRIBE stdin:<jobId>` (ownership-by-subscription pattern).

**Note:** The current worker version does not log its ephemeral `workerId` at startup (the field is generated in `NewWorker()` but not emitted via `slog`). Identity is distinct by container name and because each worker generates a unique UUID at construction (`newWorkerID()` in `internal/worker/worker.go:145`). The compose service is confirmed scalable: no fixed `container_name`, no host port collisions on the worker.

**`docker compose config` validation:** passed (no errors — `--quiet` flag exits 0).

**Torn down with:** `docker compose down -v`

## Deviations from Plan

None — plan executed as written. The test seeding strategy (batch RPUSH for 256 items to avoid argument limits) was an implementation detail not blocking the gate. The worker not logging its workerId at startup is a pre-existing condition; the smoke evidence (two `running` containers, distinct container names) satisfies the "two Up workers" acceptance path specified in the plan.

## Threat Surface Scan

No new network endpoints or auth paths introduced. `admissionError()` deliberately exposes only queue depth vs. cap (operational, non-sensitive per T-05-08 accept disposition). The admission gate inherits the existing `/v1/*` bearer-token middleware per T-05-09 mitigation.

## Self-Check: PASSED

- `apps/api/src/admission.ts` — FOUND
- `apps/api/src/config.ts` (maxQueueDepth) — FOUND
- `apps/api/src/routes/execute.ts` (atCapacity call) — FOUND
- `apps/api/test/admission.test.ts` — FOUND
- `docs/scaling.md` — FOUND
- `README.md` (scaling section, scale worker=2) — FOUND
- Commit 98dc744 — FOUND
- Commit 1f4fc58 — FOUND
- All 49 tests pass — CONFIRMED
- `tsc --noEmit` clean — CONFIRMED
