# Deferred Items — Phase 08 Distributed Observability

Out-of-scope discoveries logged during execution. NOT fixed (scope boundary: only
auto-fix issues directly caused by the current task's changes).

## Pre-existing Redis integration-test failures (env-dependent) — from 08-02

**Found during:** 08-02 Task 2 verification (`go test ./...`).

**Packages:** `internal/jobstore`, `internal/stdintransport` — neither touched by
plan 08-02 (which only adds OTel to `internal/otelinit`, `internal/logging`,
`internal/worker`, `apps/worker`, `internal/runner` docs).

**Symptoms (against the local Redis 7.4.9 on 127.0.0.1:6379):**
- `internal/jobstore`: `TestJobStore_ClaimTimeout` / `TestJobStore_ClaimEnqueue` /
  `TestJobStore_SpecRoundTrip` intermittently fail. The go-redis warning
  `specified duration is 100ms, but minimal supported value is 1s - truncating to 1s`
  and `ReadSpec ... key not found` point to a Redis-version / BLPOP-min-duration /
  TTL mismatch in this environment, not to application logic.
- `internal/stdintransport`: `TestRedisTransport_RoundTrip` /
  `TestRedisTransport_CloseStopsDelivery` time out waiting 3s for pub/sub delivery
  — a pub/sub timing/environment issue.

**Evidence they are pre-existing & out of scope:**
- `git status` shows no modifications to either package by this plan.
- Failures persist after `redis-cli FLUSHALL` and the failing subtest varies run
  to run (flaky/env-dependent), independent of the OTel changes.
- The OTel no-op gate means with `OTEL_*` unset the worker behaves exactly as
  before; `go test ./internal/worker/` (the package this plan changed) is GREEN.

**Disposition:** Deferred. Not a regression from 08-02. Should be triaged
separately (likely a Redis test-harness/version pin issue).

## From 08-03 (API trace inject + pino migration)

- **`console.error` still present in `apps/api/src/redis.ts:19`** (ioredis connection-error handler) and **`apps/api/src/channelAuth.ts:75`** (optional Pusher channel-auth helper error path). These are NOT in 08-03's scope (the plan migrates only the request/job paths: `server.ts`, `app.ts` onError, `routes/execute.ts`). A future plan can migrate these two remaining `console.error` call sites to the pino logger for full structured-logging coverage. Low risk; both are non-request-path error logs.
