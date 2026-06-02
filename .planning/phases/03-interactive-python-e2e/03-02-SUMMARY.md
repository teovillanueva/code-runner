---
phase: 03-interactive-python-e2e
plan: "02"
subsystem: worker
tags: [worker, session, interactive, stdin, redis, docker, publisher]
dependency_graph:
  requires:
    - 03-01 (RedisTransport, jobstore, stdintransport)
    - 02-03 (DockerSocketRunner with CPUReader/Limits accessors)
    - 02-04 (session lifecycle, pump, clocks)
  provides:
    - session.RunInteractive with publisher sinks and caller-owned stdin
    - internal/worker run loop with claim/start-handshake/stdin-routing/teardown
    - apps/worker real boot wiring
    - Guarded integration test: full Python job over Redis + Docker
  affects:
    - internal/session (adds interactive.go)
    - internal/worker (new package)
    - internal/publisher (adds testing.go export seam)
    - apps/worker (replaces Phase-1 stub with real wiring)
    - Makefile (adds test-worker target)
tech_stack:
  added:
    - publisher.Triggerer exported interface + NewForTest (testing.go)
    - worker.Transport interface for transport injection in tests
    - worker.NewWithTransport constructor for test injection
    - worker.HandleJobForTest test entry point
  patterns:
    - subscribe-before-queued: subscriptions established before "queued" is published to eliminate start-handshake race
    - sync.Once teardown: single idempotent teardown across all terminal paths
    - full-write loop (writeAll): no partial-write bugs per PITFALLS §6
    - session owns output pipes: worker never reads Stdout()/Stderr() directly
key_files:
  created:
    - internal/session/interactive.go
    - internal/session/interactive_test.go
    - internal/worker/worker.go
    - internal/worker/worker_test.go
    - internal/worker/integration_test.go
    - internal/publisher/testing.go
  modified:
    - apps/worker/main.go (replaced Phase-1 stub with real wiring)
    - apps/worker/main_test.go (replaced with config/env tests)
    - Makefile (added test-worker target)
decisions:
  - "Subscribe stdin/ctrl BEFORE publishing 'queued' stage to eliminate start-handshake race window"
  - "worker.Transport interface for testability — *RedisTransport satisfies it; in-memory fake used in unit tests"
  - "publisher.Triggerer exported interface in testing.go (not export_test.go) so external test packages can inject fakes"
  - "python -c inline code in integration tests to avoid ReadonlyRootfs+CopyToContainer incompatibility"
  - "session.RunInteractive reuses superviseInteractive — same machinery as supervise but with real sinks"
metrics:
  duration_minutes: 21
  tasks_completed: 3
  tasks_total: 3
  files_created: 6
  files_modified: 3
  completed_date: "2026-06-02"
---

# Phase 3 Plan 02: Worker Run Loop Summary

**One-liner:** Interactive worker loop — claim→subscribe→create→park-for-start→session.RunInteractive with publisher sinks, stdin routing, single sync.Once teardown, and integration-tested over real Redis + Python:3.12.

## Tasks Completed

| Task | Name | Commit | Status |
|------|------|--------|--------|
| 1 | session.RunInteractive | 3ced3ed | Done |
| 2 | internal/worker run loop + apps/worker boot | 32d2301 | Done |
| 3 | Guarded integration test + Makefile | eab97c6 | Done |

## What Was Built

### Task 1: session.RunInteractive

Added `internal/session/interactive.go` providing:
- `Sinks` struct with `Stdout`/`Stderr func([]byte)` callbacks
- `RunInteractive(ctx, sb, limits, cpuFn, sinks)` — identical to `Run` but wires real sink callbacks into the two Pump calls instead of no-ops
- Session does **NOT** touch `sb.Stdin()` — the caller owns the write side and the single `Close()` call
- All three clocks + single `sync.Once` teardown identical to `Run`

Tests verify: stdout/stderr chunks reach sinks, idle clock fires on silence, truncation sets `Result.Truncated`, single teardown under race, session does not touch caller-owned stdin.

### Task 2: Worker Run Loop

Added `internal/worker/worker.go` with:
- `Worker` struct: jobstore, Transport interface, runner.Runner, publisher, Config, semaphore
- `Run(ctx)` claim loop: BRPOP → goroutine per job → slot released on terminate
- `runJobFromSpec`: subscribe stdin/ctrl → publish queued → create sandbox → park-for-start → RunInteractive → teardown
- Start-handshake gate (SESS-01): parks in select until start/kill/warmup fires
- Warmup timeout (SESS-03): reclaims slot if start never arrives within `WarmupMs`
- stdin routing: full-write loop (`writeAll`) feeds chunks from Redis into `sb.Stdin()`
- `stdin_close` closes `sb.Stdin()` exactly once via `sync.Once` (STDIN-02)
- Single `sync.Once` teardown on every terminal path (exit/kill/wall/idle/cpu)
- Worker NEVER reads `Stdout()` or `Stderr()` (02-04 race fix; session owns pipes)

Also added:
- `publisher/testing.go`: exported `Triggerer` interface + `NewForTest` for test injection
- `worker.Transport` interface for test injection without live Redis
- `worker.HandleJobForTest` test entry point

Updated `apps/worker/main.go` with real wiring: config from env vars, Redis ping, jobstore, stdintransport, DockerSocketRunner, publisher, graceful signal shutdown.

### Task 3: Integration Test

