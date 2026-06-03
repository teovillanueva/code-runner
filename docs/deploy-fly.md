# Deploying code-runner to Fly.io (Camino C: dockerd-in-Machine)

This deploys four Fly apps in one org, talking over Fly's private 6PN network:

| App | Visibility | Role |
|-----|-----------|------|
| `code-runner-soketi` | **public** (wss) | browsers subscribe here for output |
| `code-runner-redis`  | private | job queue + stdin/control pub/sub (native Redis) |
| `code-runner-api`    | private | Hono gateway (your backend calls it with the bearer token) |
| `code-runner-worker` | private | runs its own dockerd + launches hardened sandboxes |

The worker is a Fly Machine (Firecracker microVM) that runs **its own dockerd** and
launches sandbox containers inside it. Strong isolation = the microVM boundary;
per-execution isolation = the hardened container. Sandbox language images are
pulled from GHCR on first boot (cached on the `docker_data` volume).

> **Cost heads-up:** this provisions real Machines + volumes. The worker needs
> ~2GB RAM + a ~10GB volume. Keep `min_machines_running` low and use
> `auto_stop`/scale-to-zero where possible.

---

## 0. Prerequisites

- `flyctl` installed and logged in (`fly auth whoami`).
- The GHCR images published (push a `v*` tag or run the `release-images` workflow):
  `gh workflow run release-images.yml`
- Make the language packages **public** (one-time) so the worker can pull without a token —
  Repo → Packages → each `executor-*` → Package settings → Change visibility → Public.
  (Or keep them private and set `GHCR_TOKEN` as a worker secret.)

Pick a region near you and replace `gru` in the four `fly.toml` files if desired
(e.g. `gru` São Paulo, `scl` Santiago, `bog` Bogotá).

---

## 1. Create the apps

```bash
fly apps create code-runner-soketi
fly apps create code-runner-redis
fly apps create code-runner-api
fly apps create code-runner-worker
```

## 2. Create the volumes

```bash
fly volumes create redis_data  -a code-runner-redis  -r gru -s 1   --yes
fly volumes create docker_data -a code-runner-worker -r gru -s 6   --yes
```

## 3. Generate + set secrets

The same `SOKETI_APP_SECRET` must be on soketi, api, and worker.

```bash
API_TOKEN=$(openssl rand -hex 32)
SOKETI_SECRET=$(openssl rand -hex 32)

fly secrets set -a code-runner-soketi SOKETI_DEFAULT_APP_SECRET="$SOKETI_SECRET"
fly secrets set -a code-runner-api    EXECUTOR_API_TOKEN="$API_TOKEN" SOKETI_APP_SECRET="$SOKETI_SECRET"
fly secrets set -a code-runner-worker SOKETI_APP_SECRET="$SOKETI_SECRET"

echo "Save this — your upstream app authenticates with it:"
echo "EXECUTOR_API_TOKEN=$API_TOKEN"
```

If your GHCR packages are private, also:
`fly secrets set -a code-runner-worker GHCR_TOKEN=<a GH PAT with read:packages>`

## 3a. Provision object storage for artifacts (Tigris)

Run artifacts (files the executed program writes) are uploaded by the **worker**
to an S3-compatible bucket and handed back as presigned URLs. On Fly this is
**Tigris** — `fly storage create` provisions a bucket and injects its credentials
as **secrets on the worker app**:

```bash
fly storage create -a code-runner-worker
```

This sets the following **secrets** on `code-runner-worker` automatically. The
worker's `S3Store` reads the standard `AWS_*` names with **zero translation** —
the same code that runs against the dev MinIO runs unmodified against Tigris:

| Tigris-injected secret (worker app) | S3Store env it satisfies | Notes |
|-------------------------------------|--------------------------|-------|
| `BUCKET_NAME`          | `BUCKET_NAME` (bucket) | the artifacts bucket |
| `AWS_ACCESS_KEY_ID`    | `AWS_ACCESS_KEY_ID`    | credential — secret, never in `[env]` |
| `AWS_SECRET_ACCESS_KEY`| `AWS_SECRET_ACCESS_KEY`| credential — secret, never in `[env]` |
| `AWS_ENDPOINT_URL_S3`  | `AWS_ENDPOINT_URL_S3`  | Tigris endpoint URL |
| `AWS_REGION`           | `AWS_REGION`           | region (e.g. `auto`) |

