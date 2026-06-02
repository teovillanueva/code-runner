# Scaling & Statelessness

This document describes the scaling model for code-runner: autoscaling by queue depth, scale-to-zero, the worker-node topology, and deploy-target specifics.

---

## Architecture Overview

```
  Client / Upstream App
         │
         ▼
  ┌──────────────┐
  │  API (Hono)  │  N replicas — stateless
  └──────┬───────┘
         │  LPUSH jobs:queue
         ▼
  ┌──────────────┐
  │    Redis     │  native TCP (pub/sub + blocking ops required by worker)
  └──────┬───────┘
         │  BRPOP / SUBSCRIBE stdin:<jobId>
         ▼
  ┌────────────────────────────────┐
  │  Worker NODE (Go, long-lived)  │  M replicas — stateless, scaled by queue depth
  │                                │
  │  ┌──────────┐  ┌──────────┐   │
  │  │ Sandbox  │  │ Sandbox  │ … │  up to WORKER_MAX_SANDBOXES per node
  │  └──────────┘  └──────────┘   │
  └────────────────────────────────┘
         │  Pusher HTTP trigger
         ▼
  ┌──────────────┐
  │   soketi     │  WebSocket fan-out (output only)
  └──────────────┘
```

---

## 1. Scaling Unit: the Worker NODE (not a microVM per execution)

The **scaling unit is the worker node** — a long-lived Go process that:

- Claims jobs from the Redis queue (`BRPOP jobs:queue`)
- **Launches sandboxes internally** via the host container runtime (`DockerSocketRunner`)
- Hosts up to `WORKER_MAX_SANDBOXES` concurrent live sandbox sessions
- Subscribes to each live job's stdin channel for the duration of the session

Each worker is **stateless**: it carries no persistent per-job state beyond its in-memory session slots and its ephemeral `workerId`. All shared state lives in Redis and soketi. This allows N identical worker replicas to run in parallel — a job enqueued by any API replica will be claimed by whichever worker wins the `BRPOP`.

**Stdin routing uses ownership-by-subscription:** the worker that claims a job immediately `SUBSCRIBE`s to `stdin:<jobId>`. When the API publishes a stdin frame (`PUBLISH stdin:<jobId> ...`), only the owning worker receives it — no coordination layer needed.

### v1 vs v2 sandbox backends

| Backend | Sandbox model | Status |
|---------|--------------|--------|
| `DockerSocketRunner` | Worker creates containers via the **mounted host Docker socket**; one container per sandbox, isolated with seccomp + cap-drop + read-only rootfs + NetworkMode=none | **v1 (current)** |
| `gVisorRunner` | Same Docker socket model with `HostConfig.Runtime="runsc"` (Firecracker-class Sentry isolation); no Worker code changes | **v1 upgrade path** |
| `FlyMachinesRunner` | Worker calls the Fly Machines REST API to create an **ephemeral Firecracker microVM per execution**; per-exec isolation at the cost of seconds of create latency + unproven interactive-streaming semantics | **v2 (deferred)** — see [Key Decisions](../.planning/PROJECT.md) |

> `FlyMachinesRunner` (microVM-per-execution) is **v2**. Per PROJECT.md Key Decisions, the Firecracker isolation is available today via gVisor `--runtime=runsc` on a long-lived worker node without the per-execution create-latency cost. `FlyMachinesRunner` will be implemented as a parallel `Runner` backend once interactive-stdin streaming through the Machines API is validated.

---

## 2. Capacity and Backpressure

### Per-node slot cap (SCALE-02)

Each worker node has a bounded concurrency limit (`WORKER_MAX_SANDBOXES`, default 8). The worker acquires a slot **before** claiming a job from the queue; when all slots are occupied, the `BRPOP` blocks without claiming new work — the queue depth naturally grows, which is the scale-up signal.

### Global admission gate: POST /execute → 429 (SCALE-03)

When `LLEN(jobs:queue) >= MAX_QUEUE_DEPTH` (env: `MAX_QUEUE_DEPTH`, default 256), the API returns **HTTP 429** with:

```json
{
  "error": "Executor at capacity (queue depth N ≥ 256). Retry shortly.",
  "retryAfterMs": 1000
}
```

This is **distinct** from the per-job stdin rate-limit 429 in `apps/api/src/ratelimit.ts`:

| Gate | Trigger | Route |
|------|---------|-------|
| Admission | `LLEN(jobs:queue) >= MAX_QUEUE_DEPTH` | `POST /v1/execute` |
| Stdin rate | Frame rate per job | `POST /v1/jobs/:id/stdin` |
| Stdin pending bytes | Un-drained pending bytes per job | `POST /v1/jobs/:id/stdin` |

Admission rejects work **before writing anything to Redis** — no spec, no status, no queue entry is created for a rejected request. Clients receive a clear retry signal; work is never silently dropped.

---

## 3. Autoscaling by Queue Depth (SCALE-05)

Scale the **worker fleet** based on the length of `jobs:queue`. More depth = more workers needed; empty queue = scale toward zero.

### Dev / docker compose

