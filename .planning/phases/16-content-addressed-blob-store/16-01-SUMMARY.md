# Phase 16 Plan 01: CAS core Summary

One-liner: Content-addressed (sha256) blob store core in Go — contract `FileInput.ref` variant + blob messages, an S3 `BlobStore` (streaming get/stat/remove under `blobs/cas/`), Redis liveness with a monotonic touch-on-use Lua + per-blob leases, worker-side ref resolution (lease → stream-pull → authoritative sha256 verify → inline as a normal workspace file for BOTH runners), and a lock-guarded grace-window GC.

## Commits (in order)

| Task | Commit | Description |
| ---- | ------ | ----------- |
| 1 | `b0bae1d` | feat(contract): FileInput.ref + blob check/finalize messages + blob keys (BLOB-05) |
| 2 | `cb5727a` | feat(blobstore): S3 content-addressed store (streaming get/stat/remove) (BLOB-01) |
| 3 | `d87f93e` | feat(config): blob idle TTL + GC interval/grace + BYO bucket override |
| 4 | `f6473ac` | feat(blobindex): Redis liveness (monotonic touch-on-use) + per-blob lease (BLOB-07/08) |
| 5 | `9f5dee2` | feat(worker): resolve blob refs — streaming pull + sha256 verify + lease (BLOB-06/09) |
| 6 | `495be78` | feat(worker): blob GC — lock-guarded sweep with grace window (BLOB-08) |
| 7 | (this doc) | docs(16): CAS core execution summary |

## Changes per task

### Task 1 — Contract (`b0bae1d`)
- `wire.schema.json`: `FileInput.content` now **optional**, added `ref` (`^sha256:[a-f0-9]{64}$`); only `name` is required. The content/ref XOR is documented as enforced at runtime (API zod refine + worker) — it is NOT expressible in draft-2020-12 codegen.
- New messages: `BlobCheckRequest {hashes[]}`, `BlobCheckResponse {missing: BlobUpload[], present: string[]}`, `BlobUpload {hash, uploadUrl}`, `BlobFinalizeRequest {hashes[]}`, `BlobFinalizeResponse {finalized[]}`.
- Regenerated TS types/zod + Go structs. **Breaking codegen change:** `FileInput.Content` is now `*string` — fixed `decodeFileContent`/`zygote.go` (nil-guard) and rewrote ~15 test files' `Content: "..."` literals to `Content: wire.Ptr(...)`.
- Added non-generated `packages/contract/gen/go/wire/ptr.go` → `wire.Ptr[T any](v T) *T` (survives `pnpm contract`; `make contract-check` only diffs generated artifacts).
- Blob keys in lockstep (Go `internal/keys` + TS `keys`): `blobMeta`, `blobLease`, `blobIndex`, `blobGcCandidates`, `blobGcLock`.
- Updated `file-input.test.ts` for optional-content + `ref` accept/reject.

### Task 2 — Blob store (`cb5727a`)
- New `internal/blobstore`: `BlobStore` interface (`Get` streaming `io.ReadCloser`, `Stat` exists+size, `Remove` idempotent, `EnsureBucket`) + `S3Store` via minio-go, prefix `blobs/cas/<hash>`. Internal endpoint only (no presign client — the API presigns PUTs). Bucket = `BlobS3Bucket`, falls back to `S3Bucket`. `isNotFound` maps NoSuchKey/404 → clean absence. **No** lifecycle rule (blob expiry is the Redis-GC, not S3 lifecycle).
- Unit tests (key derivation, construction guards) + `s3_integration` round-trip (tag-gated, runtime-skip).

### Task 3 — Config (`d87f93e`)
- `config.go`: `BlobS3Bucket`, `BlobIdleTTL` (24h), `BlobGCInterval` (10m), `BlobGCGrace` (30m) with defaults; `Validate()` enforces TTL>0, interval>0, grace>=0.
- `configFromEnv`: `BLOB_S3_BUCKET` (defaults to `S3Bucket`), `BLOB_IDLE_TTL`/`BLOB_GC_INTERVAL`/`BLOB_GC_GRACE` (seconds). Blobs reuse the artifact S3 endpoint/creds/region.
- `.env.example` documents the CAS knobs + BYO-bucket (shared vs split).

### Task 4 — Redis liveness + lease (`f6473ac`)
- New `internal/blobindex`: `Touch` (SADD index + HSETNX size/createdAt + **monotonic** PEXPIRE via the **first Lua EVAL** in the codebase — a shorter touch never shrinks the TTL), `Exists`, `Lease`/`Release` (SADD/SREM jobId, idempotent; Lease also touches), `Leased` (SCARD>0).
- Unit test pins the script's monotonic/HSETNX/PEXPIRE shape; `redis_integration` test proves monotonic extension + HSETNX-once + lease add/remove against live Redis.

