# @teovilla/code-runner-contract

## 0.5.0

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

## 0.4.0

### Minor Changes

- 88523ba: Add `keys.startFlag(jobId)` (`start:<jobId>`) to the shared Redis key conventions.

  This backs the **durable start-handshake**: the API now persists this flag on
  `POST /v1/jobs/:id/start` so a `start` sent while a job is still in `jobs:queue` (no
  worker subscribed to its `ctrl:<id>` channel yet) is not lost. The worker reads the
  flag when it claims the job, so the start is honoured even under queue backpressure.
  Without this, every job that queued behind worker capacity would die at the warm-up
  timeout — which made autoscaling and concurrent bursts unreliable.

## 0.3.0

### Minor Changes

- 81cac83: Separate compile-stage output from run output (Piston-style), with a real-time build log.
  - **contract**: add a `CompileResult` type and an optional `RunResult.compile` field (stdout/stderr/output/exitCode/durationMs), so a compiled-language run keeps its build logs distinct from the program's stdout/stderr. Add a `compile_output` soketi event (`events.compileOutput`) carrying the live, interleaved build log emitted during the `compiling` stage.
  - **react**: `useCodeRunnerJob` now returns `compileOutput` — the live build log reassembled from `compile_output` events, separate from `stdout`/`stderr` — so consumers can render a dedicated real-time build panel.

## 0.2.0

### Minor Changes

- 8eca3ae: Phase 9 — pullable run output & artifacts.
  - **contract**: add artifact wire types and the artifact event/`RunResult` surface to the shared schema.
  - **sdk-node**: add `CodeRunnerClient.getOutput(id)` returning a typed `RunResult` (Redis-backed pull of a finished run).
  - **react**: `useCodeRunnerJob` now exposes `artifacts[]` from soketi artifact events.
