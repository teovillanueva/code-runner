# Large & shared input files (content-addressed blobs)

Inline files ([input-files.md](./input-files.md)) are the right tool for small
text and binary inputs — they ride in the `POST /v1/execute` body and need
nothing provisioned. But for **large** files (datasets, model weights, big
fixtures) or files you reuse across **many** runs, inlining is wasteful: every
request re-ships the bytes, and a single request body has a size cap
(`MAX_FILES_BYTES`, default 8 MiB).

The **content-addressed blob store (CAS)** solves both. A file is identified by
the `sha256` of its bytes, uploaded **once**, and then referenced by hash from as
many jobs as you like. If the bytes are already in the store, the upload is
skipped entirely — that is the dedup win.

```
ref = "sha256:" + sha256(bytes)            # the content address
files: [{ name: "data/train.parquet", ref }]   # reference it in a run
```

## Architecture (who does what)

- **The API presigns upload URLs.** Presigning is pure **local crypto** — the API
  makes **no S3 network call** and **never proxies bytes**. The thin-gateway
  invariant holds: file bytes go **client → store direct**, never through the API.
- **Existence is checked in Redis**, never S3. A blob is "present" iff its
  liveness key `blob:meta:<hash>` exists.
- **The worker is the only party that reads/writes blob bytes**, and is the
  **authoritative `sha256` verifier**: on pull it streams the blob from
  code-runner's own configured store, recomputes the digest, and **fails the job
  cleanly** on any mismatch or miss. No partial run, no leaked container.

This is an **internal service**: a single trusted backend calls it, so the store
is globally deduplicated (no per-tenant namespacing in v1).

## The flow

```
 SDK                          API                       Store (S3/MinIO)   Redis
  │  sha256(bytes) = h         │                              │              │
  │  POST /v1/blobs/check ────►│  EXISTS blob:meta:h ─────────────────────► │
  │                            │  missing → presign PUT(blobs/cas/h)        │
  │ ◄── { missing:[{h,url}],   │  present → touch TTL (monotonic) ────────► │
  │       present:[...] }      │                              │              │
  │  PUT bytes ───────────────────────────────────────────► │ (direct)     │
  │  POST /v1/blobs/finalize ─►│  HSET blob:meta:h + SADD blobs:index ────► │
  │ ◄── { finalized:[h] }      │  (NO byte read — integrity is worker's job) │
  │                            │                              │              │
  │  POST /v1/execute  files:[{ name, ref:h }]                │              │
  │                            │  enqueue job (ref unchanged) ────────────► │
  │                            │   worker: lease h, GET bytes, verify        │
  │                            │   sha256==h, materialize at name, run       │
```

1. **`POST /v1/blobs/check { hashes: ["sha256:<hex>", ...] }`** → the API replies
   `{ missing: [{ hash, uploadUrl }], present: [hash, ...] }`. `missing` carries a
   presigned PUT URL (default 15-minute window). `present` blobs had their idle
   TTL refreshed.
2. **`PUT` the raw bytes** to each `uploadUrl` (client → store direct).
3. **`POST /v1/blobs/finalize { hashes }`** → the API records liveness
   (`blob:meta:<hash>` with an idle TTL + membership in `blobs:index`). It does
   **not** read the bytes — integrity is verified by the worker on pull.
4. **`POST /v1/execute`** with `files: [{ name, ref: "sha256:<hex>" }]` (freely
   mixed with inline `{ name, content }` files).
5. The **worker** leases the blob, streams it from the store, **verifies
   `sha256 == ref`**, and materializes it as the workspace file at `name`.

### The `FileInput` ref variant

A `FileInput` carries **exactly one** of `content` (inline) or `ref` (a blob):

```jsonc
{ "name": "data/train.parquet", "ref": "sha256:9f86d0…<64 hex>" }
```

| Field | Notes |
| --- | --- |
| `name` | Relative path under `/workspace` (same rules as inline files). |
| `ref`  | `"sha256:" + <64 lowercase hex>`. Mutually exclusive with `content`. |

The content/ref XOR is enforced in **both** the API (a request with both, neither,
or a malformed ref → **400**) and the worker (re-validated — never trust one side).

## SDK usage (Node)

The SDK makes this transparent. Use `client.blobs.upload` directly, or just hand
big buffers to `executeFiles` and let it route automatically.

```ts
import { CodeRunnerClient } from "@teovilla/code-runner-sdk-node";

const client = new CodeRunnerClient({
  baseUrl: process.env.EXECUTOR_URL!,
  token: process.env.EXECUTOR_API_TOKEN!,
  // Binary files larger than this are routed to CAS; smaller ones go inline.
  inlineThresholdBytes: 256 * 1024, // default
});

// Explicit upload → returns a ref you can reuse across runs.
const { ref } = await client.blobs.upload(bigBuffer);
await client.executeFiles({
  language: "python",
  files: [{ name: "data/train.parquet", ref }],
});

// Transparent routing: a large Buffer auto-uploads; a small one inlines.
await client.executeFiles({
  language: "python",
  files: [
    { name: "main.py", content: "import pandas as pd; ..." }, // inline text
    { name: "data/train.parquet", data: bigBuffer },           // → CAS (ref)
    { name: "tiny.json", data: smallBuffer },                  // → inline base64
  ],
});
```

`blobs.upload` computes the sha256, calls `/check`, PUTs the bytes only if
**missing**, finalizes, and returns `{ ref }`. An already-present blob skips the
PUT — re-running the same upload is cheap.

