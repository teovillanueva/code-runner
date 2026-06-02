# code-runner

An **open-source (MIT), self-hostable** remote code execution service with live interactive stdin. Run untrusted code in a hardened sandbox and stream output in real time via soketi (Pusher-compatible).

> Full documentation, add-a-language guide, and deployment targets are coming in Phase 7. This section covers the local dev quickstart.

## Quickstart

### Prerequisites

- **Docker Desktop** running (cgroup v2 recommended; needed for the worker's resource limits)
- **Node.js 22+** and **pnpm 10+** (for the API and stub)
- **Go 1.26+** (for the worker)
- Ports `8080` (API) and `6001` (soketi) free on the host

### 1. Copy env

```bash
cp .env.example .env
# Review .env — all defaults are safe for local dev.
```

### 2. Build the Python sandbox image

The worker launches Python code inside a sandboxed container image (`executor/python:3.12`).
This image must exist on the **host Docker daemon** (the worker mounts `/var/run/docker.sock` and talks to the host runtime directly — no Docker-in-Docker).

```bash
make python-image
# Builds executor/python:3.12 from languages/python-3.12/Dockerfile
```

### 3. Bring up the stack

```bash
make up
# Starts: redis, soketi, api, worker
# Equivalent to: docker compose up --build
```

The API will be available at `http://localhost:8080`. Health check: `GET /health`.

### 4. Run the interactive E2E demo

```bash
make e2e
# Equivalent to: ./scripts/e2e.sh
```

The script:
1. Ensures `.env` is present
2. Builds `executor/python:3.12` if not already built
3. Brings up the full stack (redis + soketi + api + worker)
4. Waits for the API health endpoint
5. Runs the **stub** — an interactive E2E driver that:
   - `POST /v1/execute` a Python program that calls `input()`
   - Subscribes to the `private-run-<jobId>` soketi channel
   - `POST /v1/jobs/:id/start` (start-handshake after subscription)
   - Sends `World\n` as stdin when it sees the input prompt
   - Receives and prints the streamed `hello World` stdout
   - Awaits the terminal `result` event with `exitCode=0`
6. Asserts `hello World` was received and exits 0
7. Tears down (`docker compose down -v`)

Expected output (abbreviated):
```
[stub] stdout: name?
[stub] detected prompt, sending stdin: World
[stub] stdout: hello World
[stub] result: exitCode=0 reason=exit
[stub] E2E PASS: hello World received + exitCode 0 + clean result
[PASS] ===== E2E PASS: interactive execute hello World round-trip succeeded =====
```

### 5. Tear down

```bash
make down
# Equivalent to: docker compose down -v
```

## Interactive Flow

```
stub → POST /v1/execute  { language: python, files: [{name:main.py, content:...}] }
API  → 202 { jobId, channel: "private-run-<id>", status: "queued" }

worker ← BRPOP jobs:queue    # claims job
worker → SUBSCRIBE ctrl:<id> + stdin:<id>   # ready, waiting for /start

stub → subscribe soketi private-run-<id>   # channel auth via /v1/channel-auth
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

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `EXECUTOR_API_TOKEN` | `dev-insecure-token-change-me` | Bearer token for API auth (change in prod) |
| `REDIS_URL` | `redis://redis:6379` | Redis connection URL (must be native TCP) |
| `SOKETI_HOST` | `soketi` | soketi hostname |
| `SOKETI_PORT` | `6001` | soketi port |
| `SOKETI_APP_ID` | `code-runner` | Pusher app ID |
| `SOKETI_APP_KEY` | `code-runner-key` | Pusher app key (given to clients) |
| `SOKETI_APP_SECRET` | `code-runner-secret` | Pusher app secret (**never returned by any endpoint**) |
| `API_PORT` | `8080` | API listen port |
| `WORKER_MAX_SANDBOXES` | `8` | Max concurrent live sandboxes per worker |
| `WORKER_WARMUP_MS` | `30000` | Warm-up timeout (ms) before /start reclaims slot |
| `ENABLE_CHANNEL_AUTH` | `true` | Enable the optional channel-auth helper (CHAN-02) |

## Channel Auth Trust Boundary

Private soketi channel authorization (`private-run-<jobId>`) is the **upstream app's responsibility** — not this service's core function. In a real deployment, your backend handles channel auth using the Pusher HMAC pattern with `SOKETI_APP_KEY` and `SOKETI_APP_SECRET`.

For local demos, this service provides an optional helper at `POST /v1/channel-auth` (enabled via `ENABLE_CHANNEL_AUTH=true`). The stub uses this helper. The soketi secret is read from env only and is **never written to Redis or returned by any endpoint**.

## Scaling & Statelessness

Both the API and workers are **stateless** — all shared state lives in Redis. This makes horizontal scaling trivial.

**Local multi-worker dev:**
```bash
docker compose up --scale worker=2
```
Two worker instances share the same Redis queue. Jobs are distributed via `BRPOP` (first worker to claim wins); stdin frames route only to the owning worker via `PUBLISH`/`SUBSCRIBE` ownership.

**Global backpressure:** `POST /v1/execute` returns HTTP 429 when `LLEN(jobs:queue) >= MAX_QUEUE_DEPTH` (default 256). Clients receive a clear retry message; work is never silently dropped.

**Autoscaling:** Scale worker nodes by queue depth. On Fly.io, use [`fly-autoscaler`](https://github.com/superfly/fly-autoscaler) with `LLEN jobs:queue` as the metric:
```
FAS_CREATED_MACHINE_COUNT = "min(50, max(1, qdepth / 2))"
```
On Kubernetes, use [KEDA](https://keda.sh) with the Redis scaler or an HPA custom metric.

See **[docs/scaling.md](docs/scaling.md)** for the full scaling model: worker-node topology, per-node slot cap, fly-autoscaler LLEN example, scale-to-zero caveats, and the native-Redis requirement.

## Architecture

See `.planning/research/ARCHITECTURE.md` for the full architecture. Key points:

- **No Docker-in-Docker**: the worker mounts `/var/run/docker.sock` and is a client of the host daemon
- **Language images must be built on the host**: `make python-image` (or `make build-images` for all)
- **Worker talks only to Redis + soketi**: no direct API ↔ worker HTTP calls
- **Start handshake**: `POST /start` only after the client has subscribed to the soketi channel

## Safety Gate

The adversarial abuse suite (`internal/worker/abuse_test.go`, build tag `abuse`) is the **required safety gate before any language is added** in the language fan-out phase (Phase 6: Rust, R, SQLite, etc.).

The suite drives 7 hostile Python jobs through the full worker path on real Linux cgroup v2, exercising OOM kills, CPU throttling, wall-time expiry, idle timeout, pid exhaustion, output truncation, and clean-exit containment. Behavior on macOS Docker Desktop diverges from production Linux; the CI gate closes that gap.

**CI workflow** (`.github/workflows/abuse.yml`):

- Runs on every pull request and on push to `main` (ubuntu-latest — real cgroup v2).
- Builds `executor/python:3.12` via `make python-image`, starts redis:7, then runs `make abuse`.
- Adding a new language reuses this same harness — the gate must pass for the new image before the PR merges.
- Repo owners should enable **"abuse / abuse"** as a required status check on `main` (branch protection) so fan-out PRs cannot merge unless the abuse run is green.

**Run locally** (requires Docker with cgroup v2, redis:7, and `executor/python:3.12`):

```bash
# One-time setup
make python-image
docker run -d -p 6381:6379 redis:7

# Run the suite
make abuse
```
