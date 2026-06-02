---
phase: 05-statelessness-scale
plan: "01"
subsystem: worker/scale
tags: [scale, heartbeat, semaphore, stateless, redis, worker]
dependency_graph:
  requires: []
  provides: [ephemeral-worker-id, heartbeat-substrate, owned-jobs-set, slot-semaphore-hardened]
  affects: [internal/worker, internal/jobstore, internal/keys, internal/config, apps/worker]
tech_stack:
  added: []
  patterns:
    - acquire-before-claim slot semaphore (chan struct{} buffered at MaxSandboxes)
    - sync.Once teardown releasing slot on every terminal path
    - ephemeral workerID from crypto/rand hex-encoded 16 bytes
    - heartbeat goroutine with TTL-refreshing Redis SET
    - owned-jobs Redis SADD/SREM wired into Create/teardown
key_files:
  created:
    - internal/worker/heartbeat.go
    - internal/jobstore/capacity.go
    - internal/worker/scale_test.go
  modified:
    - internal/keys/keys.go
    - internal/config/config.go
    - internal/worker/worker.go
    - apps/worker/main.go
    - .env.example
decisions:
  - "Slot release moved into single sync.Once teardown; early-return paths call releaseSlot() explicitly before teardown is defined"
  - "HandleJobForTest passes a no-op slot release so unit tests are unaffected"
  - "countingSandbox in scale_test wraps DockerSocketRunner sandbox and delegates CPUReader/Limits for DockerSandbox type assertion"
  - "scale_test uses port 6384 (separate from integration_test port 6381) to allow parallel test runs"
metrics:
  duration: "~20 minutes"
  completed: "2026-06-03"
  tasks_completed: 3
  tasks_total: 3
  files_modified: 8
---

# Phase 05 Plan 01: Statelessness + Scale Foundation Summary

Establish the per-worker ephemeral identity, harden the acquire-before-claim slot semaphore, and write the Redis heartbeat + owned-jobs substrate for the reaper (SCALE-01, SCALE-02).

## What Was Built

**SCALE-01 — Ephemeral workerId per worker instance:**
- `newWorkerID()` in `internal/worker/heartbeat.go` hex-encodes 16 crypto/rand bytes (no new dependency).
- Each `Worker` struct carries a `workerID string` field, set once at `NewWithTransport` time.
- `WorkerIDForTest()` exposes the ID for test assertions.

**SCALE-02 — Acquire-before-claim slot semaphore, released exactly once:**
- The slot channel (`chan struct{}` buffered at `MaxSandboxes`) was already present; this plan hardens the release path.
- `defer w.releaseSlot()` removed from `Run()`'s goroutine.
- `runJobFromSpec` now receives a `releaseSlot func()` parameter; every terminal path calls it exactly once:
  - Subscribe stdin failure → `releaseSlot()` + return
  - SubscribeControl failure → `releaseSlot()` + return
  - Create sandbox failure → `releaseSlot()` + return
  - All post-Create terminals (warmup, kill, ctx.Done, normal exit, session error) → `releaseSlot()` inside sync.Once teardown
- `HandleJobForTest` passes a no-op release so unit tests are unchanged.

**Heartbeat substrate:**
- `startHeartbeat(ctx)` in `heartbeat.go` writes one beat immediately (key exists at boot), then ticks every `HeartbeatInterval`.
- Each beat calls `store.Heartbeat(ctx, workerID, ttl)` which does `SET worker:<id>:heartbeat <unix-ms> EX <ttl>`.
- Transient Redis errors are logged and tolerated — a missed beat never crashes the worker.

**Owned-jobs set:**
- `store.AddOwnedJob(ctx, workerID, jobID)` → `SADD worker:<id>:jobs jobID` called after `Create` succeeds.
- `store.RemoveOwnedJob(ctx, workerID, jobID)` → `SREM` called inside sync.Once teardown.
- `store.OwnedJobs` → `SMEMBERS` for the reaper to consume (plan 05-02).

**New keys:**
- `keys.WorkerHeartbeatKey(id)` → `worker:<id>:heartbeat`
- `keys.WorkerJobsKey(id)` → `worker:<id>:jobs`
- `keys.CapacityFree` → `capacity:free` (best-effort INCR/DECR counter; authoritative gate is queue depth)

**New config fields:** `HeartbeatIntervalMs` (default 5000), `HeartbeatTTLMs` (default 20000) in both `config.Config` and `worker.Config`, threaded through `apps/worker/main.go` configFromEnv.

## Concurrency-Cap Integration Test — Real Run Output