> If a provider ever injects differently-named vars, the worker also honors
> `ARTIFACT_S3_ENDPOINT` / `ARTIFACT_S3_BUCKET` / `ARTIFACT_S3_ACCESS_KEY_ID` /
> `ARTIFACT_S3_SECRET_ACCESS_KEY` / `ARTIFACT_S3_REGION` overrides — unused for
> Tigris since its names already match.

**The `code-runner-api` app needs NO S3 credentials** — only the worker
uploads, signs presigned URLs, and manages the bucket lifecycle.

**No manual bucket creation needed.** The worker auto-creates the bucket on boot
if it does not exist (`S3Store.EnsureLifecycle` → `BucketExists` + `MakeBucket`),
so even a bucket-less Tigris store works the first time. This is the same path
proven against a fresh dev MinIO (R14).

**Retention TTLs** are non-secret and live in `deploy/fly/worker/fly.toml`
`[env]` (`RUN_RESULT_TTL`, `PRESIGNED_URL_TTL` in seconds; `ARTIFACT_S3_OBJECT_TTL`
in days). The object TTL **must be ≥** the presigned-URL TTL or the worker fails
fast at boot (R15 ordering invariant). S3/Tigris bucket lifecycle expiration is
**whole-day granular** — sub-day cleanup is not achievable, so the object TTL is
expressed in days. code-runner storage is an **ephemeral handoff**: your backend
is expected to copy artifacts to its own durable store; code-runner relies on the
bucket lifecycle rule for cleanup and makes no long-term retention guarantee.

## 4. Deploy (order matters — deps first)

All deploys run from the **repo root** (the build context is the monorepo):

```bash
fly deploy -c deploy/fly/redis/fly.toml  -a code-runner-redis
fly deploy -c deploy/fly/soketi/fly.toml -a code-runner-soketi
fly deploy -c deploy/fly/api/fly.toml    --dockerfile apps/api/Dockerfile          -a code-runner-api    .
fly deploy -c deploy/fly/worker/fly.toml --dockerfile deploy/fly/worker/Dockerfile -a code-runner-worker .
```

The worker's **first** boot pulls the four language images from GHCR — watch it:

```bash
fly logs -a code-runner-worker   # expect: dockerd up → pulling executor-* → starting worker
```

## 5. Verify end-to-end

Tunnel to the private API and drive an interactive execute:

```bash
fly proxy 8080:8080 -a code-runner-api &     # local :8080 → private API

curl -s -X POST localhost:8080/v1/execute \
  -H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json" \
  -d '{"language":"python","files":[{"name":"main.py","content":"print(input())"}]}'
# → 202 {"jobId":"...","channel":"private-run-...","status":"queued"}
```

Then subscribe to the channel on `wss://<your-soketi-app>.fly.dev` (the soketi app's
public hostname — soketi is the one app exposed to browsers; signing the
auth with `SOKETI_APP_SECRET` — see the README "Channel Auth" section and
`apps/stub/src/index.ts`), `POST /v1/jobs/:id/start`, send stdin, and watch the
streamed output. `fly logs -a code-runner-worker` shows the sandbox lifecycle.

---

## Notes & hardening

- **dockerd-in-Machine caveats:** if dockerd fails to start, check `fly logs`.
  Common fixes: ensure the `docker_data` volume is mounted at `/var/lib/docker`,
  give the Machine enough memory, and confirm the Machine kernel supports
  `overlay2` (Fly's does). This is the fiddly part of Camino C.
- **API exposure:** the API is private by design. Expose it (gated by the token)
  by adding an `[http_service]` to `deploy/fly/api/fly.toml`, or keep it private
  and have your upstream backend reach it at `code-runner-api.internal:8080`.
- **Redis password:** set `--requirepass` + a secret and update `REDIS_URL` for
  defense-in-depth (the app is already 6PN-private).
- **Scaling:** raise `WORKER_MAX_SANDBOXES` + the worker VM size together. Run
  multiple worker Machines for more capacity; the queue + ownership-by-subscription
  route work correctly across replicas (see `docs/scaling.md`).
- **Upgrade path:** for per-execution Firecracker isolation, the v2 `FlyMachinesRunner`
  (one Machine per execution via the Fly Machines API) replaces dockerd-in-Machine.
