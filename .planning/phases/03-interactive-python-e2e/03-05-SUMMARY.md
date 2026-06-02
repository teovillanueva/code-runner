---
phase: 03-interactive-python-e2e
plan: "05"
subsystem: devstack
tags: [compose, docker, soketi, e2e, stub, interactive]
dependency_graph:
  requires: [03-01, 03-02, 03-03, 03-04]
  provides: [docker-compose.yml, apps/api/Dockerfile, apps/worker/Dockerfile, apps/stub, scripts/e2e.sh, README quickstart]
  affects: [full-stack-e2e, dev-workflow]
tech_stack:
  added:
    - tsx@^4.19.2 (TypeScript runner with .js→.ts specifier remapping)
    - pusher-js@8.4.0 (WebSocket client for soketi, Node dist)
    - quay.io/soketi/soketi:latest-16-alpine (Pusher-compatible WS server)
    - redis:7-alpine (job queue + stdin/ctrl pub/sub)
    - node:24-slim (API + stub runtime)
    - golang:1.26 + debian:bookworm-slim (worker build + runtime)
  patterns:
    - docker compose multi-service wiring (api + worker + redis + soketi + stub)
    - tsx for TypeScript-in-Docker execution with cross-package imports
    - pusher-js local HMAC channel auth (trust boundary: upstream app owns auth)
    - soketi on internal docker network (no host port conflict)
    - health check via docker compose exec (not host curl)
key_files:
  created:
    - docker-compose.yml
    - apps/api/Dockerfile
    - apps/worker/Dockerfile
    - apps/stub/Dockerfile
    - apps/stub/package.json
    - apps/stub/tsconfig.json
    - apps/stub/src/index.ts
    - scripts/e2e.sh
    - README.md
  modified:
    - apps/api/package.json (added tsx dev dep)
    - .env.example (added stub env vars)
    - Makefile (added python-image, build-images targets)
    - internal/stdintransport/redis.go (bug fix: decode StdinMessage JSON)
    - pnpm-lock.yaml
decisions:
  - "Use tsx instead of --experimental-strip-types: Node 24 strip-types does not remap .js import specifiers to .ts files; tsx (esbuild-based) handles this correctly for the workspace package import chain"
  - "No host port publishing for soketi/api in compose: stub connects via internal docker network; avoids conflicts with other local services"
  - "Local HMAC channel auth in stub: pusher-js 8.x sends x-www-form-urlencoded for channel auth but the API CHAN-02 helper expects JSON; local signing avoids the format mismatch and is the correct trust model (upstream app owns auth)"
  - "StdinMessage JSON decode in redis transport: the API publishes {chunk:...} as JSON; transport must decode before passing bytes to sandbox stdin"
metrics:
  duration: "~2 hours (including iteration on tsx, soketi ports, auth format, stdin decoding)"
  completed: 2026-06-03
  tasks_completed: 3
  files_modified: 13
---

# Phase 03 Plan 05: Docker Compose Stack + E2E — Summary

Stood up the full local dev stack and proved the interactive Python E2E end-to-end:
API→Redis→worker→sandbox→soketi→stub delivers `hello World` round-trip with exitCode=0.

## What Was Built

### DEV-01: docker-compose.yml (service wiring)
Five services on the `code-runner` internal network:
- **redis** (redis:7-alpine) — job queue + stdin/ctrl pub/sub
- **soketi** (quay.io/soketi/soketi:latest-16-alpine) — Pusher-compatible WebSocket server, **no host port** (internal network only; avoids conflicts with other local soketi instances)
- **api** (build apps/api) — Hono gateway with tsx runtime; healthcheck via docker exec
- **worker** (build apps/worker) — Go binary; mounts `/var/run/docker.sock` (host daemon, no DinD); mounts `./languages` read-only
- **stub** (build apps/stub) — interactive E2E driver, on `stub` profile (run on demand)

All config from env; soketi secret never written to Redis (CFG-02/03).

