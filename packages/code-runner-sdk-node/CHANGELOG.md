# @teovilla/code-runner-sdk-node

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
