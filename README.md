# code-runner

An **open-source (MIT), self-hostable** remote code execution service with live interactive stdin.
Run untrusted code in a hardened sandbox and stream output in real time via soketi (Pusher-compatible).

## What it is

code-runner is a **Piston-style remote execution service** built as a polyglot monorepo:

- **Hono/TypeScript API gateway** (`apps/api`) — the single trusted entry point. Validates requests, enqueues jobs, relays stdin/control signals.
- **Go worker** (`apps/worker`) — claims jobs from Redis, launches hardened sandbox containers via the host Docker daemon, keeps sessions alive, streams output to soketi.
- **Manifest-driven language packages** (`languages/`) — each language is a folder containing a `manifest.json` + `Dockerfile`. The loader auto-discovers them at boot; nothing is hardcoded in Go or the API.
- **Shared JSON-Schema wire contract** (`packages/contract`) — TS types + Zod validators + Go structs are generated from a single schema. A CI drift check guards the polyglot seam.

### Architecture and data flow

```
Upstream App ──Bearer token──► API (Hono)
                                   │  LPUSH jobs:queue
                                   ▼
                               Redis
                                   │  BRPOP / SUBSCRIBE stdin:<jobId>
                                   ▼
                          Worker NODE (Go, long-lived)
                          ┌─────────────────────────┐
                          │  Sandbox  │  Sandbox  …  │  (up to WORKER_MAX_SANDBOXES)
                          └─────────────────────────┘
                                   │  Pusher HTTP trigger
                                   ▼
                              soketi (WebSocket fan-out)
                                   │
                                   ▼
                            Browser / Client
```

**Data flow in full:**

1. Upstream app `POST /v1/execute` → API validates, resolves the language manifest, writes job spec + status to Redis, LPUSHes the job ID, returns `202 {jobId, channel}`.
2. Client subscribes to the private soketi channel `private-run-<jobId>`.
3. Client sends `POST /v1/jobs/:id/start` (the start-handshake — see below).
4. Worker BRPOPs the job ID, launches the sandbox, begins streaming output events to soketi.
5. stdin chunks arrive via `POST /v1/jobs/:id/stdin` → API PUBLISHes to `stdin:<jobId>` → owning worker writes to the process pipe.

**Trust boundary:** All trusted input (code, stdin, control) enters only through the bearer-authed API. soketi is **output-only** — nothing trusted enters via it. The soketi app secret is read from the environment only; it is never written to Redis or returned by any endpoint.

**Three-clocks model:** The worker enforces three independent resource clocks per sandbox — wall time (`wallTimeMs`), idle time (`idleMs`, killed when no output and no stdin), and CPU time (`cpuMs`, via cgroup). Any clock expiry kills the sandbox unconditionally.

## Quickstart

### Prerequisites

- **Docker Desktop** running (cgroup v2 required for resource limits)
- **Node.js 22+** and **pnpm 10+** (for the API and stub)
- **Go 1.26+** (for the worker)
- Ports `8080` (API) and `6001` (soketi) free on the host

### 1. Copy env

```bash
cp .env.example .env
# All defaults are safe for local dev.
```

### 2. Build language images

Build all four language sandbox images on the host Docker daemon (the worker mounts the host socket — no Docker-in-Docker):

```bash
make build-images
# Builds: executor/python:3.12  executor/rust:1.83  executor/r:4.4  executor/sqlite:3
```

### 3. Bring up the stack

```bash
docker compose up
# Starts: redis, soketi, api, worker
# (or: make up  —  equivalent to docker compose up --build)
```

The API will be available at `http://localhost:8080`. Health check: `GET /health` (no auth required).

### 4. Run the interactive E2E demo

```bash
make e2e
# Equivalent to: ./scripts/e2e.sh
```

The script starts the full stack, runs the stub (an interactive E2E driver), and tears down on exit. Expected output (abbreviated):

```
[stub] stdout: name?
[stub] detected prompt, sending stdin: World
[stub] stdout: hello World
[stub] result: exitCode=0 reason=exit durationMs=...
[stub] E2E PASS: hello World received + exitCode 0 + clean result
[PASS] ===== E2E PASS: interactive execute hello World round-trip succeeded =====
```

