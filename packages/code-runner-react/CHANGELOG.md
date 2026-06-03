# @teovilla/code-runner-react

## 0.2.1

### Patch Changes

- fd5c351: Fix the React SDK pusher-js crash when no auth headers are supplied (`auth: undefined` tripped pusher-js's `'auth' in opts` check). Also avoid subscribing to a `private-run-` channel before a `jobId` exists, and add an `onSubscribed` callback to `useCodeRunnerJob` to fire the start-handshake once the soketi subscription is confirmed.

## 0.2.0

### Minor Changes

- 8eca3ae: Phase 9 — pullable run output & artifacts.
  - **contract**: add artifact wire types and the artifact event/`RunResult` surface to the shared schema.
  - **sdk-node**: add `CodeRunnerClient.getOutput(id)` returning a typed `RunResult` (Redis-backed pull of a finished run).
  - **react**: `useCodeRunnerJob` now exposes `artifacts[]` from soketi artifact events.

### Patch Changes

- Updated dependencies [8eca3ae]
  - @teovilla/code-runner-contract@0.2.0
