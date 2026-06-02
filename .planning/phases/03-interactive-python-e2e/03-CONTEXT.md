# Phase 3: Interactive Python End-to-End - Context

**Gathered:** 2026-06-02
**Status:** Ready for planning
**Mode:** Auto-generated (discuss skipped — autonomous run)

<domain>
## Phase Boundary

Wire ONE language (Python 3.12) through the entire interactive path so `/execute → subscribe → /start → stdin → streamed output → result` works end-to-end against a local `docker compose` stack. This is the first full vertical slice: Hono API → Redis → Go worker session (using the Phase 2 runner + clocks + publisher) → soketi → client.

Build ON Phase 1 (contract, manifest loader, keys, interfaces, config) and Phase 2 (DockerSocketRunner, internal/session three clocks + pump + sync.Once teardown, internal/publisher soketi). Do NOT reimplement those.
</domain>

<decisions>
## Implementation Decisions

### Components to build
1. **Go Redis layer** (`go get github.com/redis/go-redis/v9`):
   - Real `internal/stdintransport` implementation over Redis pub/sub (replaces the stub): Publish/Subscribe on `stdin:<jobId>` and a control channel `ctrl:<jobId>`.
   - A queue consumer + job store: `BRPOP keys.JobQueue` to claim a `wire.JobSpec` (spec read from `job:<id>:spec` hash or the queue payload), and write `wire.JobStatus` to `job:<id>:status`. Keep keys in `internal/keys`.