### 5. Tear down

```bash
make down
# Equivalent to: docker compose down -v
```

## API Reference

All `/v1/*` endpoints require:

```
Authorization: Bearer <EXECUTOR_API_TOKEN>
```

Missing or invalid token → `401 {"error":"unauthorized"}` (constant-time comparison).

---

### GET /health

**No authentication required.**

Returns `200 {"status":"ok"}`. Use this as a readiness probe.

---

### POST /v1/execute

Submit code for execution. Returns immediately (before any process starts) with a job ID and soketi channel.

**Request body:**

```json
{
  "language": "python",
  "version": "3.12",
  "files": [
    { "name": "main.py", "content": "name = input('name? ')\nprint(f'hello {name}')\n" }
  ],
  "limits": {
    "wallTimeMs": 30000,
    "idleMs": 10000,
    "cpuMs": 15000,
    "memoryMb": 128,
    "pids": 64,
    "outputKb": 512
  }
}
```

- `language` — language name or alias (e.g. `"python"`, `"py"`, `"rust"`, `"rs"`, `"sqlite"`, `"sql"`).
- `version` — optional; omit to use the only/most-recent match.
- `files` — array of `{name, content}` objects; at least one required.
- `limits` — optional per-request override; absent fields fall back to manifest defaults.

**Responses:**

| Code | Body | When |
|------|------|------|
| `202` | `{"jobId":"<uuid>","channel":"private-run-<jobId>","status":"queued"}` | Accepted; job enqueued. |
| `400` | `{"error":"...","details":[...]}` | Invalid body, unknown language, or unknown version. |
| `429` | `{"error":"Executor at capacity...","retryAfterMs":1000}` | Queue depth >= `MAX_QUEUE_DEPTH`. Checked after manifest resolution, so invalid requests get `400` not `429`. |

---

### POST /v1/jobs/:id/start