```
=== RUN   TestConcurrencyCap
    scale_test.go:247: Enqueued 6 jobs, MaxSandboxes=2
    scale_test.go:283: scale_test: sent start for scale-cap-1780442396324595000-1
    scale_test.go:283: scale_test: sent start for scale-cap-1780442396323634000-0
    scale_test.go:283: scale_test: sent start for scale-cap-1780442396325225000-2
    scale_test.go:283: scale_test: sent start for scale-cap-1780442396325855000-3
    scale_test.go:283: scale_test: sent start for scale-cap-1780442396326441000-4
    scale_test.go:283: scale_test: sent start for scale-cap-1780442396327026000-5
    scale_test.go:310: scale_test: 6/6 jobs produced result events
    scale_test.go:311: scale_test: peak concurrent sandboxes = 2 (MaxSandboxes = 2)
    scale_test.go:312: scale_test: current active sandboxes after drain = 0
    scale_test.go:347: scale_test: workerID = e97c7ec1f9300831380c02c7792d6840
    scale_test.go:349: scale_test: total events = 25
--- PASS: TestConcurrencyCap (12.00s)
PASS
ok  	github.com/teovillanueva/code-runner/internal/worker	12.348s
```

**Peak concurrent sandboxes = 2 = MaxSandboxes.** All 6 jobs completed. Owned-jobs set was empty after drain. OwnedJobs Redis set was empty after all jobs drained.

## Task Commits

| Task | Name | Commit | Key Files |
|------|------|--------|-----------|
| 1 | New keys + config fields + capacity store ops | 3eb50d7 | internal/keys/keys.go, internal/config/config.go, internal/jobstore/capacity.go, .env.example |
| 2 | Ephemeral workerId + heartbeat + owned-jobs/slot teardown | 5cf4335 | internal/worker/heartbeat.go, internal/worker/worker.go, apps/worker/main.go |
| 3 | Concurrency-cap integration test | 5c7e3a0 | internal/worker/scale_test.go |

## Deviations from Plan

### Minor Structural Deviations

**1. [Rule 2 - Missing critical functionality] HandleJobForTest no-op slot release**
- **Found during:** Task 2
- **Issue:** Unit tests call `HandleJobForTest` which calls `runJobFromSpec` directly without acquiring a slot. If `runJobFromSpec` called `w.releaseSlot()` unconditionally it would send to a full channel, causing a deadlock or panic.
- **Fix:** Added `releaseSlot func()` parameter to `runJobFromSpec` and `runJob`. `HandleJobForTest` passes `func() {}`. Run's goroutine passes `w.releaseSlot`. This satisfies the exact-once requirement with zero test regression.
- **Files modified:** internal/worker/worker.go

**2. [Rule 1 - Bug prevention] countingSandbox DockerSandbox interface delegation**
- **Found during:** Task 3
- **Issue:** The worker does `sb.(DockerSandbox)` type assertion to get `CPUReader`/`Limits`. If `countingSandbox` didn't forward these, the assertion would fail silently (falling through to the no-op path), producing incorrect CPU tracking in tests but not a crash.
- **Fix:** Added `CPUReader()` and `Limits()` methods to `countingSandbox` that delegate to inner via type assertions, satisfying the `DockerSandbox` interface.
- **Files modified:** internal/worker/scale_test.go

None - plan executed as specified.

## Threat Surface Scan

No new network endpoints or auth paths introduced. The new Redis keys (`worker:<id>:heartbeat`, `worker:<id>:jobs`, `capacity:free`) are written only by trusted workers over the existing Redis connection. These keys are worker-internal — no client-facing API reads them. Consistent with T-05-02 (accept — trusted writer only) in the plan's threat model.

## Self-Check: PASSED

- internal/keys/keys.go: FOUND (WorkerHeartbeatKey, WorkerJobsKey, CapacityFree)
- internal/config/config.go: FOUND (HeartbeatIntervalMs, HeartbeatTTLMs)
- internal/jobstore/capacity.go: FOUND (6 Store methods)
- internal/worker/heartbeat.go: FOUND (newWorkerID, startHeartbeat)
- internal/worker/worker.go: FOUND (workerID, AddOwnedJob, RemoveOwnedJob, sync.Once teardown)
- internal/worker/scale_test.go: FOUND (build-tagged, MaxSandboxes assertions)
- apps/worker/main.go: FOUND (HeartbeatIntervalMs/TTL threaded)
- .env.example: FOUND (WORKER_HEARTBEAT_INTERVAL_MS, WORKER_HEARTBEAT_TTL_MS, MAX_QUEUE_DEPTH)
- Commits 3eb50d7, 5cf4335 exist in git log
- `go build ./...` PASSED
- `go test ./...` (no tags) PASSED — 11 packages, 0 failures
- `go test -tags=worker_integration ./internal/worker/... -run TestConcurrencyCap` PASSED — peak=2==MaxSandboxes