```bash
# Start two workers locally (no fixed container_name or host port on the worker service):
docker compose up --scale worker=2

# Confirm both workers are running:
docker compose ps worker
```

Both workers share the same Redis and soketi. Jobs posted to the API are distributed between them via `BRPOP` (first worker to pop wins). Stdin frames route only to the owning worker by `PUBLISH`/`SUBSCRIBE` ownership.

### Fly.io (recommended prod target)

**Tool: `fly-autoscaler` v0.3.1** — the official Fly metrics-based autoscaler for background workers.

`fly-autoscaler` polls a metric source on a ~15-second reconcile loop and starts/stops Fly Machines to match the desired count. Example configuration:

```toml
# fly-autoscaler.toml
[autoscaler]
app = "code-runner-worker"

[metrics.redis_queue_depth]
type   = "redis"
url    = "redis://your-redis-host:6379"
metric = "llen"
key    = "jobs:queue"

[scaling]
min = 1
max = 50
# Scale: 1 worker per 2 queued jobs, capped at 50.
# fly-autoscaler always keeps ≥1 Machine running (warm floor).
count = "min(50, max(1, qdepth / 2))"
```

**Important caveats:**

1. **fly-autoscaler will NOT scale workers to zero** — it always keeps ≥1 Machine running (the warm floor). This is by design to avoid cold-start latency on the next job arrival.
2. For **true scale-to-zero** (e.g. a demo deployment with long idle periods), pair `fly-autoscaler` for scale-up with Fly Machines **auto-stop** (`auto_stop_machines = "stop"` in `fly.toml`) for idle-Machine teardown. The auto-start-on-traffic feature does NOT apply to workers (they have no HTTP traffic), so a dedicated wake-on-enqueue mechanism is needed for genuine zero.
3. The recommended default for a live code-execution service is **warm floor of 1 + scale-up by `LLEN jobs:queue`** — this matches how the per-node slot cap works: one warm worker handles bursts up to `WORKER_MAX_SANDBOXES` concurrent sessions, and additional workers come online as the queue grows.

**Metric source note:** the example above reads `LLEN jobs:queue` directly from Redis. If your Redis is Upstash (REST-only) for the API, you cannot point `fly-autoscaler` at it for blocking reads — use a small sidecar exporter or a native-TCP Redis; see §5.

### Kubernetes

Deploy workers as a `Deployment` with `WORKER_MAX_SANDBOXES` set, and use an **HPA (Horizontal Pod Autoscaler)** backed by a custom metric from a Redis exporter (e.g. `redis-exporter` → Prometheus Adapter → HPA `type: External` on `llen_jobs_queue`). Target ~2 queued jobs per worker. Pair with `keda/ScaledObject` (KEDA) for simpler queue-depth autoscaling — KEDA's `redis` scaler natively supports `LLEN`.

---

## 4. Dead-Worker Reaper (SCALE-04)

Workers maintain a heartbeat key in Redis. If a worker dies mid-job, a reaper process (see `internal/reaper`) detects the stale heartbeat, marks owned jobs as failed, and releases their slots. This prevents "stuck" jobs blocking queue consumers indefinitely.

---

## 5. Native Redis Requirement (CFG-04)

**The worker requires a native TCP Redis connection** — API-only serverless Redis implementations such as Upstash's REST/HTTP tier are NOT viable for the worker.

The worker relies on:
- `SUBSCRIBE` / `BRPOP` — persistent blocking connections (not available over HTTP REST)
- `XREAD BLOCK` / `XREADGROUP` — planned Streams upgrade path

The **API process** (`apps/api`) is Upstash-safe — it only issues non-blocking commands (`LPUSH`, `PUBLISH`, `GET`, `SET`). So an asymmetric setup is valid:

```
API     → Upstash (REST-OK, non-blocking only)
Worker  → Native Redis/Valkey TCP (REQUIRED)
```

See [docs/redis-constraint.md](./redis-constraint.md) for full details and deployment options.

---

## 6. Scaling Summary by Deploy Target

| Target | API replicas | Worker scaling | Redis | Notes |
|--------|-------------|----------------|-------|-------|
| **Dev (docker compose)** | 1 | `--scale worker=N` | local Redis 7 | `docker compose up --scale worker=2` |
| **Fly.io** | N Machines | `fly-autoscaler` LLEN metric | Native Fly Redis / managed Valkey | warm floor ≥1; scale-to-zero via auto-stop |
| **Kubernetes** | HPA by CPU/RPS | HPA/KEDA by `LLEN jobs:queue` | Redis Cluster / MemoryDB | `RuntimeClass=gvisor` for sandbox isolation |
| **Single server** | 1 process | N worker processes (systemd) | local Redis | simple self-host; no autoscaling needed |

---

## See Also

- `docs/redis-constraint.md` — native Redis requirement detail
- `apps/api/src/admission.ts` — queue-depth admission gate implementation
- `apps/api/src/config.ts` — `MAX_QUEUE_DEPTH` config
- `.planning/research/STACK-API-CONTRACT-DEPLOY.md §3.4` — fly-autoscaler research notes
- `.planning/PROJECT.md` — Key Decisions (scaling unit, FlyMachinesRunner v2)
