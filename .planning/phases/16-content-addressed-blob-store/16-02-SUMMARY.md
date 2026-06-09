# Phase 16 Plan 02: CAS edge (API presign + Node SDK + infra + docs) Summary

One-liner: The CAS edge on top of the 16-01 core — the API presigns blob PUT URLs (local crypto, no S3 call, no byte proxying) and checks/refreshes liveness in Redis via the verbatim monotonic-touch Lua; the Node SDK gains `blobs.upload` + transparent inline-vs-CAS routing in `executeFiles`; compose/.env wire the blob store (BYO-bucket, unconfigured => 501); plus a CAS guide doc.

## Commits (in order)

| Task | Commit | Description |
| ---- | ------ | ----------- |
| 1 | `e37af40` | feat(api): blob-store presign config (public endpoint, BYO bucket) |
| 2 | `adc74ed` | feat(api): /v1/blobs/check + /v1/blobs/finalize (presigned upload handshake) (BLOB-02/03/04) |
| 3 | `49ddae9` | feat(sdk-node): blobs.upload + transparent inline-vs-CAS routing in execute (BLOB-10/11) |
| 4 | `4c1b1c3` | chore(infra): wire blob store env into compose + .env.example (BLOB-12) |
| 5 | `3f01cb8` | docs(16): CAS blobs guide (check->PUT->finalize->execute, TTL/lease/GC, SDK, security) |
| 6 | (this doc) | docs(16): CAS edge execution summary |

## Changes per task

### Task 1 — API presign config (`e37af40`)
- `apps/api/src/config.ts`: added blob presign settings to `Config`:
  - `blobS3Endpoint` = `BLOB_S3_PUBLIC_ENDPOINT` ?? `ARTIFACT_S3_PUBLIC_ENDPOINT` ?? `AWS_ENDPOINT_URL_S3` ?? `ARTIFACT_S3_ENDPOINT` (presign against the **public** endpoint so the SDK can reach the store).
  - `blobS3Bucket` = `BLOB_S3_BUCKET` ?? `ARTIFACT_S3_BUCKET` ?? `BUCKET_NAME`.
  - `blobS3AccessKeyId`/`blobS3SecretAccessKey`/`blobS3Region` reuse `AWS_*`/`ARTIFACT_S3_*`.
  - `blobS3UseSsl` derived from the endpoint scheme (mirrors the Go `NewS3Store` Secure-from-scheme logic).
  - `blobIdleTtlSeconds` (`BLOB_IDLE_TTL`, default 24h — mirrors the worker), `blobUploadUrlTtlSeconds` (`BLOB_UPLOAD_URL_TTL`, default 15 min).
  - `blobStoreConfigured` = endpoint && bucket && accessKey && secret all present; gates the routes (unconfigured => 501).