### Task 5 — Worker ref resolution (`9f5dee2`)
- `internal/worker/blobs.go`: `resolveBlobRefs` runs **before** `runner.Create`. Per ref: validate ref shape + workspace path; `Lease`; `pullAndVerify` streams the blob from OUR store into a disk staging file (`os.CreateTemp`) while feeding `sha256.New()` (RAM-bounded verify); verify digest==ref; inline the verified bytes as **base64** content (Ref cleared, Encoding=base64). So both `DockerSocketRunner` and the zygote relay see refs as ordinary files.
- Clean-failure gates (all → `errBlobVerify`, `publishError`, no sandbox, no partial run): store-miss, sha256 mismatch, content+ref both set, unconfigured store, bad path/ref.
- Lease released on **every** terminal path (resolve-failure, create-failure, once-only teardown — idempotent SREM).
- `blobs.resolve` span emitted **only** when the job has refs (keeps the exact OBS-03 phase-span set for inline-only jobs — see Deviations).
- `worker.Config` gains `BlobStore`/`BlobIndex`/`BlobIdleTTL`; wired in `apps/worker/main.go`.
- Unit tests: match inlines+leases (and does NOT mutate caller spec), mismatch/store-miss fail+release, no-refs back-compat, content+ref rejected, ref-shape guard.

### Task 6 — GC (`495be78`)
- `internal/blobindex/gc.go`: `GC.Sweep` acquires `blobs:gc:lock` (SET NX PX, skip if held), enumerates `blobs:index`. Collectable iff meta expired AND unleased. Grace via `blobs:gc:candidates` ZSET (first-seen score); delete only when `now-score > grace`. Recovered/leased blobs dropped from candidates and survive; **leased expired blob is never deleted**. On reclaim: Stat (byte report) → Remove → scrub index/candidates/lease/meta. Always logs reclaimed count+bytes.
- `GC.Run` ticks every `BlobGCInterval`; started in `apps/worker/main.go` when a blob store is configured (lock TTL = interval+1m).
- Unit + `redis_integration` tests: within-grace kept, past-grace deleted+byte report, first-sight records candidacy, recovered survives, leased never deleted, lock-held skips.

## Test results

- `go build ./...` — **green**.
- `go vet ./...` (and with all build tags) — **green**.
- `go test ./...` — green EXCEPT two pre-existing pub/sub failures in `internal/stdintransport` (`TestRedisTransport_RoundTrip`, `TestRedisTransport_CloseStopsDelivery`). That package is **untouched** by these commits (verified via `git diff --name-only 5ff8a76 HEAD`); the failures are an environment/pubsub-timing issue with the local `redis:7` container, not a CAS regression. Logged to `deferred-items.md`. All other packages incl. the new `blobstore`/`blobindex`/`worker` are green.
- `make contract-check` — **clean** (no drift, exit 0).
- Contract TS tests (`pnpm --filter @teovilla/code-runner-contract test`) — **18/18 pass**.
- `redis_integration` tests (blobindex liveness + GC) — **pass** against a throwaway `redis:7` on `:6380`. Started for the run, **stopped after** (test convention).
- `s3_integration` (blobstore) — compiles + skips cleanly (no MinIO started this run); mirrors the artifact-store integration pattern, runs against the compose MinIO.

### Skips (expected, as existing)
- Docker-tagged runner/worker tests (zygote integration/abuse, docker integration) — skipped on macOS as existing; the blob logic is covered by non-Docker unit tests + redis-backed integration tests.
- `s3_integration` / `redis_integration` — tag-gated + runtime-skip without infra.

## Deviations from Plan

### Auto-fixed / design choices (no user permission needed)

1. **[Rule 3 — Blocking] `FileInput.Content` became `*string`.** Making `content` optional in the schema made the generated Go field a pointer. Fixed `decodeFileContent` + `zygote.go` (nil-guard: a nil Content reaching the runner = unresolved-ref error, an empty string = legit zero-byte file) and rewrote all `Content: "..."` test literals via a new `wire.Ptr[T]` helper. Commit `b0bae1d`.

2. **[Rule 1 — Test] `blobs.resolve` span made conditional.** Two strict OBS-03 tests (`TestOutputBytesCounter_NonzeroAndNoPerChunkSpans`, `TestPhaseSpans_NoPerChunkSpans`) assert an exact phase-span set. Rather than relax the tests, I emit the `blobs.resolve` span **only when a job actually references a blob** — inline-only jobs (the common case + these tests) produce no extra span. Cleaner and preserves the OBS-03 invariant. Commit `9f5dee2`.

