---
"@teovilla/code-runner-contract": minor
"@teovilla/code-runner-sdk-node": minor
---

v1.2 Input Files & Content-Addressed Blobs

**Contract** — `FileInput` gains an optional `encoding` (`utf8` default | `base64`) so binary
files (xlsx/parquet/images/zip) can be sent inline, and `name` now accepts `/` subdirectories
(e.g. `data/input.csv`). Adds a `ref` variant to `FileInput` (`sha256:<hex>`, mutually exclusive
with `content`) for content-addressed blobs, plus `BlobCheck`/`BlobFinalize` request/response
messages and `blob:*` Redis key builders. All additions are backward-compatible — existing
text-`content`, flat-filename callers are unaffected.

**Node SDK** — accepts binary `Buffer` input files (auto-sets `encoding: "base64"`); adds
`client.blobs.upload(buffer, { ttlSeconds })` (sha256 → existence check → presigned PUT of only
the missing bytes → finalize → `ref`) and a low-level `client.blobs.check`. `execute()` now
transparently routes each file inline-vs-CAS by a configurable `inlineThresholdBytes`
(default 256 KiB); raw `{ name, ref }` and raw `FileInput` continue to pass through unchanged.
