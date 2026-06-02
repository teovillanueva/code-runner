---
phase: 03-interactive-python-e2e
plan: "01"
subsystem: database
tags: [redis, go-redis, pub-sub, jobstore, queue, brpop, worker]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: internal/keys, internal/config, internal/stdintransport stub + interface
  - phase: 02-worker-core
    provides: apps/worker scaffold, wire types
provides:
  - go-redis/v9 shared client constructor (internal/redisx)
  - Redis pub/sub StdinTransport implementation (internal/stdintransport/redis.go)
  - SubscribeControl for wire.ControlMessage on ctrl:<jobId>
  - JobSpec/JobStatus round-trip store (internal/jobstore/jobstore.go)
  - BRPOP-based job queue consumer + Enqueue helper (internal/jobstore/queue.go)
affects: [03-02-worker-run-loop, 03-04-api, 03-05-e2e]

# Tech tracking
tech-stack:
  added: [github.com/redis/go-redis/v9 v9.20.0]
  patterns:
    - two-gate skip pattern for live-Redis tests (parse URL + Ping)
    - sync.Once for idempotent Subscription.Close
    - ErrNotFound sentinel with errors.Is for missing-key API mapping
    - All Redis keys/channels via internal/keys (no inline strings)

key-files:
  created:
    - internal/redisx/redisx.go
    - internal/redisx/redisx_test.go
    - internal/stdintransport/redis.go
    - internal/stdintransport/redis_test.go
    - internal/jobstore/jobstore.go
    - internal/jobstore/queue.go
    - internal/jobstore/jobstore_test.go
  modified:
    - go.mod
    - go.sum
    - Makefile

key-decisions:
  - "No Ping in redisx.New constructor — keeps construction cheap and testable without live Redis"
  - "Subscription.Close: cancel goroutine context first, then pubsub.Close() — prevents goroutine leak (T-03-02)"
  - "ErrTimeout sentinel on BRPOP redis.Nil — worker loop can re-poll without fatal error"
  - "dialOrSkip inline in each test package — avoids cross-package test helper import complexity"
  - "WriteStatus stamps UpdatedAtMs at write time — worker does not need to compute current epoch"

patterns-established:
  - "Two-gate live-Redis skip: parse URL → Ping → skip if either fails; go test ./... stays green"
  - "All Redis key strings via internal/keys; never inline job:<id>:spec or similar"
  - "sync.Once in Subscription.Close for idempotent cleanup (PITFALLS goroutine leak guard)"

requirements-completed: [WRK-01, STDIN-01, STDIN-03]

# Metrics
duration: 25min
completed: 2026-06-02
---

# Phase 03 Plan 01: Go Redis Layer Summary

**go-redis/v9 client, Redis pub/sub StdinTransport with SubscribeControl, and BRPOP job store wiring the Hono API to the Go worker**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-06-02T22:45:00Z
- **Completed:** 2026-06-02T20:54:49Z
- **Tasks:** 3
- **Files modified:** 10

## Accomplishments
- Added go-redis/v9 v9.20.0 with shared `redisx.New` / `redisx.NewFromURL` constructors
- Implemented `RedisTransport` satisfying `StdinTransport` interface via PUBLISH/SUBSCRIBE on `stdin:<jobId>`; `SubscribeControl` subscribes `ctrl:<jobId>` and JSON-decodes `wire.ControlMessage`
- Implemented `jobstore.Store` with `WriteSpec`/`ReadSpec`/`WriteStatus`/`ReadStatus` (JSON SET/GET keyed via `internal/keys`) plus `Claim` (BRPOP) and `Enqueue` (LPUSH) for the job queue

## Task Commits

1. **Task 1: redisx shared client constructor** - `1bebdf6` (feat)
2. **Task 2: RedisTransport TDD RED** - `adfb6ea` (test)
3. **Task 2: RedisTransport TDD GREEN** - `8cb3c3e` (feat)
4. **Task 3: jobstore + queue consumer** - `2bfab3f` (feat)

## Files Created/Modified
- `internal/redisx/redisx.go` - `New(config.Config)` and `NewFromURL(string)` constructors; no Ping on construct
- `internal/redisx/redisx_test.go` - Unit tests: malformed URL error, valid URL non-nil client, live Ping skip guard
- `internal/stdintransport/redis.go` - `RedisTransport` + `redisSubscription`; Publish/Subscribe/SubscribeControl; compile-time interface assertions
- `internal/stdintransport/redis_test.go` - Round-trip, close-stops-delivery, close-idempotent, publish-no-subscriber, SubscribeControl decode; all skip without Redis
- `internal/jobstore/jobstore.go` - `Store.WriteSpec/ReadSpec/WriteStatus/ReadStatus`; `ErrNotFound` sentinel; `IsNotFound` helper
- `internal/jobstore/queue.go` - `Store.Claim` (BRPOP + `ErrTimeout`); `Store.Enqueue` (LPUSH)
- `internal/jobstore/jobstore_test.go` - Spec/status round-trips, not-found sentinels, Claim/Enqueue, ClaimTimeout; all skip without Redis
- `go.mod` / `go.sum` - Added `github.com/redis/go-redis/v9 v9.20.0`
- `Makefile` - Added `test-redis` target

## Decisions Made
- No Ping in `redisx.New` constructor — keeps construction cheap and testable
- `sync.Once` in `Subscription.Close`: cancel goroutine context first, then `pubsub.Close()` (T-03-02 goroutine leak mitigation)
- `ErrTimeout` sentinel on BRPOP `redis.Nil` — worker run loop can re-poll in a normal loop without treating timeout as fatal
- Inline `dialOrSkip` in each test package rather than cross-package export — sidesteps Go's restriction on importing `_test.go` symbols

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
- `go get` initially ran in shell working dir rather than module root; resolved by running it from the correct directory.
- Note: `ClaimTimeout` test with 100ms timeout logs `"specified duration is 100ms, but minimal supported value is 1s - truncating to 1s"` from go-redis. This is expected behavior; the test passes correctly (Redis enforces 1s minimum for BRPOP timeout).

## Live Redis Test Output

Executed against `docker run -d -p 6380:6379 redis:7`:

```
ok  github.com/teovillanueva/code-runner/internal/redisx         0.500s
ok  github.com/teovillanueva/code-runner/internal/stdintransport  1.254s
ok  github.com/teovillanueva/code-runner/internal/jobstore        1.425s
```

All 21 tests pass (4 redisx + 6 stdintransport + 6 stub + 5 jobstore). All Redis-dependent tests skip cleanly without infra.

## Verification

- `go build ./...` exits 0
- `go test ./...` exits 0 with no Redis running (live-path tests skip via `dialOrSkip`)
- `grep -rn 'keys\.'` confirms every Redis key/channel in `internal/jobstore` and `internal/stdintransport/redis.go` is derived from `internal/keys`
- WRK-01: `jobstore.Claim` BRPOPs `keys.JobQueue` and returns a jobId
- STDIN-01: `RedisTransport.Publish/Subscribe` round-trip on `stdin:<jobId>`
- STDIN-03: `RedisTransport.SubscribeControl` decodes `wire.ControlMessage` on `ctrl:<jobId>`

## Next Phase Readiness
- `internal/redisx`, `internal/stdintransport/redis.go`, and `internal/jobstore` are ready for the worker run loop (Plan 02)
- Plan 02 can call `redisx.New(cfg)`, `jobstore.New(client)`, `store.Claim(ctx, timeout)`, `store.ReadSpec(ctx, jobID)`, and `stdintransport.NewRedis(client)` without any further setup

---
*Phase: 03-interactive-python-e2e*
*Completed: 2026-06-02*