Send the start signal after subscribing to the soketi channel. See the [Start-handshake](#start-handshake) section.

**Responses:**

| Code | Body | When |
|------|------|------|
| `202` | `{"ok":true}` | Signal published. |

---

### POST /v1/jobs/:id/stdin

Write a chunk to the running process stdin.

**Request body:**

```json
{ "chunk": "World\n" }
```

**Responses:**

| Code | Body | When |
|------|------|------|
| `200` | `{"ok":true}` | Chunk published. |
| `400` | `{"error":"...","details":[...]}` | Invalid body. |
| `429` | — | Frame-rate or pending-byte cap exceeded. |

---

### POST /v1/jobs/:id/stdin/close

Signal EOF to the process stdin (equivalent to Ctrl-D).

**Responses:**

| Code | Body |
|------|------|
| `200` | `{"ok":true}` |

---

### POST /v1/jobs/:id/kill

Send SIGKILL to the sandbox.

**Responses:**

| Code | Body |
|------|------|
| `200` | `{"ok":true}` |

---

### GET /v1/jobs/:id

Poll job status.

**Responses:**

| Code | Body | When |
|------|------|------|
| `200` | `JobStatus` | Job found. |
| `404` | `{"error":"Job not found: <id>"}` | Unknown job ID. |

**JobStatus shape:**

```json
{
  "jobId": "<uuid>",
  "channel": "private-run-<uuid>",
  "language": "python",
  "version": "3.12",
  "state": "running",
  "updatedAtMs": 1700000000000
}
```

`state` enum: `queued` | `starting` | `running` | `done` | `killed` | `error`

---

### GET /v1/languages

Returns the list of available languages discovered from `languages/*/manifest.json`. Zero language identifiers are hardcoded.

**Response `200`:**

```json
[
  { "language": "python", "version": "3.12", "aliases": ["py","py3","python3"], "interactive": true },
  { "language": "rust",   "version": "1.83", "aliases": ["rs"],                  "interactive": true },
  { "language": "r",      "version": "4.4",  "aliases": ["R"],                   "interactive": false },
  { "language": "sqlite", "version": "3",    "aliases": ["sql"],                  "interactive": true }
]
```

---

### POST /v1/channel-auth

**Optional.** Only registered when `ENABLE_CHANNEL_AUTH=true`. Used by the stub for local demos. In production the upstream app handles channel auth directly (see [Channel Auth](#channel-auth)).

**Request body:**

```json
{ "socket_id": "123.456", "channel_name": "private-run-<jobId>" }
```

**Responses:**

| Code | Body | When |
|------|------|------|
| `200` | Pusher auth response `{"auth":"<key>:<hmac>"}` | Channel authorized. |
| `400` | `{"error":"Missing required fields: socket_id, channel_name"}` | Missing fields. |
| `403` | `{"error":"Only private-run-<jobId> channels can be authorized here"}` | Non-`private-run-` channel. |

---

### Start-handshake

The worker parks the job at the queue until it receives the start signal. The sequence ensures the client is subscribed to the soketi channel before output begins:

```
1. POST /v1/execute            → 202 {jobId, channel}
2. Subscribe soketi private-run-<jobId>   (confirm subscription)
3. POST /v1/jobs/:id/start     → 202 {ok:true}
4. Worker starts the sandbox   → output flows to soketi
```

For batch jobs (no stdin expected): follow the same sequence. The worker reclaims the slot if `/start` never arrives within `WORKER_WARMUP_MS`.

---

### Output events

All events are emitted on the soketi channel `private-run-<jobId>`. soketi delivers event data as a JSON-encoded string; parse it with `JSON.parse`. The contract is generated from `packages/contract/schema/wire.schema.json`.

#### `stage`

```json
{ "phase": "queued" }
```

`phase` enum: `queued` | `compiling` | `running`

#### `stdout` / `stderr`

```json
{ "chunk": "name? ", "seq": 0 }
```

- `chunk` — UTF-8 output text.
- `seq` — monotonic sequence number for ordering.

#### `result`

Terminal event. Emitted exactly once when the sandbox exits.

```json
{
  "exitCode": 0,
  "signal": null,
  "timedOut": false,
  "idleTimedOut": false,
  "truncated": false,
  "durationMs": 312
}
```

All fields are required. `exitCode` is `null` when the process was killed by signal. `timedOut` covers wall-clock and CPU-clock expiry; `idleTimedOut` covers idle-clock expiry.

## Channel Auth

Authorizing the browser's `private-run-<jobId>` soketi channel is the **upstream app's responsibility**, not a core function of code-runner.

**How it works:** The upstream app signs HMAC-SHA256 over `"<socket_id>:<channel_name>"` using `SOKETI_APP_SECRET` and returns `"<SOKETI_APP_KEY>:<hmac>"` as the Pusher auth response to the browser. This is the standard Pusher private-channel auth pattern.

**Trust boundary:** The app secret is read from the environment only. It is never written to Redis, never returned by any endpoint, and never sent to the browser. The secret stays inside the upstream/API trust boundary.

**Worked example:** `apps/stub/src/index.ts` implements the HMAC locally:

```ts
import { createHmac } from "node:crypto";
function signChannel(socketId: string, channelName: string): string {
  const stringToSign = `${socketId}:${channelName}`;
  const hmac = createHmac("sha256", SOKETI_APP_SECRET)
    .update(stringToSign)
    .digest("hex");
  return `${SOKETI_APP_KEY}:${hmac}`;
}
```

**Optional helper:** When `ENABLE_CHANNEL_AUTH=true`, the API registers `POST /v1/channel-auth` (see [API Reference](#post-v1channel-auth)). This helper is intended for local demos — it performs the same HMAC signing so a single service can handle both execution and channel auth. In production, your own backend should implement the signing using its copy of the app secret.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `EXECUTOR_API_TOKEN` | `dev-insecure-token-change-me` | Bearer token for API auth. Use `openssl rand -hex 32` in prod. |
| `REDIS_URL` | `redis://redis:6379` | Redis connection URL. Must be native TCP (`redis://` or `rediss://`). See [docs/redis-constraint.md](docs/redis-constraint.md). |
| `SOKETI_HOST` | `soketi` | Hostname of the soketi server. |
| `SOKETI_PORT` | `6001` | Port of the soketi server. |
| `SOKETI_USE_TLS` | `false` | Set `true` to connect to soketi over TLS. |
| `SOKETI_APP_ID` | `code-runner` | Pusher app ID. |
| `SOKETI_APP_KEY` | `code-runner-key` | Pusher app key (given to clients for WebSocket connections). |
| `SOKETI_APP_SECRET` | `code-runner-secret` | Pusher app secret. **Never returned by any endpoint; never written to Redis.** Change in prod. |
| `API_PORT` | `8080` | API listen port. |
| `WORKER_MAX_SANDBOXES` | `8` | Max concurrent live sandboxes per worker node. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker endpoint the worker uses. No Docker-in-Docker. |
| `SANDBOX_RUNTIME` | _(unset = runc)_ | Optional container runtime override. Set to `runsc` to enable gVisor. |
| `WORKER_WARMUP_MS` | `30000` | Slot reclaim timeout (ms): if `/start` never arrives after `/execute`, the slot is released. |
| `WORKER_HEARTBEAT_INTERVAL_MS` | `5000` | How often (ms) the worker writes its Redis heartbeat key. |
| `WORKER_HEARTBEAT_TTL_MS` | `20000` | TTL (ms) applied to the heartbeat key on each write. Must be several times the interval. |
| `MAX_QUEUE_DEPTH` | `256` | Maximum queue depth. `POST /v1/execute` returns `429` when `LLEN(jobs:queue) >= this`. |
| `API_BASE_URL` | `http://api:8080` | Base URL of the API as seen from the stub (inside docker compose). |
| `CHANNEL_AUTH_URL` | `http://api:8080/v1/channel-auth` | Channel-auth endpoint used by the stub. |
| `ENABLE_CHANNEL_AUTH` | `true` | Register the optional `POST /v1/channel-auth` helper. |

## Deployment

### Dev (docker compose)

The local stack runs redis, soketi, api, and worker. The stub is a separate on-demand service.

```bash
# Build all language images on the host daemon first
make build-images

# Start the stack
docker compose up          # or: make up

# Scale workers locally
docker compose up --scale worker=2

# Tear down
make down
```

### Production

**Scaling unit:** The long-lived **worker NODE** — a Go process that claims jobs from the Redis queue and launches sandboxes internally (up to `WORKER_MAX_SANDBOXES` concurrently). The scaling unit is the node, not a microVM per execution.

**Autoscaling by queue depth:** Scale the worker fleet by `LLEN(jobs:queue)`.

- **Fly.io:** use [`fly-autoscaler`](https://github.com/superfly/fly-autoscaler) with the Redis LLEN metric:
  ```toml
  [metrics.redis_queue_depth]
  type   = "redis"
  url    = "redis://your-redis-host:6379"
  metric = "llen"
  key    = "jobs:queue"

  [scaling]
  min   = 1
  max   = 50
  count = "min(50, max(1, qdepth / 2))"
  ```
- **Kubernetes:** use KEDA with the Redis scaler on `LLEN jobs:queue`, or an HPA with a custom metric from a Redis exporter.

**gVisor (extra sandbox isolation):** Set `SANDBOX_RUNTIME=runsc` on the worker. This passes `HostConfig.Runtime="runsc"` to every container launch — no worker code changes required. gVisor's Sentry kernel intercepts all syscalls and provides Firecracker-class isolation with no per-execution create latency.

**Redis:** The **worker requires native TCP Redis** (`redis://` or `rediss://`) — it uses `BRPOP` and `SUBSCRIBE`, which are blocking operations unavailable over HTTP REST. Upstash's REST tier is not viable for the worker (it is fine for the API). See [docs/redis-constraint.md](docs/redis-constraint.md).

**API:** stateless; can run anywhere (serverless, VMs, k8s). Does not need native Redis.

**soketi:** run as a sidecar or standalone service accessible from the worker.

See [docs/scaling.md](docs/scaling.md) for the full topology, fly-autoscaler LLEN example, scale-to-zero caveats, and the per-deploy-target table.

### Future / v2

**Kubernetes with RuntimeClass=gvisor:** Deploy workers as a `Deployment` with `SANDBOX_RUNTIME=runsc` and a `RuntimeClass=gvisor` node selector. Use KEDA's Redis scaler or an HPA custom metric for autoscaling.

**FlyMachinesRunner (v2, deferred):** A `Runner` backend that calls the Fly Machines REST API to create an ephemeral Firecracker microVM per execution. This gives per-execution isolation but costs seconds of create latency and has unproven interactive-stdin streaming semantics. It will be implemented as a parallel `Runner` backend once those trade-offs are resolved. See [PROJECT.md Key Decisions](.planning/PROJECT.md).

## Adding a Language

Languages are self-contained packages in `languages/<lang-version>/`. Adding a language requires **zero changes** to the Go worker or the API — only a new folder, a manifest, and a Dockerfile.

### Package model

```
languages/
  python-3.12/
    manifest.json
    Dockerfile
  rust-1.83/
    manifest.json
    Dockerfile
  sqlite-3/
    manifest.json
    Dockerfile
```

### Manifest fields

`manifest.json` defines everything the worker needs to run the language:

| Field | Type | Description |
|-------|------|-------------|
| `language` | `string` | Primary name (e.g. `"python"`, `"rust"`, `"sqlite"`). |
| `version` | `string` | Version string (e.g. `"3.12"`, `"1.83"`, `"3"`). |
| `aliases` | `string[]` | Alternative names accepted by `POST /v1/execute` (e.g. `["py","py3","python3"]`). |
| `image` | `string` | Pre-built Docker image (e.g. `"executor/python:3.12"`). |
| `entrypoint` | `string` | Main file name written into the sandbox workspace (e.g. `"main.py"`). |
| `compile` | `string[] \| null` | Compile command argv for compiled languages; `null` for interpreted languages. |
| `run` | `string[]` | Run command argv. |
| `interactive` | `boolean` | Whether the language supports live interactive stdin. |
| `defaultLimits` | `object` | Resource limits: `wallTimeMs`, `idleMs`, `cpuMs`, `memoryMb`, `pids`, `outputKb`. |

**Compile stage:** For compiled languages, set `compile` to the compiler argv. The worker runs the compile step first (with its own resource limits) and only starts the `run` command if compilation succeeds. For interpreted languages, set `compile` to `null`.

### Worked example: Rust (compiled language)

```json
{
  "language": "rust",
  "version": "1.83",
  "aliases": ["rs"],
  "image": "executor/rust:1.83",
  "entrypoint": "main.rs",
  "compile": ["rustc", "-O", "main.rs", "-o", "/workspace/prog"],
  "run": ["/workspace/prog"],
  "interactive": true,
  "defaultLimits": {
    "wallTimeMs": 120000,
    "idleMs": 15000,
    "cpuMs": 60000,
    "memoryMb": 512,
    "pids": 128,
    "outputKb": 1024
  }
}
```

The compiler runs `rustc -O main.rs -o /workspace/prog`; the run stage executes `/workspace/prog`.

### Worked example: SQLite (non-general-purpose tool)

```json
{
  "language": "sqlite",
  "version": "3",
  "aliases": ["sql"],
  "image": "executor/sqlite:3",
  "entrypoint": "main.sql",
  "compile": null,
  "run": ["sqlite3", "-batch", ":memory:", "-init", "main.sql"],
  "interactive": true,
  "defaultLimits": {
    "wallTimeMs": 30000,
    "idleMs": 10000,
    "cpuMs": 15000,
    "memoryMb": 64,
    "pids": 32,
    "outputKb": 512
  }
}
```

SQLite is not a general-purpose language — it is the `sqlite3` shell run against an ephemeral in-memory database. `compile` is `null`; the `.sql` file is passed with `-init`. This deliberately validates that the `language = image + compile? + run` abstraction holds for non-traditional runtimes.

### Steps to add a language

1. Create `languages/<lang-version>/manifest.json` and `languages/<lang-version>/Dockerfile`.
2. Build the image on the **host Docker daemon** (the worker mounts the host socket):
   ```bash
   make <lang>-image      # e.g. make python-image, make rust-image
   # or build all at once:
   make build-images
   ```
3. Restart the stack — the manifest loader auto-discovers the new folder at boot. No code changes needed.
4. Verify: `GET /v1/languages` should list the new language.

## Contributing

Contributions are welcome. The main constraint before merging a new language or a worker change is the safety gate:

**The abuse suite must be green before any new language merges.** The suite (`internal/worker/abuse_test.go`, build tag `abuse`) drives hostile jobs through the full worker path on real Linux cgroup v2 — OOM kills, CPU throttling, wall-time expiry, idle timeout, PID exhaustion, output truncation, and clean-exit containment.

Run it locally (requires Docker with cgroup v2, `redis:7` on port `6381`, and `executor/python:3.12`):

```bash
make python-image
docker run -d -p 6381:6379 redis:7
make abuse
```

The CI workflow (`.github/workflows/abuse.yml`) runs the suite on every pull request on ubuntu-latest (real cgroup v2). Enable **"abuse / abuse"** as a required status check on `main` so fan-out PRs cannot merge unless the abuse run is green.

### Make targets

| Target | Description |
|--------|-------------|
| `build-images` | Build all language sandbox images on the host daemon (python, rust, r, sqlite). |
| `up` | Bring up the local dev stack (`docker compose up --build`). |
| `down` | Tear down the local dev stack (`docker compose down -v`). |
| `e2e` | Run the end-to-end interactive demo against the local stack. |
| `abuse` | Run the adversarial abuse/safety suite (requires Docker cgroup v2 + redis:7 on port 6381 + executor/python:3.12). |
| `test` | Run all unit/integration tests (Go + JS). |
| `contract` | Regenerate the wire contract (TS types + Zod + Go structs). |
| `contract-check` | Fail if generated contract artifacts have drifted from the schema. |

## Interactive Flow

```
stub → POST /v1/execute  { language: python, files: [{name:main.py, content:...}] }
API  → 202 { jobId, channel: "private-run-<id>", status: "queued" }

worker ← BRPOP jobs:queue    # claims job
worker → SUBSCRIBE ctrl:<id> + stdin:<id>   # ready, waiting for /start

stub → subscribe soketi private-run-<id>   # channel auth (HMAC with app secret)
stub → POST /v1/jobs/:id/start             # start-handshake AFTER subscribe

API  → PUBLISH ctrl:<id> { type: "start" }
worker → sandbox.Start() → python main.py
sandbox stdout "name? " → worker → soketi channel → stub prints "name?"

stub → POST /v1/jobs/:id/stdin  { chunk: "World\n" }
API  → PUBLISH stdin:<id> "World\n"
worker → sandbox stdin pipe
sandbox stdout "hello World\n" → worker → soketi channel → stub prints "hello World"

sandbox exits 0 → worker → soketi result { exitCode: 0, reason: "exit" } → stub asserts
worker → cleanup (remove container, free slot, unsubscribe)
```

## Safety Gate

The adversarial abuse suite (`internal/worker/abuse_test.go`, build tag `abuse`) is the **required safety gate before any language is added**.

The suite drives 7 hostile Python jobs through the full worker path on real Linux cgroup v2, exercising OOM kills, CPU throttling, wall-time expiry, idle timeout, pid exhaustion, output truncation, and clean-exit containment. Behavior on macOS Docker Desktop diverges from production Linux; the CI gate closes that gap.

**CI workflow** (`.github/workflows/abuse.yml`):

- Runs on every pull request and on push to `main` (ubuntu-latest — real cgroup v2).
- Builds `executor/python:3.12` via `make python-image`, starts redis:7, then runs `make abuse`.
- Adding a new language reuses this same harness — the gate must pass for the new image before the PR merges.
- Repo owners should enable **"abuse / abuse"** as a required status check on `main` (branch protection) so fan-out PRs cannot merge unless the abuse run is green.

## License

MIT. See [LICENSE](LICENSE).