Added `internal/worker/integration_test.go` (build tag: `worker_integration`):
- Two-gate guard: `dialTestRedis` + `requireDockerAndImage` (skip with clear message if infra absent)
- `TestIntegration_InteractivePythonJob`: full interactive Python job over real Redis + Docker
  - Python inline: `name=input(); print('hi', name)`
  - Drives: enqueue → start → stdin "world\n" → asserts stdout "hi world" + result exitCode=0
  - Asserts no container leak (`code-runner.jobId` label filter)
- `TestIntegration_BatchPythonJob`: batch no-stdin job (SESS-02 degenerate)
  - Python inline: `print('batch')`
  - Drives: enqueue → start → asserts stdout "batch" + result exitCode=0 + no leak

Added `make test-worker` Makefile target.

## Integration Test Output (REAL RUN)

```
=== RUN   TestIntegration_InteractivePythonJob
    [private-run-integration-interactive-...] stage: {"phase":"queued"}
    [private-run-integration-interactive-...] stage: {"phase":"running"}
    [private-run-integration-interactive-...] stdout: {"chunk":"hi world\n","seq":1}
    [private-run-integration-interactive-...] result: {"durationMs":594,"exitCode":0,"idleTimedOut":false,"signal":null,"timedOut":false,"truncated":false}
--- PASS: TestIntegration_InteractivePythonJob (2.10s)

=== RUN   TestIntegration_BatchPythonJob
    [private-run-integration-batch-...] stage: {"phase":"queued"}
    [private-run-integration-batch-...] stage: {"phase":"running"}
    [private-run-integration-batch-...] stdout: {"chunk":"batch\n","seq":1}
    [private-run-integration-batch-...] result: {"durationMs":26,"exitCode":0,"idleTimedOut":false,"signal":null,"timedOut":false,"truncated":false}
--- PASS: TestIntegration_BatchPythonJob (2.08s)
PASS
ok  github.com/teovillanueva/code-runner/internal/worker  4.633s
```

**No container leaks** confirmed: `docker ps -a --filter "label=code-runner.jobId"` returns empty after test run.

## Verification

- `go build ./...` exits 0
- `go build -tags=worker_integration ./internal/worker/...` exits 0
- `go vet ./internal/worker/...` exits 0
- `go test ./... -count=1 -race` exits 0 (no Docker/Redis needed)
- `make test-worker` (with Docker + Redis + executor/python:3.12) passes both tests
- `grep -rn 'apps/api\|http.Get\|http.Post\|net/http' internal/worker apps/worker/main.go` → empty (WRK-04)
- `grep -n 'Stdout()\|Stderr()' internal/worker/worker.go` → comments only, no actual calls

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] subscribe-before-queued race condition**
- **Found during:** Task 3 integration test (warmup timeout fired because "start" was published before Redis subscription was ready)
- **Issue:** Original code published "queued" stage BEFORE calling `Subscribe`/`SubscribeControl`. External client sees "queued" and immediately sends "start", but the subscription isn't established yet. The "start" message is lost.
- **Fix:** Reordered `runJobFromSpec` to subscribe first (stdin + ctrl), THEN publish "queued". By the time the client receives "queued" via soketi, both subscriptions are active.
- **Files modified:** `internal/worker/worker.go`
- **Commit:** eab97c6

**2. [Rule 2 - Testability] worker.Transport interface**
- **Found during:** Task 2 unit test implementation
- **Issue:** `worker.New` took `*stdintransport.RedisTransport` directly, making unit tests require a live Redis connection.
- **Fix:** Extracted `worker.Transport` interface (`Subscribe` + `SubscribeControl`). Added `NewWithTransport` constructor. `*RedisTransport` satisfies the interface implicitly.
- **Files modified:** `internal/worker/worker.go`
- **Commit:** 32d2301

**3. [Rule 2 - Testability] publisher.Triggerer + NewForTest**
- **Found during:** Task 2 unit test implementation
- **Issue:** `publisher.newWithTriggerer` is unexported; external test packages cannot inject a fake triggerer.
- **Fix:** Added `internal/publisher/testing.go` with exported `Triggerer` interface and `NewForTest` constructor. Non-test file so it's available to all test packages.
- **Files modified:** `internal/publisher/testing.go` (new)
- **Commit:** 32d2301

**4. [Rule 3 - Blocking] ReadonlyRootfs + CopyToContainer incompatibility**
- **Found during:** Task 3 integration test
- **Issue:** Docker Engine API blocks `CopyToContainer` even on not-yet-started containers when `ReadonlyRootfs=true`. This prevents `Files`-based job specs from working with the Python image. This is a Docker version-specific behavior (not a new bug we introduced).
- **Fix:** Integration test uses `python -c "..."` inline code instead of `Files`, avoiding the copy entirely. The limitation is documented as a known deviation.
- **Files modified:** `internal/worker/integration_test.go`
- **Deferred:** A proper fix would add a writable tmpfs at `/workspace` in docker.go, or inject files via a different mechanism. Logged to deferred-items.

## Known Stubs

None — all code paths are wired to real implementations.

## Threat Flags

None — no new network endpoints, auth paths, or trust boundary changes beyond what the plan's threat model covers.

## Self-Check: PASSED

- `internal/session/interactive.go` exists: FOUND
- `internal/worker/worker.go` exists: FOUND
- `apps/worker/main.go` exists: FOUND (real wiring)
- `internal/worker/integration_test.go` exists: FOUND
- `internal/publisher/testing.go` exists: FOUND
- Commits 3ced3ed, 32d2301, eab97c6: FOUND in git log
