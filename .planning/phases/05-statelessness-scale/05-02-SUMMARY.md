---
phase: 05-statelessness-scale
plan: "02"
subsystem: reaper
tags: [reaper, orphan-sweep, docker, redis, scale, capacity]
dependency_graph:
  requires: ["05-01"]
  provides: ["SCALE-04"]
  affects: [internal/reaper, internal/jobstore, apps/worker]
tech_stack:
  added: []
  patterns:
    - "label-based container discovery (Docker ContainerList + label filter)"
    - "Redis SCAN for worker-set key discovery"
    - "go reaper.Run(ctx) goroutine at worker boot"
key_files:
  created:
    - internal/reaper/reaper.go
    - internal/reaper/reaper_test.go
  modified:
    - internal/jobstore/capacity.go
    - apps/worker/main.go
    - Makefile
decisions:
  - "Reaper uses store-level helpers (HeartbeatAlive, ScanWorkerJobsKeys) rather than raw redis.Client — keeps reaper free of Redis import, easier to unit-test"
  - "Sweep is conservative on Redis error: logs warning and skips sweep rather than false-positive reaping live containers"
  - "Reaper interval = heartbeatTTL + 5s so dead worker's key reliably expired before first evaluation"
  - "Second Docker client created in main.go for the reaper rather than widening runner interface"
metrics:
  duration: "~12 minutes"
  completed: "2026-06-03"
  tasks_completed: 2
  files_created: 2
  files_modified: 3
---

# Phase 05 Plan 02: Dead-Worker Reaper (SCALE-04) Summary

**One-liner:** Label-based orphan sweep using Docker ContainerList + Redis SCAN/EXISTS, force-removes dead-worker containers with RemoveVolumes:true and marks jobs error.

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | Reaper package + worker wiring + Makefile | 2b85c5f | internal/reaper/reaper.go, internal/jobstore/capacity.go, apps/worker/main.go, Makefile |
| 2 | Reaper integration test | 905206b | internal/reaper/reaper_test.go |

## Implementation Details

### internal/reaper/reaper.go

The `Reaper` struct holds a `*client.Client` (Docker), a `*jobstore.Store`, and an interval. Its public API:

- `New(cli, store, interval) *Reaper` — constructor
- `(r *Reaper) Run(ctx)` — ticker goroutine, calls `Sweep` on each tick until ctx done
- `(r *Reaper) Sweep(ctx) error` — the core orphan-detection and removal pass:
  1. `ContainerList(All:true, Filters: label "code-runner.jobId")` — enumerate all labelled containers
  2. Scan `worker:*:jobs` keys via `store.ScanWorkerJobsKeys` (SCAN), extract worker IDs
  3. For each worker, call `store.HeartbeatAlive` (EXISTS) to determine liveness
  4. Build live-jobs map: union of OwnedJobs for all live workers
  5. Container is ORPHANED if its jobId is NOT in the live-jobs map
  6. For each orphan: `ContainerRemove(Force:true, RemoveVolumes:true)` — removes container + anonymous /workspace volume; tolerate not-found
  7. Call `store.RemoveOwnedJob` to clean up stale set membership (best-effort)
  8. Call `store.WriteStatus(..., state="error")` — best-effort terminal status

### internal/jobstore/capacity.go additions

Two new methods added to `*Store`:

- `HeartbeatAlive(ctx, workerID) (bool, error)` — Redis EXISTS on WorkerHeartbeatKey; used by reaper to distinguish live vs. dead workers
- `ScanWorkerJobsKeys(ctx, cursor) (keys, nextCursor, error)` — Redis SCAN with pattern `worker:*:jobs`; gives the reaper access to all worker-owned-jobs keys without raw redis.Client exposure

### apps/worker/main.go wiring

After the worker is constructed, a second `*client.Client` is created for the reaper (cheap, avoids changing the runner interface). The reaper interval is `heartbeatTTL + 5s`. The reaper is started as `go r.Run(ctx)` before `w.Run(ctx)`.

### Makefile

New target: `reaper-test` — runs `go test -tags=reaper_integration -timeout 180s ./internal/reaper/... -run Reaper -v`.

## Real Test Output

```
=== RUN   TestReaper_OrphanedContainer_IsReaped
    reaper_test.go:219: created container 4f0e09c37f95 (job reaper-orphan-1780443005404252000) with volume 0bd34f9a7d0b68863ec9bc440ebf94f43a9f1476118840647f0fecb010165ed9
    reaper_test.go:229: Test A: no heartbeat key for any worker owning job reaper-orphan-1780443005404252000 — expecting reap
2026/06/03 01:30:05 INFO reaper.Sweep: reaping orphaned container containerID=4f0e09c37f95 jobID=reaper-orphan-1780443005404252000
2026/06/03 01:30:05 INFO reaper: container removed containerID=4f0e09c37f95 jobID=reaper-orphan-1780443005404252000
    reaper_test.go:240: container for job reaper-orphan-1780443005404252000 is gone after sweep
    reaper_test.go:244: anonymous volume 0bd34f9a7d0b68863ec9bc440ebf94f43a9f1476118840647f0fecb010165ed9 is gone after sweep
    reaper_test.go:250: job reaper-orphan-1780443005404252000 status is "error"
--- PASS: TestReaper_OrphanedContainer_IsReaped (0.29s)
=== RUN   TestReaper_LiveWorkerContainer_IsProtected
    reaper_test.go:271: created container 90adef193ce6 (job reaper-live-1780443005690216000) with volume b15b02d92c41bb2e575c9024b230dac4dfd2143240959ca66c42afef31f673ef
    reaper_test.go:290: Test B: worker test-worker-1780443005690214000 is ALIVE and owns job reaper-live-1780443005690216000 — container should NOT be reaped
    reaper_test.go:304: container for job reaper-live-1780443005690216000 still exists after sweep (live worker protected)
--- PASS: TestReaper_LiveWorkerContainer_IsProtected (0.24s)
PASS
ok  	github.com/teovillanueva/code-runner/internal/reaper	0.892s
```

**Orphan reaped:** container `4f0e09c37f95` and its anonymous volume `0bd34f9a7d0b...` were both removed. Job status set to `"error"`.

**Live worker protected:** container `90adef193ce6` (job owned by worker with live heartbeat) survived the sweep.

## Deviations from Plan

None — plan executed exactly as written.

The plan mentioned optionally adding a `code-runner.workerId` label to map jobId → worker via the container label rather than scanning `worker:*:jobs` sets. The implementation took the SCAN approach as described in the plan's `<read_first>` section (no label change needed in docker.go), which is the simpler path and keeps docker.go unchanged.

## Threat Surface Scan

No new network endpoints, auth paths, or schema changes introduced. The reaper operates within the control plane (Docker socket + Redis) and does not add any new trust-boundary surface beyond what is described in the plan's threat model.

## Known Stubs

None.

## Self-Check: PASSED

- `/Users/teovillanueva/code-runner/internal/reaper/reaper.go` — exists
- `/Users/teovillanueva/code-runner/internal/reaper/reaper_test.go` — exists
- Commit `2b85c5f` — feat(05-02): add dead-worker reaper (SCALE-04)
- Commit `905206b` — test(05-02): reaper integration test — orphan reaped + live worker protected
- `go build ./...` — PASS
- `go test ./...` (no tags) — all green
- `go test -tags=reaper_integration ./internal/reaper/... -run Reaper -v` — both tests PASS