3. **RAM-bound nuance (noted, consistent with 16-CONTEXT).** The security-critical **verify** step is fully RAM-bounded: the blob is streamed through `io.MultiWriter(stagingFile, sha256Hasher)` to disk, never buffered whole in memory during verification. The verified bytes ARE then read back from the staging file to hand to the existing `[]byte`-based runner file-materialization path (same as every inline file today). End-to-end streaming directly into the container tar is a future optimization (the existing tar pipeline is in-memory for inline files too); reworking it is out of scope for plan 01 (would be a Rule-4 architectural change). The disk staging file + `/workspace` are disk-backed.

No architectural (Rule 4) changes were required; no checkpoints hit.

## Authentication gates

None.

## Known Stubs

None. All blob logic is wired end-to-end (resolve → verify → materialize → lease → GC). The API/SDK surface (presign, /v1/blobs/*) is plan 02, not stubbed here.

---

## What plan 02 (API/SDK) needs to know

### Contract (already shipped)
- `FileInput`: `{ name, content?, encoding?, ref? }`. **Exactly one of `content`/`ref`** — enforce in the API with a zod `.refine` on the request (the generated `FileInputSchema` does NOT enforce the XOR; the worker enforces it too). `ref` pattern: `^sha256:[a-f0-9]{64}$`.
- Messages to implement endpoints against: `BlobCheckRequest`/`BlobCheckResponse` (with `BlobUpload {hash, uploadUrl}`), `BlobFinalizeRequest`/`BlobFinalizeResponse`. All hashes are the full `sha256:<64hex>` ref string.

### Exact Redis key names (use `keys.*` from the contract — Go `internal/keys` mirrors them)
- `blob:meta:<hash>` — `keys.blobMeta(h)`. Hash `{size, createdAtMs}` + idle TTL. **EXISTS** ⇒ live.
- `blob:lease:<hash>` — `keys.blobLease(h)`. SET of jobIds; non-empty ⇒ pinned.
- `blobs:index` — `keys.blobIndex`. SET of known hashes.
- `blobs:gc:candidates` — `keys.blobGcCandidates`. ZSET (GC-internal).
- `blobs:gc:lock` — `keys.blobGcLock`. GC singleton lock (GC-internal).
- The `<hash>` in every key is the **full `sha256:<64hex>` ref**, NOT bare hex.

### Monotonic-TTL Lua (the API's `/blobs/check` "present" path and `/finalize` should use the SAME monotonic-extend semantics)
The Go worker's touch (port to the API for "touch on present"/"finalize"):
```
KEYS[1]=blob:meta:<hash>  KEYS[2]=blobs:index
ARGV[1]=hash ARGV[2]=size ARGV[3]=createdAtMs ARGV[4]=requestedTTLms
redis.call('SADD', KEYS[2], ARGV[1])
redis.call('HSETNX', KEYS[1], 'size', ARGV[2])
redis.call('HSETNX', KEYS[1], 'createdAtMs', ARGV[3])
local cur = redis.call('PTTL', KEYS[1])
if cur < 0 or tonumber(ARGV[4]) > cur then
  redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[4])); return tonumber(ARGV[4])
end
return cur
```
(`internal/blobindex.touchScript`.) Monotonic = only ever extends; HSETNX = metadata written once.

### Existence check (`/blobs/check`)
- "present" iff `EXISTS blob:meta:<hash>` (the worker's `Index.Exists`). For present hashes, the API should **touch** (monotonic extend) the TTL. For missing hashes, presign a PUT to `blobs/cas/<hash>` against the **public** S3 endpoint.

### `/finalize`
- After the SDK PUTs the bytes, set/refresh `blob:meta:<hash>` (record liveness) + `SADD blobs:index <hash>`. **Do NOT verify integrity here** — the worker is the authoritative sha256 verifier on pull.

### S3 object key + bucket
- Object key: `blobs/cas/<hash>` (`<hash>` = full `sha256:<64hex>`). Presign against this exact key.
- Bucket: `BLOB_S3_BUCKET` (defaults to the artifact `BUCKET_NAME`). Presign against the **public** endpoint (`ARTIFACT_S3_PUBLIC_ENDPOINT` if split-horizon).
- TS presigner: pick `minio` JS client (matches the Go minio-go) OR `@aws-sdk/s3-request-presigner` — pin + justify in plan 02 (CONTEXT leans toward minio for symmetry).

### Worker behavior the API/SDK can rely on
- A job with `files: [{name, ref: "sha256:..."}]` is leased + verified + materialized by the worker before the run. A store-miss or sha256 mismatch fails the job cleanly with a result error (no partial run). Leases are released on every terminal path; GC respects leases + grace.
- Env knobs: `BLOB_IDLE_TTL` (s, default 86400), `BLOB_GC_INTERVAL` (s, 600), `BLOB_GC_GRACE` (s, 1800), `BLOB_S3_BUCKET` (defaults to artifact bucket). See `.env.example`.

## Self-Check: PASSED

All created files exist on disk; all six task commits are present in git history.
