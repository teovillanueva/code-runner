---
phase: "03-interactive-python-e2e"
plan: "04"
subsystem: "api-gateway"
tags: ["hono", "typescript", "redis", "auth", "rate-limit", "REST"]
dependency_graph:
  requires: []
  provides:
    - "POST /v1/execute → 202 {jobId,channel,status:queued} + Redis LPUSH+SET"
    - "GET /v1/jobs/:id → JobStatus or 404"
    - "GET /v1/languages → LanguageInfo[] from manifests"
    - "POST /v1/jobs/:id/start|stdin|stdin/close|kill → Redis PUBLISH only"
    - "constant-time bearer auth (sha256+timingSafeEqual)"
    - "per-job stdin rate-limit + pending-byte cap → 429"
    - "optional channel-auth helper (ENABLE_CHANNEL_AUTH flag)"
  affects: []
tech_stack:
  added:
    - "hono@4.12.23 + @hono/node-server@2.0.4"
    - "@hono/zod-validator@0.8.0 (uses generated ExecuteRequestSchema from contract)"
    - "ioredis@5.11.0 (non-blocking commands only — PUBLISH/LPUSH/SET/GET)"
    - "hono-rate-limiter@0.5.3 (frame rate, Redis-backed)"
    - "pusher@5.3.3 (optional channel-auth helper only)"
    - "vitest@3.2.0 (test runner)"
  patterns:
    - "sha256+timingSafeEqual constant-time bearer auth (STACK §1.3)"
    - "generated zod schemas from @code-runner/contract — never hand-written (STACK §1.2)"
    - "pipeline/multi for atomic Redis writes (SET spec + SET status + LPUSH)"
    - "per-job Redis counter for pending-byte cap (INCRBY/DECRBY)"
    - "INCR+EXPIRE for per-window frame rate limiting"
key_files:
  created:
    - apps/api/package.json
    - apps/api/tsconfig.json
    - apps/api/vitest.config.ts
    - apps/api/src/config.ts
    - apps/api/src/redis.ts
    - apps/api/src/auth.ts
    - apps/api/src/manifests.ts
    - apps/api/src/app.ts
    - apps/api/src/server.ts
    - apps/api/src/routes/execute.ts
    - apps/api/src/routes/jobs.ts
    - apps/api/src/routes/languages.ts
    - apps/api/src/routes/control.ts
    - apps/api/src/ratelimit.ts
    - apps/api/src/channelAuth.ts
    - apps/api/test/auth.test.ts
    - apps/api/test/execute.test.ts
    - apps/api/test/control.test.ts
    - apps/api/test/ratelimit.test.ts
  modified:
    - pnpm-lock.yaml
decisions:
  - "Used generated ExecuteRequestSchema from @code-runner/contract (not hand-written) per STACK §1.2"
  - "Auth uses sha256+timingSafeEqual pattern: hashes both sides to 32-byte digest before comparing, making it length-safe"
  - "vitest.config.ts sets env vars before module load to solve config singleton initialization order with ES module hoisting"
  - "Rate limiting uses two independent guards: INCR+EXPIRE frame counter and INCRBY pending-byte counter, both Redis-backed for horizontal scale"
  - "Optional channel-auth helper is strictly flag-gated (ENABLE_CHANNEL_AUTH=true) and never a dependency of core flow"
metrics:
  duration: "~45 minutes"
  completed: "2026-06-02"
  tasks_completed: 3
  tests_written: 45
  files_created: 19
---

# Phase 03 Plan 04: Hono API Gateway Summary

Thin, trusted HTTP gateway: bearer auth, contract-validated requests, Redis enqueue, PUBLISH control/stdin — coupling to the worker via Redis only.

## Tasks Completed

| Task | Name | Commit | Status |
|------|------|--------|--------|
| 1 | Scaffold apps/api + config + redis + auth middleware + manifest loader | 9383e4e | Done |
| 2 | POST /v1/execute + GET /v1/jobs/:id + GET /v1/languages (TDD RED+GREEN) | 7619b47 | Done |
| 3 | Control endpoints + stdin rate-limit/byte-cap + optional channel-auth | 15fe513 | Done |