### DEV-02: apps/stub — interactive E2E driver
`src/index.ts` drives the full interactive slice:
1. `POST /v1/execute` → captures `{jobId, channel}`
2. Connects pusher-js to soketi (wsHost/wsPort; dummy cluster required by pusher-js 8.x; local HMAC signs channel auth)
3. Subscribes to `private-run-<jobId>`, waits for `pusher:subscription_succeeded`
4. `POST /v1/jobs/:id/start` (start-handshake after subscription)
5. Receives `stage: queued → running` events
6. Receives `stdout: name?` → sends `World\n` via `POST /v1/jobs/:id/stdin`
7. Sends `POST /v1/jobs/:id/stdin/close`
8. Receives `stdout: hello World`
9. Receives `result: exitCode=0 reason=exit`

Channel/event names from `@code-runner/contract`.

### DEV-03: scripts/e2e.sh + README quickstart
`scripts/e2e.sh` (bash, set -euo pipefail):
- Copies `.env.example` → `.env` if absent
- Builds `executor/python:3.12` on host daemon (required for worker)
- Brings up redis+soketi+api+worker via `docker compose up -d --build`
- Waits for API health via `docker compose exec -T api node -e ...` (no host port needed)
- Runs stub via `docker compose run --rm stub`
- Asserts "hello World" in output and stub exit 0
- Always tears down via `docker compose down -v` in `trap cleanup EXIT`

`README.md` includes quickstart with prerequisites, `make python-image`, `make up`, `make e2e`, interactive flow diagram, channel auth trust boundary documentation.

## Confirmed E2E Output

```
[stub] starting interactive E2E
[stub] POST /v1/execute
[stub] jobId=a5bd0f62-... channel=private-run-a5bd0f62-...
[stub] connecting to soketi
[stub] subscribed to private-run-a5bd0f62-...
[stub] POST /v1/jobs/:id/start
[stub] start sent
[stub] stage: queued
[stub] stage: running
[stub] stdout: name?
[stub] detected prompt, sending stdin: World
[stub] stdin sent
[stub] stdin/close sent
[stub] stdout: hello World
[stub] FOUND: hello World in stdout
[stub] result: exitCode=0 reason=exit durationMs=45
[stub] E2E PASS: hello World received + exitCode 0 + clean result
[stub] exit 0 (PASS)
[PASS] Found 'hello World' in stub output
[PASS] exitCode=0 confirmed in result event
[PASS] ===== E2E PASS: interactive execute hello World round-trip succeeded =====
[PASS] Stack torn down cleanly.
```

No leaked containers or volumes after teardown.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] stdin transport passes raw JSON to sandbox instead of decoded chunk bytes**
- **Found during:** Task 4 (E2E run)
- **Issue:** `internal/stdintransport/redis.go` `Subscribe` handler was passing the raw Redis payload `{"chunk":"World\n"}` directly to the sandbox stdin. Python received the JSON string instead of the actual bytes, causing `input()` to return `{"chunk":"World` and the idle clock to fire at 10 seconds.
- **Fix:** Decode the `StdinMessage` JSON in the Subscribe handler; pass `msg.Chunk` bytes to the handler, not the raw JSON envelope.
- **Files modified:** `internal/stdintransport/redis.go`
- **Commit:** `77192b8`

**2. [Rule 3 - Blocking] Node 24 --experimental-strip-types does not remap .js → .ts import specifiers**
- **Found during:** Task 1 (image build) — API container failed to start
- **Issue:** `packages/contract/src/index.ts` imports `"../gen/ts/types.js"` (TypeScript ESM convention). Node's `--experimental-strip-types` strips types but does NOT remap `.js` → `.ts` specifiers. The file exists as `types.ts`, not `types.js`.
- **Fix:** Switch API and stub Dockerfiles from `node --experimental-strip-types` to `tsx` (esbuild-based runner that handles `.js` → `.ts` remapping). Added `tsx@^4.19.2` to api and stub devDependencies.
- **Files modified:** `apps/api/Dockerfile`, `apps/stub/Dockerfile`, `apps/api/package.json`, `apps/stub/package.json`
- **Commit:** `77192b8`