### Task 2 — `/v1/blobs/check` + `/v1/blobs/finalize` (`adc74ed`)
- New `apps/api/src/blobPresign.ts`: `minio@8.0.7` JS client (pinned). `presignedPutObject(bucket, blobs/cas/<hash>, ttl)` — pure local crypto, **no S3 network call**. `parseEndpoint` mirrors the Go scheme-strip + bare-host + useSSL behavior so the JS and Go signers agree. Memoized presign-only client; `resetPresignClient()` test seam. Key = `blobs/cas/<full sha256:hex ref>` (identical to the Go BlobStore key + the Redis `blob:*` keys — one token end to end).
- New `apps/api/src/routes/blobs.ts`, `registerBlobsRoutes(app)`, wired into `app.ts` after control routes (bearer auto via `/v1/*`).
  - `POST /v1/blobs/check` (validated by generated `BlobCheckRequestSchema`): per hash, `EXISTS blob:meta:<hash>` in Redis (never S3). Present → **monotonic TTL touch** via ioredis `eval` of a **verbatim port of `internal/blobindex.touchScript`** (SADD index + HSETNX size/createdAt + PEXPIRE-only-if-longer) + push to `present[]`. Missing → presign a 15-min PUT to `blobs/cas/<hash>` against the public endpoint → push `{hash, uploadUrl}` to `missing[]`. De-dups repeated hashes preserving order.
  - `POST /v1/blobs/finalize` (validated by `BlobFinalizeRequestSchema`): per hash, record liveness via the **same** monotonic Lua (HSETNX meta + idle TTL + SADD index). **No byte read** — integrity is the worker's pull-time job.
  - Malformed hash (not `sha256:<64hex>`) → 400 (the generated schema's regex enforces it). Unconfigured store → 501.
- `apps/api/src/files.ts`: enforce the **content/ref XOR** in `validateFiles` (the generated `FileInputSchema` can't express XOR): both set, neither set, or a malformed `ref` → a new `kind: "ref"` error. Ref bytes are **excluded** from `MAX_FILES_BYTES` (CAS is exactly the large-file path). `apps/api/src/routes/execute.ts`: surface the `ref` error kind as `400 Invalid file ref: …`.
- Tests (`apps/api/test/blobs-route.test.ts`, `blobs-unconfigured.test.ts`, plus XOR cases in `execute-files.test.ts`): presigned-URL **shape** assertion (host == public endpoint, decoded path == `/<bucket>/blobs/cas/<hash>`, SigV4 query params present) without uploading; present → no upload URL + TTL extended monotonically; mixed missing+present; finalize records liveness; malformed/short hash → 400; bearer 401; unconfigured store → 501; `parseEndpoint` host/port/scheme forms.

### Task 3 — Node SDK upload + transparent routing (`49ddae9`)
- `packages/code-runner-sdk-node/src/client.ts`:
  - `client.blobs.upload(buffer, {ttlSeconds?})`: sha256 (`node:crypto`) → `POST /v1/blobs/check` → if missing, `PUT` raw bytes to `uploadUrl` via `fetchImpl` (client→store direct, sent as a `Uint8Array` view) → `POST /v1/blobs/finalize` → `{ ref }`. Already-present blob **skips** the PUT + finalize.
  - `client.blobs.check(hashes)`: low-level passthrough.
  - `executeFiles` is now async and routes per-file: a binary `{name, data: Buffer}` whose `byteLength > inlineThresholdBytes` (option, default 256 KiB) is uploaded to CAS and sent as `{name, ref}`; smaller binaries + text files take the Phase-15 inline path; raw `{name, ref}` and raw `FileInput`s pass through unchanged.
  - New `CodeRunnerClientOptions.inlineThresholdBytes`; exported `DEFAULT_INLINE_THRESHOLD_BYTES`, `BlobsApi`, `BlobUploadOptions`, and the blob wire types from `index.ts`.
- `packages/code-runner-sdk-node/src/files.ts`: `toFileInput` now **preserves `ref`** (a ref FileInput passes through with no inline content/encoding) — needed so raw `{name, ref}` survives `executeFiles` normalization.
- Tests (`test/blobs.test.ts`): recording fake-fetch asserts the exact call sequence — missing → `check → PUT → finalize`; present → only `check` (PUT skipped); small binary → inline base64 (no blob calls); large binary → `check/PUT/finalize/execute` and sent as `{name, ref}`; raw `{name, ref}` passthrough; text stays utf8. Plus a `toFileInput` ref-passthrough case in `files.test.ts`.

### Task 4 — Infra (`4c1b1c3`)
- `docker-compose.yml`:
  - **worker**: added `BLOB_S3_BUCKET` (BYO; empty → Go config defaults to `S3Bucket`), `BLOB_IDLE_TTL`, `BLOB_GC_INTERVAL`, `BLOB_GC_GRACE`. Blobs reuse the artifact S3 endpoint/creds already present.
  - **api**: added blob presign env — `BLOB_S3_PUBLIC_ENDPOINT` (unset → 501), `BLOB_S3_BUCKET` (defaults to `BUCKET_NAME`), `AWS_*` creds, `BLOB_IDLE_TTL`. Updated the "API needs no S3 creds" comment to reflect CAS presign (local crypto only).
- `.env.example`: documented `BLOB_S3_PUBLIC_ENDPOINT` (host-reachable for the local SDK; uncomment MinIO `ports:`) + `BLOB_UPLOAD_URL_TTL`, plus the existing BYO-bucket/TTL/GC knobs.
- `docker compose config` parses clean; verified `BLOB_S3_BUCKET` resolves to `code-runner-artifacts` (api, shared default) and empty (worker, Go-resolved).

### Task 5 — Docs + verify (`3f01cb8`)
- New `docs/blobs.md`: full CAS guide — the check→PUT→finalize→execute(ref) flow (with a sequence diagram), the dedup/skip-upload win, the `FileInput.ref` variant + content/ref XOR, the SDK `blobs.upload` + transparent-routing example, idle-TTL/lease/grace-window GC behavior, the config table (which env, which side, defaults), local-dev steps, and the security notes (API presigns/local crypto only; worker-only byte access + authoritative sha256 verify → no SSRF; single-trusted-caller dedup).
- `docs/input-files.md`: cross-linked to `blobs.md` (replaced the stale "not in this phase" note) and added the both/neither/malformed-ref `400` row.

## Test results

- `pnpm -r test` — **green**. apps/api: 96/96 (13 files, incl. 9 blobs-route + 2 unconfigured + 4 new XOR cases). sdk-node: 41/41 (incl. 6 new CAS + 1 ref-passthrough). code-runner-react: 20/20. contract: all pass. (Run with `REDIS_URL=redis://localhost:6380` against a throwaway redis, **stopped after** per the test convention.)
- `make contract-check` — **clean** (RC 0, no drift; this plan added no schema changes — all blob messages shipped in 16-01).
- `go build ./...` — **green** (RC 0).
- `go test ./...` — **16 packages OK, 0 FAIL**. The two pre-existing `internal/stdintransport` pub/sub flakes (documented by 16-01 in deferred-items.md) happened to pass this run; this plan touched **no Go code**, so neither a regression nor a fix.
- SDK + API typecheck (`tsc --noEmit`) — **clean**. (Fixed two pre-existing `FileInput.content`-optional typecheck breakages in `sdk-node/test/files.test.ts` left by the 16-01 contract change — `out.content!` assertions.)

## Deviations from Plan

### Auto-fixed / design choices (no user permission needed)

1. **[Rule 3 — Blocking] `toFileInput` did not preserve `ref`.** The Phase-15 `toFileInput` only handled `content`, so a raw `{name, ref}` handed to `executeFiles` lost its `ref` during normalization (a test caught it). Fixed `toFileInput` to pass a ref FileInput through unchanged. Commit `49ddae9`.

2. **[Rule 1 — Test, pre-existing] `sdk-node/test/files.test.ts` typecheck breakage.** 16-01 made `FileInput.content` optional (`*string`/`string | undefined`), which broke two `Buffer.from(out.content, …)` calls under `tsc`. The SDK runs tests via `node --test` (type-stripping, no typecheck) so `pnpm test` was green, but `pnpm typecheck` failed. Fixed with `out.content!` assertions to keep the SDK typecheck clean for the new code. Out-of-scope-but-trivial; logged here. Commit `49ddae9`.

3. **content/ref XOR placed in `validateFiles` (not a separate zod refine).** The plan/critical-rule asked for a zod `.refine` on the request. The API already centralizes file validation in `validateFiles` (called from `execute.ts`), and the worker enforces the XOR independently. Implementing the XOR there (rather than a parallel zod refine) keeps a single API-side validation path, returns the same `400`, and the generated `FileInputSchema.ref` regex still rejects a malformed ref at the schema layer. Functionally identical to a refine; cleaner. The malformed-hash 400 is covered both by the schema regex (zod) and `validateFiles`.

4. **`BLOB_UPLOAD_URL_TTL` env added (not named in the plan).** The plan said "TTL = a short upload window, e.g. 15 min" — I made it configurable (`BLOB_UPLOAD_URL_TTL`, default 900s) rather than hardcoding, and documented it. Minor additive knob.

5. **Local live E2E documented, not executed.** A headless full-stack E2E (bring up compose with MinIO ports + public endpoint, run a real job referencing a blob) is heavy and would leave state on the host. The presign signing, Redis liveness/touch, SDK call sequence, and worker pull/verify are all covered by unit + integration tests (API blob tests, SDK sequence tests, 16-01 `redis_integration`/`s3_integration`). Per the plan's stated fallback, the manual E2E steps are documented below. No architectural changes; no checkpoints hit.

## Dependency added

- **`minio@8.0.7`** (pinned, exact) in `apps/api`. Chosen over `@aws-sdk/s3-request-presigner` for **symmetry with the Go worker** (which uses `minio-go/v7`) and the running compose MinIO — same presign algorithm, same endpoint/port/useSSL semantics, so the API-signed URL and the worker's view of the object key/bucket stay in lockstep. `presignedPutObject` is pure local crypto (no network). Engines `>=20` satisfies the repo's Node `>=22`. Lockfile updated.

## Known Stubs

None. The edge is wired end to end: SDK `blobs.upload`/transparent routing → API presign + Redis liveness → (worker pull/verify/materialize/GC already shipped in 16-01). When the blob store is unconfigured the routes return a clean 501 and the SDK transparent path is simply unused (inline still works) — intentional graceful degradation, not a stub.

## Threat Flags

None new. The API presign surface (`/v1/blobs/*`) is exactly the trust boundary locked in 16-CONTEXT: API signs URLs (local crypto), bytes never transit the gateway, existence is Redis-only, the worker is the sole byte reader + authoritative sha256 verifier (no SSRF — it pulls only our configured store). Bearer auth is automatic via the central `/v1/*` middleware.

---

## PROD DEPLOY STEPS (blob store — NOT provisioned autonomously; documented only)

Prod object storage must be provisioned by a human. The worker auto-creates the bucket on boot **only if its credentials allow `CreateBucket`**; otherwise pre-create it. Steps:

### 1. Bucket
- Provision **one** S3-compatible bucket for code-runner (blobs + artifacts can share it — distinct prefixes `blobs/cas/` vs `artifacts/`), OR a dedicated blob bucket if you set `BLOB_S3_BUCKET`.
- On Fly/Tigris: `fly storage create` (Tigris) yields the bucket + `AWS_*` creds + endpoint. Same bucket already used for artifacts works — no second bucket required.
- **No S3 lifecycle rule for blobs** — blob expiry is the worker's Redis GC (idle TTL + lease + grace window), not bucket lifecycle. (The artifact `artifacts/` lifecycle rule is separate and unchanged.)

### 2. Worker env (Fly: `fly secrets set` / `[env]`)
Blobs reuse the artifact S3 config already set for the worker. Add only:
- `BLOB_S3_BUCKET` — **only if** splitting blobs into their own bucket (else omit; defaults to `BUCKET_NAME`).
- `BLOB_IDLE_TTL` (s, default `86400`), `BLOB_GC_INTERVAL` (s, default `600`), `BLOB_GC_GRACE` (s, default `1800`) — tune for your reuse/retention profile.
- Existing artifact S3 secrets (`AWS_ENDPOINT_URL_S3`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, `BUCKET_NAME`) are reused as-is.

### 3. API env (Fly: `fly secrets set` on the API app)
The API now presigns blob PUT URLs (local crypto). Set:
- `BLOB_S3_PUBLIC_ENDPOINT` — the **public** S3/CDN endpoint the SDK (your backend) can reach. If clients hit the same endpoint the worker uses, set it to that; if split-horizon, set the public domain. **If unset, `/v1/blobs/*` returns 501 and transparent CAS is disabled** (artifacts/inline files keep working).
- `BLOB_S3_BUCKET` — match the worker (or omit to default to `BUCKET_NAME`).
- `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION` — **the API now needs these** (to sign). They can be the same creds as the worker, or a **presign-only** key scoped to `PutObject` on `blobs/cas/*` (recommended least-privilege).
- `BLOB_IDLE_TTL` (match the worker), `BLOB_UPLOAD_URL_TTL` (s, default `900`) — presigned PUT-URL validity.

### 4. Verify in prod
- `POST /v1/blobs/check { hashes:["sha256:<64hex>"] }` with the bearer → expect `{ missing:[{hash, uploadUrl}], present:[] }` (501 means the API blob env is missing).
- `PUT` bytes to `uploadUrl`, `POST /v1/blobs/finalize`, then run a job with `files:[{name, ref}]` → the worker pulls + verifies + materializes. A sha256 mismatch/miss fails the job cleanly.

### Least-privilege note
The API key only needs `s3:PutObject` on `blobs/cas/*` (presign is local but the signed creds must be valid for the upload). The worker needs `GetObject`/`HeadObject`/`DeleteObject` (+`CreateBucket` if you want boot-time auto-create) on the blob prefix.

## Self-Check: PASSED

- Files exist: `apps/api/src/config.ts`, `apps/api/src/blobPresign.ts`, `apps/api/src/routes/blobs.ts`, `apps/api/test/blobs-route.test.ts`, `apps/api/test/blobs-unconfigured.test.ts`, `packages/code-runner-sdk-node/src/client.ts`, `packages/code-runner-sdk-node/test/blobs.test.ts`, `docs/blobs.md` — all present on disk.
- Commits `e37af40`, `adc74ed`, `49ddae9`, `4c1b1c3`, `3f01cb8` all present in git history.