## Test Output

```
 RUN  v3.2.6 /Users/teovillanueva/code-runner/apps/api

 ✓ test/execute.test.ts (18 tests) 181ms
 ✓ test/ratelimit.test.ts (5 tests) 118ms
 ✓ test/control.test.ts (11 tests) 28ms
 ✓ test/auth.test.ts (11 tests) 2ms

 Test Files  4 passed (4)
      Tests  45 passed (45)
```

`pnpm --filter @code-runner/api typecheck` exits 0.

## Architecture Decisions

### Auth (T-03-05)

Used `sha256+timingSafeEqual` pattern: both sides are hashed to a fixed 32-byte SHA-256 digest before comparing with `timingSafeEqual`. This removes the early-exit length leak that would occur if `timingSafeEqual` were called directly on raw tokens of differing lengths (it throws on mismatched buffer sizes, and that throw itself is a timing side-channel).

### Schema Validation (T-03-06)

All request bodies validated against **generated** zod schemas imported from `@code-runner/contract` (never hand-written). This ensures the API validator cannot drift from the Go wire format. `@hono/zod-validator` gives Hono-native `c.req.valid('json')` typing.

### Rate Limiting (T-03-07)

Two independent Redis-backed guards applied to `/stdin`:
1. **Frame rate**: `INCR job:<id>:stdin_rate:<window>` + `EXPIRE` — resets each window
2. **Pending-byte cap**: `INCRBY job:<id>:stdin_pending <bytes>` — decremented by worker as it drains

Both are Redis-backed so limits hold correctly across horizontal API replicas.

### Secrets (T-03-08 / CFG-02/03)

`SOKETI_APP_SECRET` is read from env only in `config.ts` and used only in `channelAuth.ts` (optional, flag-gated). It is never written to Redis, never returned in any response body. `grep -rn 'APP_SECRET' apps/api/src` shows only env-read locations, not response paths.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] vitest env hoisting — config singleton loaded before test env vars**

- **Found during:** Task 2 (first test run)
- **Issue:** ES module `import` statements are hoisted and executed before module body code, so `process.env["EXECUTOR_API_TOKEN"] = VALID_TOKEN` assignments in test files ran after `config.ts` already loaded as a singleton — resulting in the wrong token being used.
- **Fix:** Moved env configuration to `vitest.config.ts` `env` block, which sets vars before any test file modules are loaded. This is the correct pattern for vitest config-level env injection.
- **Files modified:** `apps/api/vitest.config.ts`, `apps/api/test/execute.test.ts`
- **Commit:** 7619b47

**2. [Rule 1 - Bug] TS type error: FileInput[] not assignable to [FileInput, ...FileInput[]]**

- **Found during:** Task 2 typecheck
- **Issue:** `req.files` (type `FileInput[]` from zod schema inference) not directly assignable to the `[FileInput, ...FileInput[]]` tuple type in `JobSpec.files`.
- **Fix:** Added `as JobSpec["files"]` cast — safe because the zod schema validates `minItems: 1` at runtime before this cast is reached.
- **Files modified:** `apps/api/src/routes/execute.ts`
- **Commit:** 7619b47

**3. [Rule 1 - Bug] ioredis publish mock type mismatch in control tests**

- **Found during:** Task 3 typecheck
- **Issue:** Mock implementation typed `channel: string` but ioredis signature accepts `string | Buffer`.
- **Fix:** Updated mock to accept `string | Buffer` and call `.toString()` on both.
- **Files modified:** `apps/api/test/control.test.ts`
- **Commit:** 15fe513

## Known Stubs

None — all routes are fully implemented and wired to Redis.

## Threat Flags

No new threat surface beyond what was planned in the threat model (T-03-05 through T-03-08, all mitigated).

## Self-Check: PASSED

All 5 key source files exist on disk. All 3 task commits verified in git log.