**3. [Rule 3 - Blocking] soketi host port conflicts with other local containers**
- **Found during:** Task 4 (E2E run) — `docker compose up` failed with port bind error
- **Issue:** Port 6001 (soketi) and 8080 (API) were already allocated by other local services (operance-soketi, operance-qstash).
- **Fix:** Remove host port publishing for soketi entirely (stub/worker connect via internal network). Remove API host port publishing (health check via `docker compose exec` instead of `curl localhost`). API/soketi are only on the internal `code-runner` network.
- **Files modified:** `docker-compose.yml`, `scripts/e2e.sh`
- **Commit:** `77192b8`

**4. [Rule 1 - Bug] pusher-js 8.x requires `cluster` option even when wsHost is set**
- **Found during:** Task 4 (E2E run) — stub crashed with "Options object must provide a cluster"
- **Issue:** pusher-js 8.x has a `validateOptions()` guard that throws if `cluster == null`, even when `wsHost` is provided. The `wsHost` takes precedence for routing but `cluster` must be non-null.
- **Fix:** Provide `cluster: "mt1"` (dummy value; wsHost overrides it for routing).
- **Files modified:** `apps/stub/src/index.ts`
- **Commit:** `77192b8`

**5. [Rule 1 - Bug] pusher-js channel auth uses x-www-form-urlencoded; API CHAN-02 helper expects JSON**
- **Found during:** Task 4 (E2E run) — subscription error HTTP 400
- **Issue:** pusher-js sends channel auth as `application/x-www-form-urlencoded` to the endpoint, but the API's `/v1/channel-auth` handler calls `c.req.json()` and fails on form-encoded body.
- **Fix:** Use local HMAC signing in the stub (`createHmac("sha256", SOKETI_APP_SECRET).update("socketId:channelName").digest("hex")`). This is the correct trust model: the upstream app signs channels using its own app secret. The API CHAN-02 helper is still valid for JSON-speaking clients.
- **Files modified:** `apps/stub/src/index.ts`
- **Commit:** `77192b8`

**6. [Rule 1 - Bug] soketi event data is JSON string; stub was treating it as object**
- **Found during:** Task 4 (E2E run) — `stage.stage === undefined`, `result.exitCode === null`
- **Issue:** soketi delivers event `data` as a JSON-encoded string; the stub was treating the raw string as an object and accessing `.stage`/`.exitCode` directly.
- **Fix:** Add `parseEventData<T>(raw)` helper that JSON.parses strings before returning.
- **Files modified:** `apps/stub/src/index.ts`
- **Commit:** `77192b8`

**7. [Rule 1 - Bug] stub used wrong field names for wire event types**
- **Found during:** Task 4 debugging — raw event data showed `{phase:...}` not `{stage:...}`, result has `idleTimedOut`/`timedOut` not `reason`
- **Issue:** The stub used the contract's event names (correct) but wrong field names in the data payload. `StageEvent` uses `phase`, not `stage`. `ResultEvent` uses `exitCode`/`idleTimedOut`/`timedOut`/`signal`, not `exitCode`/`reason`.
- **Fix:** Update stub to use correct field names from `wire.gen.go` / JSON schema.
- **Files modified:** `apps/stub/src/index.ts`
- **Commit:** `77192b8`

## Known Stubs

None — all data flows are wired to real services.

## Threat Flags

| Flag | File | Description |
|------|------|-------------|
| No new threats | All as planned | The worker socket mount is worker-only (T-03-14 mitigated); soketi secret is env-only (T-03-15 mitigated); stub auth uses EXECUTOR_API_TOKEN (T-03-16 mitigated); pusher-js and redis/soketi images are pinned official releases (T-03-SC mitigated) |

## Self-Check: PASSED

- `docker-compose.yml` exists and `docker compose config -q` exits 0
- `apps/api/Dockerfile`, `apps/worker/Dockerfile`, `apps/stub/Dockerfile` exist and build
- `apps/stub/src/index.ts` exists, 130+ lines, typechecks
- `scripts/e2e.sh` exists, executable, bash -n passes
- `README.md` has Quickstart section
- `Makefile` has `make e2e` and `make python-image`
- Commits `ca59923` and `77192b8` exist
- `docker.sock` mounted only on worker service (verified by grep)
- E2E run confirmed: `hello World` + `exitCode=0` + clean teardown
