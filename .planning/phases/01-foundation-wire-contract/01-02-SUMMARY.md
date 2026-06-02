---
phase: 01-foundation-wire-contract
plan: 02
subsystem: runner-stdintransport-config
tags: [interfaces, stubs, config, redis-constraint, no-op]
dependency_graph:
  requires: [packages/contract/gen/go/wire]
  provides: [internal/runner, internal/stdintransport, internal/config]
  affects: [Phase 2 DockerSocketRunner, Phase 3 Redis pub/sub StdinTransport]
tech_stack:
  added: []
  patterns: [interface-stub-seam, compile-time-assertion, sync.Once idempotency, in-memory handler map]
key_files:
  created:
    - internal/runner/runner.go
    - internal/runner/stub.go
    - internal/runner/runner_test.go
    - internal/stdintransport/transport.go
    - internal/stdintransport/stub.go
    - internal/stdintransport/transport_test.go
    - internal/config/config.go
    - internal/config/config_test.go
    - docs/redis-constraint.md
  modified: []
decisions:
  - "Result struct defined inline in runner package (not aliasing wire.ResultEvent) to give Phase 2 freedom to add runner-internal fields without coupling to the wire schema"
  - "Config.RequiresNativeRedis() as a method (not a constant) so it remains testable and could later be guarded by a parsed URL check"
  - "stubSubscription.deliver uses a mutex-guarded closed flag rather than channel-close to avoid double-close panics on idempotent Close()"
metrics:
  duration_seconds: 237
  completed: "2026-06-02T19:37:36Z"
  tasks_completed: 3
  files_created: 9
  files_modified: 0
---

# Phase 1 Plan 02: Runner/Sandbox + StdinTransport Interfaces + Config Summary

## One-liner

Runner/Sandbox and StdinTransport swap-boundary interfaces with idempotent no-op stubs, plus native-Redis worker constraint encoded in Config and documented in docs/redis-constraint.md.

## What Was Built

### Task 1 — internal/runner (Runner/Sandbox interface + stub)

- `runner.go`: `Runner` interface (`Create(ctx, wire.JobSpec) (Sandbox, error)`), `Sandbox` interface (Stdin/Stdout/Stderr/Wait/Kill/Cleanup), `Result` struct mirroring `wire.ResultEvent` terminal fields.
- `stub.go`: `stubRunner` + `stubSandbox` (no-op, in-memory buffers). Cleanup idempotent via `sync.Once`. `var _ Runner` and `var _ Sandbox` compile-time assertions. `NewStub()` constructor.
- `runner_test.go`: 6 tests covering Create, pipe accessors, Wait, Kill, idempotent Cleanup (5x), Stdin write+close.

### Task 2 — internal/stdintransport (StdinTransport interface + stub)

- `transport.go`: `StdinTransport` interface (`Publish`, `Subscribe`), `Subscription` interface (`Close`). Documented MVP=pub/sub on `stdin:<jobID>`, upgrade path=`XREAD BLOCK` Streams.
- `stub.go`: `stubTransport` (in-memory handler map, mutex-guarded). `stubSubscription` with idempotent Close via `sync.Once`. `var _ StdinTransport` and `var _ Subscription` assertions. `NewStub()` constructor.
- `transport_test.go`: 5 tests covering round-trip, no-subscriber publish, close-stops-delivery, idempotent close, multi-subscriber broadcast.

### Task 3 — internal/config + docs/redis-constraint.md (CFG-04)

- `config.go`: `Config` struct with all worker env vars (REDIS_URL, SOKETI_*, WORKER_MAX_SANDBOXES, DOCKER_HOST, SANDBOX_RUNTIME, WORKER_WARMUP_MS). `RequiresNativeRedis() bool` method (always true) encoding CFG-04 with a doc comment listing the blocking ops. `Default()` returns dev defaults matching `.env.example`.
- `config_test.go`: 2 tests asserting RequiresNativeRedis() true (zero value + default) and non-zero defaults.
- `docs/redis-constraint.md`: explains why Upstash REST is API-only, which operations require native TCP (SUBSCRIBE/BRPOP/XREAD BLOCK), deployment recommendations, and URL hint.

## Test Results

```
ok  github.com/teovillanueva/code-runner/internal/runner         (6 tests)
ok  github.com/teovillanueva/code-runner/internal/stdintransport (5 tests)
ok  github.com/teovillanueva/code-runner/internal/config         (2 tests)

go build ./...  — exit 0
```

13/13 tests pass. `go vet` clean across all three packages.

## Deviations from Plan

### Auto-fixed Issues

None — plan executed exactly as written.

### Decisions Made

1. **Result struct inline (not alias):** `runner.Result` is defined independently rather than aliasing `wire.ResultEvent`. This decouples runner internals from the wire format, letting Phase 2 add runner-only fields (e.g. container ID) without touching the schema.

2. **RequiresNativeRedis() as method:** Made it an instance method on `Config` (rather than a package-level constant) so it can be tested on both zero-value and initialized configs, and could later inspect the `RedisURL` field as a validation heuristic.

3. **stubSubscription close guard:** Used a `closed bool` field under a mutex inside `stubSubscription.deliver()` rather than closing a channel, to avoid double-close panics when `Close()` is called multiple times concurrently.

## Known Stubs

All implementations in this plan are intentional stubs. None flow to UI rendering or block plan goals:

| Stub | File | Reason |
|------|------|--------|
| stubRunner.Create | internal/runner/stub.go | Phase 1: proves seam only; DockerSocketRunner in Phase 2 |
| stubTransport.Publish/Subscribe | internal/stdintransport/stub.go | Phase 1: in-memory only; Redis pub/sub in Phase 3 |
| Config.Default() | internal/config/config.go | Phase 1: defaults only; env-parsing wired in Phase 2/3 |

## Self-Check: PASSED

Files created:
- /Users/teovillanueva/code-runner/internal/runner/runner.go — FOUND
- /Users/teovillanueva/code-runner/internal/runner/stub.go — FOUND
- /Users/teovillanueva/code-runner/internal/runner/runner_test.go — FOUND
- /Users/teovillanueva/code-runner/internal/stdintransport/transport.go — FOUND
- /Users/teovillanueva/code-runner/internal/stdintransport/stub.go — FOUND
- /Users/teovillanueva/code-runner/internal/stdintransport/transport_test.go — FOUND
- /Users/teovillanueva/code-runner/internal/config/config.go — FOUND
- /Users/teovillanueva/code-runner/internal/config/config_test.go — FOUND
- /Users/teovillanueva/code-runner/docs/redis-constraint.md — FOUND

Commits: fda830d (runner), 575113e (stdintransport), 6ad9f91 (config+docs)