2. **Worker main loop** (`apps/worker/main.go` → real run loop, likely a new `internal/worker` package): claim job → publish `stage queued` → subscribe `stdin:<id>` + `ctrl:<id>` → **start-handshake**: create the sandbox but PARK until a `start` control message arrives (warm-up timeout reclaims the slot + tears down if `start` never comes — SESS-03); on `start`, publish `stage running` (and `compiling` when a compile step exists, though Python has none), run via `internal/session` (single owner of the sandbox pipes: route Redis stdin chunks INTO the session's stdin writer — do NOT read sandbox pipes directly, avoiding the pump/pipe race noted in 02-04-SUMMARY), stream stdout/stderr via `internal/publisher`, deliver `stdin_close` as EOF exactly once, handle `kill`; on any terminal path run the single `sync.Once` teardown: unsubscribe, close pipes, remove container, free slot, publish terminal `result`. Batch (no-stdin) Python runs as the degenerate case (SESS-02). The worker talks ONLY to Redis + soketi — never to the API (WRK-04).
3. **Hono API** (`apps/api`, Node via `@hono/node-server`, `ioredis`, `@code-runner/contract`):
   - Endpoints: `POST /v1/execute` (validate with generated zod, resolve manifest→JobSpec via the shared TS manifest loader, generate `jobId`+`channel=private-run-<id>`, write status+spec, `LPUSH` queue, return `202 {jobId, channel, status:"queued"}` BEFORE the process starts — SESS-01/API-01), `POST /v1/jobs/:id/start|stdin|stdin/close|kill` (PUBLISH to `ctrl:`/`stdin:`), `GET /v1/jobs/:id` (read status), `GET /v1/languages` (manifest loader).
   - Auth middleware: `EXECUTOR_API_TOKEN` bearer with constant-time compare (`crypto.timingSafeEqual`, length-safe) — API-08.
   - Validation errors are clear for malformed body, unknown language/version, unknown jobId — API-09.
   - Per-job stdin rate limit + pending-stdin byte cap → `429` — API-10.
   - Stateless; only Redis — API-11. Config from env only (EXECUTOR_API_TOKEN, REDIS_URL, SOKETI_* if signing channel auth) — CFG-01/02/03; no secret-returning endpoints. CHAN-02: optionally expose a documented, clearly-optional channel-auth helper behind the token.
4. **Python 3.12 image** (`languages/python-3.12/Dockerfile`): `python:3.12-slim`, numpy/pandas/requests baked in, non-root user, `python -u` friendly (force unbuffered output — set PYTHONUNBUFFERED or the run uses `-u`), workspace in /tmp. Image tag `executor/python:3.12` (matches the existing manifest). LANG-04 (per-request limits override) + LANG-05.
5. **docker-compose.yml**: services `api`, `worker` (mounts `/var/run/docker.sock`), `redis`, `soketi`, `stub` (upstream app). Build the python image (or document `make` to build it). `.env`/`.env.example` wiring. DEV-01.
6. **Stub upstream app** (`apps/stub` or `examples/stub`): minimal Node/TS that drives an interactive execute (call API execute → subscribe to the soketi channel via pusher-js → confirm → start → send stdin → print streamed output → result). DEV-02.
7. **E2E script** (`scripts/e2e.sh`): brings up the stack and runs a full interactive Python execute end-to-end, asserting streamed output + result. DEV-03.

### Locked facts
- Output buffering: Python buffers stdout when not a TTY — force unbuffered (`PYTHONUNBUFFERED=1` or `python -u`) or interactive streaming silently shows nothing. This is mandatory (per FEATURES.md / PITFALLS.md).
- soketi event-size limit ~10KB — the publisher already chunks; the API/worker must not exceed it.
- Channel auth is the upstream app's job; the stub demonstrates it using the app key/secret. Keep any API helper optional/non-core (CHAN-02).
</decisions>

<canonical_refs>
## Canonical References — downstream agents MUST read

### Build ON (Phases 1-2)
- `internal/manifest`, `internal/keys`, `internal/config`, `internal/runner` (DockerSocketRunner + accessors), `internal/session` (Run + clocks + pump + teardown), `internal/publisher` (soketi)
- `02-03-SUMMARY.md` and `02-04-SUMMARY.md` — how session.Run wires to the sandbox, the stdin/pipe ownership note, seccomp-inline fix
- `packages/contract/src/index.ts` + `packages/contract/src/manifest.ts` — TS contract, zod, manifest loader; keys/channels/events
- `packages/contract/gen/go/wire` — Go wire types

### Research
- `.planning/research/STACK-API-CONTRACT-DEPLOY.md` — Hono (Node via @hono/node-server), ioredis, constant-time auth, hono-rate-limiter, pusher-js channel auth, soketi compose config
- `.planning/research/ARCHITECTURE.md` — start-handshake, ownership-by-subscription, session goroutine tree
- `.planning/research/PITFALLS.md` — output buffering, EOF-exactly-once, pub/sub loss, soketi limits
- `CLAUDE.md` — toolchain & conventions
</canonical_refs>

<specifics>
## Specific Ideas

Phase 3 requirement IDs: API-01..11, WRK-01..04, SESS-01..03, STDIN-01..03, OUT-01..03, LANG-04, LANG-05, CFG-01..03, CHAN-02, DEV-01..03.

**Testing (Docker + ability to run Redis/soketi locally):**
- API tests (vitest or node:test): token auth (constant-time, reject missing/invalid), zod validation + clear errors, execute returns 202 {jobId,channel,status} and LPUSHes, stdin/control PUBLISH, GET status/languages, 429 on stdin overflow. Use a real Redis container (or ioredis-mock) for the Redis assertions.
- Go worker integration test (`//go:build docker` or redis-guarded): enqueue a Python job, drive start/stdin/stdin_close over Redis, assert stage/stdout/result events reach a fake or real soketi, assert no container leak. This exercises the full session.Run wiring.
- E2E (`scripts/e2e.sh`): `docker compose up`, run the stub, assert an interactive `input()`-style Python program receives stdin and streams output, then a clean result. Keep it runnable and documented.
- Build the python image locally and run at least one real container job through the worker to prove the slice. Network is available for the image build.
</specifics>

<deferred>
## Deferred Ideas

Slot capacity accounting, 429 backpressure under full capacity, reliable claim, dead-worker reaper, N-replica scaling (Phase 5). The abuse suite (Phase 4). Rust/R/SQLite (Phase 6). README/deploy docs/CI (Phase 7). Phase 3 proves the single-language interactive slice works end-to-end.
</deferred>
