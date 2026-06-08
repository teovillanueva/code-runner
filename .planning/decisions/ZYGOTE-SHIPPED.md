# v1.1 Density / ZygoteRunner — SHIPPED status (2026-06-09)

## What shipped (all on `main`)
- **Phase 10** — `preimport` manifest field + contract regen + Python preimport set; `manifest.ZygoteEligible` routing predicate. (R's preimport later removed → Docker tier.)
- **Phase 11** — Python zygote agent (`languages/python-3.12/zygote_agent.py`): pre-import, framed TCP relay, double-fork + per-child hardening (distinct UID, PID/NET/MNT ns, no_new_privs, private /tmp, per-child cgroup-v2 memory.max/pids.max, fd-scrub>2), CoW. R agent + C helper are WIP → R ships on the Docker tier (see ZYGOTE-R-STATUS.md).
- **Phase 12** — Go `ZygoteRunner` + `zygoteSandbox` + warm per-(lang,version) pool + relay client, behind the existing Runner/Sandbox interface (CPUReader, sync.Once cleanup, full-tree kill via cgroup.kill, three-clock compatible).
- **Phase 13** — `TieredRunner` (Python→zygote, R/Rust/SQLite→Docker), worker wiring, config gating (ZYGOTE_ENABLED default off), Fly worker deploy config.
- **Phase 14** — zygote safety/abuse/isolation/density/no-leak suite (ZTEST-01..04) + pool OTel metrics (ZOBS-01..02) + the `scripts/zygote-suite.sh` gate + the CI `zygote` job.
- **Resilience** — `TieredRunner` falls back to the Docker tier on any zygote Create error (logged + `code_runner.zygote.fallback.count`), so enabling zygote can never break Python.
- **Docs** — `docs/zygote.md` (+ deploy-fly/README pointers).

## Validation
- `go test ./...` green; full TS typecheck green; contract drift clean.
- **Seam + isolation + density + no-leak** ran and PASSED on native-Linux GitHub CI (the new `zygote` job) and locally in a bridge container.
- **cgroup-enforced cases** (fork bomb / OOM / cpu-clock) can only run where cgroup-v2 is delegated; they skip on Docker Desktop + GitHub runners and were proven on a Firecracker-class host by spike 006 (cg_ok=81/81, memory.max+pids.max contained).
- **On the real Fly worker (2026-06-09):** confirmed the agent creates its delegated cgroup subtree (`/sys/fs/cgroup/docker/<id>/zygote`) → per-child containment is active in prod.

## Prod state (Fly, org edalef)
- Worker image **deployed**: `code-runner-worker-fly:sha-2ddd3b2`, `ZYGOTE_ENABLED=true`. Worker boots clean (verified): "zygote tier enabled — tiered runner active", 4 languages loaded, redis/publisher/artifacts/reaper up.
- Agent-baked `executor/python:3.12` refreshed **in-place on all 3 pool nodes** (volumes persist it).
- Restored to **scale-to-zero** (all workers + redis stopped; api/soketi/autoscaler left as-found — note: the autoscaler was already stopped before this work).
- No Fly test machines were created (validated via local container + CI + in-place `docker run --rm` probes).

## Remaining ops follow-up (NOT blocking; safe due to fallback)
1. **Golden-snapshot re-bake** so AUTOSCALER-CREATED new machines also carry the agent-baked image (the existing 3 nodes are already refreshed in-place). Run when convenient:
   `APP=code-runner-worker IMAGE=ghcr.io/teovillanueva/code-runner-worker-fly:sha-2ddd3b2 bash deploy/fly/worker/provision-pool.sh bake`
   then `GOLDEN_SNAPSHOT=<id> deploy/fly/worker/provision-pool.sh grow <N>` (recreate pool volumes from it). Until then, a fresh autoscaled node with a stale image simply falls back to Docker for Python (still correct, just no density gain — watch `code_runner.zygote.fallback.count`).
2. **Live E2E job through the API** for all 4 languages was not run here (needs `EXECUTOR_API_TOKEN`, a Fly secret). Every layer below it is validated + fallback-protected; the first real Python job will engage zygote (confirm via the fallback counter staying flat + the `warm_parents` gauge).
3. **R on zygote** — currently Docker tier; promote later via the C-helper path in ZYGOTE-R-STATUS.md.

## How to confirm zygote is actually engaged (not silently falling back)
- Metric `code_runner.zygote.fallback.count` stays ~0 for Python jobs.
- Metric `code_runner.zygote.pool.warm_parents{language=python}` ≥ 1 under Python load.
- No `WARN zygote Create failed; falling back to Docker tier` lines in worker logs.
