# Phase 16 — Content-Addressed Blob Store (CAS) · Context

**Goal:** Dedupe large/shared input files across runs via a content-addressed (sha256) blob store that code-runner OWNS. Skip re-upload via an existence check; bytes go client→store directly (presigned); the worker streams blobs into the sandbox and verifies integrity. Redis tracks liveness (idle TTL) + per-run leases so GC never deletes an in-use blob.

**Requirements:** BLOB-01..BLOB-12 (see REQUIREMENTS.md).

## Locked architecture (resolved against the real codebase — do NOT re-litigate)

### Who presigns — **the API presigns PUT URLs**
- Presigning is pure local crypto, **no S3 network call** (confirmed: `internal/artifactstore/s3.go:71-74`). So the API can issue presigned PUT URLs without ever contacting or proxying bytes to S3.
- The Phase-9 note "API needs no S3 creds" was scoped to *artifacts*; CAS upload is an API-mediated handshake. We give the API **blob-store presign config** (endpoint/public-endpoint/bucket/creds/ttl). The API still: never streams file bytes, never reads/writes objects, makes zero S3 network calls (presign is local). The thin-gateway invariant (no byte proxying) holds.
- The **worker** remains the ONLY party that reads/writes blob bytes and is the **authoritative sha256 verifier** (on pull). No SSRF: the worker pulls only from our own configured store at a known endpoint, never a client-supplied URL.

### Store
- **Reuse the existing artifact S3/minio infra.** Same minio service in `docker-compose.yml`. Blob objects live under a distinct key prefix `blobs/cas/<sha256>` (artifacts use `artifacts/`). Extend the artifact store, or add a sibling `BlobStore`, sharing `NewS3Store`-style construction from `config.Config`.
- Add to the Go store: `PresignedPutObject`-equivalent (for the API path we presign in TS, but the worker also needs **streaming GET** — `GetObject` returning an `io.ReadCloser`) and `StatObject`/exists + `RemoveObject` (for GC).
- The API (TS) presigns PUT using a TS S3 presigner. Prefer the minimal dependency: `minio` JS client (matches the Go side's minio-go and the existing minio service) OR `@aws-sdk/s3-request-presigner`. Pick one, pin it, justify in the summary. Presign against the **public** endpoint (split-horizon) so the SDK can reach it.

### Redis liveness + lease + GC (bytes in S3, liveness in Redis)
- Keys (add to `internal/keys` Go + `packages/contract/src/index.ts` `keys`, in lockstep):
  - `blob:meta:<sha256>` → small hash `{size, createdAtMs}` with an **idle TTL** (`EXPIRE`). Touch-on-use bumps it; bump is **monotonic** — only ever extend (`EXPIRE` to `max(currentTTL, requested)`), via a tiny Lua `EVAL` (no Lua exists yet — add it).
  - `blobs:index` → SET of all known hashes (GC enumeration).
  - `blob:lease:<sha256>` → SET of active jobIds currently referencing the blob. Non-empty ⇒ pinned.
  - `blobs:gc:lock` → `SET NX PX` lock so only one worker replica GCs at a time.
- **Lease lifecycle:** when the worker claims a job that references blobs, `SADD blob:lease:<hash> <jobId>` for each ref (and touch `blob:meta`); on any terminal path (result/kill/timeout/error), `SREM`. Idempotent with the existing once-only cleanup.
- **GC (worker goroutine, periodic, lock-guarded):** for each hash in `blobs:index`: collectable iff `blob:meta:<hash>` is gone (TTL expired) AND `blob:lease:<hash>` is empty AND it has stayed in that state past a **grace window** (track first-seen-expired in a `blobs:gc:candidates` ZSET scored by timestamp; only delete when `now - score > grace`). Delete the S3 object + remove from index/candidates. Log what was reclaimed (no silent caps).

### Upload + run flow
1. SDK computes `sha256(buffer)` locally.
2. `POST /v1/blobs/check { hashes: ["<hex>", ...] }` → API checks `EXISTS blob:meta:<hash>` for each; for **missing** returns `{ hash, uploadUrl }` (presigned PUT to `blobs/cas/<hash>`). For present, touches the TTL (extend).
3. SDK `PUT`s missing bytes to the presigned URL (client→store direct).
4. SDK `POST /v1/blobs/finalize { hashes }` (or check re-confirms) → API sets/refreshes `blob:meta:<hash>` (records liveness) + adds to `blobs:index`. (Integrity is NOT trusted here — it is verified authoritatively by the worker on pull.)
5. `POST /v1/execute` with `files: [{ name, ref: "sha256:<hex>" }, ...]` (mixed with inline files).
6. Worker: for each `ref`, lease + `GetObject` **streaming** → write into the sandbox workspace (no full-file buffering in RAM) while hashing; **verify sha256 == ref**; mismatch/missing → fail the job with a clear error. Touch TTL. Release lease on terminal.

### Contract — `FileInput` gains a `ref` variant
- Either `{ name, content, encoding? }` (inline, Phase 15) OR `{ name, ref: "sha256:<64hex>" }`. Make `content` optional and add optional `ref`; a `FileInput` must have exactly one of `content`/`ref` (enforce in zod refinement + Go/worker validation + worker-side). Keep backward-compat: existing inline callers unaffected.

## Surfaces
- **Contract:** `wire.schema.json` `FileInput` (+`ref`), maybe a `BlobCheckRequest`/`BlobCheckResponse`/`BlobFinalizeRequest` message; `keys` blob builders. `pnpm contract` + `make contract-check`.
- **Go store:** extend `internal/artifactstore` (or new `internal/blobstore`) — streaming Get, Stat/exists, Remove, key prefix `blobs/cas/`.
- **Go config:** blob S3 settings (reuse artifact S3 config or `BLOB_S3_*`), idle TTL (`BLOB_IDLE_TTL`), GC interval/grace (`BLOB_GC_INTERVAL`, `BLOB_GC_GRACE`), inline-vs-CAS not needed server-side. BYO-bucket = the existing `*_S3_*` envs.
- **Go worker:** ref resolution + streaming pull + sha256 verify + lease add/remove on the existing lifecycle; GC goroutine + Lua scripts; wire into both DockerSocketRunner and (parity note) the zygote path materialization — the worker resolves refs to bytes/files BEFORE handing to the runner, so refs become regular workspace files for either runner.
- **API:** `routes/blobs.ts` (`/v1/blobs/check`, `/v1/blobs/finalize`), presign via TS S3 lib, Redis liveness reads/writes, blob S3 presign config in `config.ts`. Wire into `app.ts`. Bearer auth automatic via `/v1/*`.
- **SDK:** `client.blobs.upload(buffer, {ttlSeconds})` (hash→check→PUT-if-missing→finalize→return ref); `execute()` transparently routes each file inline-vs-CAS by a size threshold (default ~256 KiB, configurable).
- **Compose/infra:** minio already present; ensure blob prefix works with one bucket. Document BYO-bucket env. If a separate inert profile is wanted mirror `observability`, else reuse the running minio.
- **Tests + docs.**

## Security / threat model (host-escape-only, edalef exams)
- Worker pulls ONLY from our configured store (no client URLs) → no SSRF.
- sha256 verified at worker pull (authoritative integrity/tamper gate).
- Single trusted backend caller → global dedup is safe; **no per-tenant namespacing** in v1.2.
- API presign = local crypto only; bytes never transit the gateway.

## Constraints
- Atomic commits; trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Contract is the fragile seam: schema → `pnpm contract` → `make contract-check`.
- Phase needs a bucket: implement + test locally against the compose minio; document deploy steps; do NOT half-provision prod storage.
