# @teovilla/code-runner-react

## 0.3.1

### Patch Changes

- c3b2c8b: Fix duplicate/leaked soketi WebSocket connections from `CodeRunnerProvider`. The pusher-js client is now created exactly once via a ref-guard instead of inside `useMemo` (which React StrictMode double-invokes, spawning a second socket and orphaning the first), and the effect cleanup disconnects the captured instance rather than reading a ref that may already point at a newer client.

## 0.3.0

### Minor Changes

- 81cac83: Separate compile-stage output from run output (Piston-style), with a real-time build log.
  - **contract**: add a `CompileResult` type and an optional `RunResult.compile` field (stdout/stderr/output/exitCode/durationMs), so a compiled-language run keeps its build logs distinct from the program's stdout/stderr. Add a `compile_output` soketi event (`events.compileOutput`) carrying the live, interleaved build log emitted during the `compiling` stage.
  - **react**: `useCodeRunnerJob` now returns `compileOutput` — the live build log reassembled from `compile_output` events, separate from `stdout`/`stderr` — so consumers can render a dedicated real-time build panel.

### Patch Changes

- Updated dependencies [81cac83]
  - @teovilla/code-runner-contract@0.3.0

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
