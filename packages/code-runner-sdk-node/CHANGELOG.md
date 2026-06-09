# @teovilla/code-runner-sdk-node

## 0.4.0

### Minor Changes

- fdd3236: v1.2 Input Files & Content-Addressed Blobs

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

### Patch Changes

- Updated dependencies [fdd3236]
  - @teovilla/code-runner-contract@0.5.0

## 0.3.0

### Minor Changes

- f2d13e4: Add `CodeRunnerClient.getStatus(id)` — `GET /v1/jobs/:id/status`, returning the live
  `JobStatus`. Intended for reconciling client state after a late soketi subscription:
  pull the authoritative state instead of waiting for an event that may have already
  fired. Functionally an alias of `getJob(id)` against the explicit `/status` sub-path
  (mirroring `/output`).

### Patch Changes

- Updated dependencies [88523ba]
  - @teovilla/code-runner-contract@0.4.0

## 0.2.1

### Patch Changes

- Updated dependencies [81cac83]
  - @teovilla/code-runner-contract@0.3.0

## 0.2.0

### Minor Changes

- 8eca3ae: Phase 9 — pullable run output & artifacts.
  - **contract**: add artifact wire types and the artifact event/`RunResult` surface to the shared schema.
  - **sdk-node**: add `CodeRunnerClient.getOutput(id)` returning a typed `RunResult` (Redis-backed pull of a finished run).
  - **react**: `useCodeRunnerJob` now exposes `artifacts[]` from soketi artifact events.

### Patch Changes

- Updated dependencies [8eca3ae]
  - @teovilla/code-runner-contract@0.2.0
