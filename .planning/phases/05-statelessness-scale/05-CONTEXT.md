# Phase 5: Statelessness & Scale - Context

**Gathered:** 2026-06-03
**Status:** Ready for planning
**Mode:** Auto-generated (discuss skipped — autonomous run)

<domain>
## Phase Boundary

Make the API + workers safely horizontally scalable: slot-bounded capacity (acquire-before-claim), backpressure (429) instead of dropped work, a dead-worker reaper that prevents container/volume/slot leaks on worker death, N replicas, and a documented autoscaling-by-queue-depth + scale-to-zero design — where the **scaling unit is the worker node** (which launches its sandboxes internally and hosts N concurrent ones), NOT a microVM per execution.

Build ON the Phase 3 worker (`internal/worker`), jobstore/queue (`internal/jobstore`), DockerSocketRunner (anonymous-volume `/workspace`, labels `code-runner.jobId`), and the API (`apps/api`). Do NOT rebuild them.
</domain>

<decisions>
## Implementation Decisions

### SCALE-01 — statelessness / N replicas
Both API and worker are already stateless (coupled only via Redis + soketi). Make worker identity ephemeral: each worker gets a random `workerId` at boot. Validate via `docker compose up --scale worker=2`.

### SCALE-02 — slot capacity (acquire-before-claim)
Worker holds at most `WORKER_MAX_SANDBOXES` concurrent live sandboxes. Implement an in-process slot semaphore: the worker only `BRPOP`s the next job when a slot is free (acquire-before-claim — never claim work it can't run). Release the slot in the single `sync.Once` teardown. Capacity is counted in live sandboxes, not request bursts.

### SCALE-03 — backpressure → 429 (no silent drops)
Global backpressure so the API rejects rather than letting the queue grow unbounded:
- Track global in-flight + queued capacity in Redis. Simplest robust approach: the API checks `LLEN(keys.JobQueue)` (and/or a Redis-tracked in-flight counter) against a configurable `MAX_QUEUE_DEPTH`; if exceeded, `POST /v1/execute` returns `429` with a clear "at capacity, retry" message instead of enqueuing. (The per-job stdin rate-limit + pending-byte 429 already exists from Phase 3 — this is the JOB-admission 429.)
- Workers may publish their free-slot count to Redis (e.g. a `worker:<id>:slots` key or a global `capacity:free` counter via INCR/DECR) so the API's admission decision reflects real capacity; queue-depth alone is an acceptable MVP. Planner's discretion — keep it simple and correct.

### SCALE-04 — dead-worker reaper (no leaks)
- Each worker writes a heartbeat to Redis on an interval: `worker:<id>:heartbeat` with a TTL (a few × the interval) and records the jobIds it currently owns (e.g. a Redis set `worker:<id>:jobs`).
- A reaper (a goroutine in every worker, or a documented standalone mode) periodically: (a) lists host containers with label `code-runner.jobId` whose owning worker heartbeat is GONE (expired) → force-remove them WITH their anonymous volumes (`RemoveVolumes:true`) — the file-injection fix uses anonymous volumes, so the reaper MUST prune volumes too; (b) reclaims their slot accounting in Redis; (c) optionally marks the orphaned jobs' status as `error`. Use Docker label filters + `ContainerList(all)`.
- Also handle the normal-but-crashed case: a job whose worker died mid-session leaves a container — the reaper removes it. Assert no orphaned container/volume after a simulated worker death.

### SCALE-05 — autoscaling + scale-to-zero design
- Document (in `docs/scaling.md` or README section) the autoscaling-by-queue-depth model: scaling unit = the worker node (hosts N sandboxes); the fleet scales up when `LLEN(jobs:queue) > 0` and to zero when idle; native-protocol Redis required for the worker (Upstash API-only — CFG-04). Reference `fly-autoscaler` (`LLEN` metric) as the example mechanism. This is design/docs + whatever small hooks make it real (e.g. a metrics/health endpoint or a documented `LLEN` query).
- Keep it honest: don't claim per-execution microVM; FlyMachinesRunner is v2.

### Mechanics / testing
- Reuse the worker integration harness. Add tests: slot semaphore caps concurrency (enqueue > MAX jobs, assert only MAX run concurrently); API returns 429 when queue depth exceeded (apps/api test against real/mock Redis); reaper removes an orphaned labeled container+volume and frees the slot (create a container with the label + a dead/absent heartbeat, run the reaper, assert gone). Multi-worker: enqueue jobs across 2 workers, assert each stdin reaches only the owning worker (ownership-by-subscription) and capacity respected. Guard Docker/Redis tests with build tags + runtime skips; keep `go test ./...` green.
</decisions>

<canonical_refs>
## Canonical References — downstream agents MUST read
- `internal/worker/worker.go` (run loop, teardown — add the slot semaphore + heartbeat here), `internal/worker/integration_test.go` (harness)
- `internal/jobstore/*` (queue + status; add capacity/heartbeat keys), `internal/keys/keys.go` (add new key helpers; mirror in `packages/contract/src/index.ts` if the API needs them)
- `internal/runner/docker.go` (labels `code-runner.jobId`, RemoveVolumes; the reaper uses ContainerList + ContainerRemove)
- `internal/config/config.go` (WORKER_MAX_SANDBOXES, add MAX_QUEUE_DEPTH + heartbeat interval), `apps/api/src/routes/execute.ts` + `apps/api/src/ratelimit.ts` (job-admission 429)
- `.planning/research/ARCHITECTURE.md`, `.planning/research/PITFALLS.md` (orphaned containers, slot leaks, thundering herd), `.planning/research/STACK-API-CONTRACT-DEPLOY.md` (Upstash/native Redis, fly-autoscaler, scale-to-zero)
- `.planning/PROJECT.md` Key Decisions (scaling unit = worker node; FlyMachinesRunner v2), `docker-compose.yml`, `CLAUDE.md`
</canonical_refs>

<specifics>
## Specific Ideas

Phase 5 requirement IDs: SCALE-01, SCALE-02, SCALE-03, SCALE-04, SCALE-05.
ACTUALLY RUN the new tests (Docker + redis available). Prove: concurrency capped at MAX, 429 on over-capacity admission, reaper cleans an orphaned container+volume and frees the slot, and `docker compose up --scale worker=2` works (at least author + smoke-test the multi-worker routing). Paste real output where you run it.
</specifics>

<deferred>
## Deferred Ideas

gVisor/Fly runners (v2). Redis Streams guaranteed delivery (v2). The full README/deploy-per-target docs + broader CI matrix (Phase 7) — Phase 5 only needs the scaling doc note. Rust/R/SQLite (Phase 6).
</deferred>