> Transparent CAS routing requires the blob store to be **configured
> server-side** (see below). When it is not, `/v1/blobs/*` returns **501** and you
> should keep using inline files.

## Seed a SQLite database via blob

The `sqlite` language opens **`database.db`** in the run cwd (`/workspace`). That
makes the CAS a perfect fit for an exam-style workload: upload a pre-built SQLite
`.db` **once** as a blob, then reference it as `{ name: "database.db", ref }` in
every run alongside the student's SQL. Each run executes in a **fresh, ephemeral
sandbox**, so every student gets their own copy of the seed db — no cross-run
state, no corruption from concurrent writers.

```ts
import { CodeRunnerClient } from "@teovilla/code-runner-sdk-node";
import { readFileSync } from "node:fs";

const client = new CodeRunnerClient({
  baseUrl: process.env.EXECUTOR_URL!,
  token: process.env.EXECUTOR_API_TOKEN!,
});

// Upload the exam database ONCE — deduped across every student run.
const dbBuffer = readFileSync("exam.db");
const { ref } = await client.blobs.upload(dbBuffer);

// Each student's SQL runs against a fresh copy of the seeded db.
await client.executeFiles({
  language: "sqlite",
  files: [
    { name: "database.db", ref },                  // ← the seeded blob
    { name: "main.sql", content: "SELECT * FROM exam;" },
  ],
});
```

The run argv is `sqlite3 -batch database.db -init main.sql`: sqlite opens the
provided `database.db`, runs `main.sql`, then accepts interactive SQL over stdin.

> If **no** `database.db` is provided, sqlite creates an empty file-backed db —
> functionally equivalent to the old in-memory default for query workloads. One
> minor note: with `collectOutput`, that auto-created empty `database.db` may show
> up as a run artifact when no db was supplied.

## Liveness, TTL, leases & GC

Bytes live in S3; **liveness lives in Redis**.

- **Idle TTL** (`BLOB_IDLE_TTL`, default 24h): every touch (a `/check` hit, a
  `/finalize`, or a worker pull) extends the TTL **monotonically** — it only ever
  *lengthens*, never shrinks. A blob unused for longer than the TTL becomes a GC
  candidate.
- **Leases** (`blob:lease:<hash>`): while a job references a blob it is **pinned**
  — the worker adds the job id on claim and removes it on every terminal path. GC
  **never** deletes a leased blob, even past its TTL.
- **GC** (worker, periodic, lock-guarded): a blob is collected only when its meta
  has expired **and** it is unleased **and** it has stayed that way past a **grace
  window** (`BLOB_GC_GRACE`, default 30m). This avoids deleting a blob that is
  about to be re-used. Runs every `BLOB_GC_INTERVAL` (default 10m); a single
  replica holds the GC lock at a time.

## Configuration

Blobs **reuse the artifact S3 endpoint, credentials, and region**. By default
blobs and artifacts share one bucket under distinct prefixes (`blobs/cas/` vs
`artifacts/`).

| Env | Side | Default | Purpose |
| --- | --- | --- | --- |
| `BLOB_S3_BUCKET` | worker + api | `BUCKET_NAME` | BYO-bucket: split blobs into their own bucket. |
| `BLOB_S3_PUBLIC_ENDPOINT` | api | `ARTIFACT_S3_PUBLIC_ENDPOINT` → `AWS_ENDPOINT_URL_S3` | Endpoint the API presigns **upload** URLs against (must be reachable by the SDK). |
| `BLOB_UPLOAD_URL_TTL` | api | `900` (s) | Presigned PUT-URL validity window. |
| `BLOB_IDLE_TTL` | worker + api | `86400` (s) | Idle liveness TTL on `blob:meta:<hash>`. |
| `BLOB_GC_INTERVAL` | worker | `600` (s) | How often the GC sweep runs. |
| `BLOB_GC_GRACE` | worker | `1800` (s) | Grace window before a collectable blob is deleted. |
| `AWS_*` | api + worker | MinIO dev creds | S3 credentials/region (reused from artifacts). |

If the blob store is **unconfigured** (no endpoint/bucket/creds on the API), the
`/v1/blobs/*` routes return **501** and `docker compose up` stays a no-op —
mirroring telemetry-off-by-default.

### Local dev (compose)

The dev MinIO already backs blobs. Because the SDK runs on your **host** (outside
the compose network), the API must presign against a **host-reachable** address:

1. Uncomment the MinIO `ports:` block in `docker-compose.yml` (publish `9000`).
2. Set `BLOB_S3_PUBLIC_ENDPOINT=http://127.0.0.1:9000` in `.env`.
3. `docker compose up`. The shared bucket is auto-created by the worker on boot.

## Security notes

- **No SSRF.** The worker pulls only from code-runner's own configured store at a
  known endpoint — never a client-supplied URL.
- **Tamper-proof.** The `ref` *is* the sha256; the worker recomputes it on pull
  and refuses any blob whose bytes don't match. A poisoned or truncated upload
  fails the job, it does not run.
- **No bytes through the gateway.** The API only signs URLs (local crypto) and
  reads/writes Redis liveness. Uploads and pulls are client↔store and
  worker↔store respectively.
- **Single trusted caller.** Your backend is the only client, so global dedup is
  safe.

## See also

- [input-files.md](./input-files.md) — inline text/binary files (the small-file path).
- [deploy-fly.md](./deploy-fly.md) — production deploy; provision the blob bucket + env there.
