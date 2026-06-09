# @teovilla/code-runner-react

## 0.4.2

### Patch Changes

- Updated dependencies [fdd3236]
  - @teovilla/code-runner-contract@0.5.0

## 0.4.1

### Patch Changes

- 4b4d807: Fix duplicate/leaked soketi WebSocket connections from `CodeRunnerProvider` under React's concurrent renderer.

  The client was created during render with a `useRef` guard. `new Pusher()` opens a WebSocket in its constructor (a side effect), and React's concurrent renderer can run a component's render multiple times before commit and **discard** the intermediate passes (StrictMode, interrupted/replayed renders, Suspense retries). Each discarded render still ran `new Pusher()` and opened a socket, but never reached an effect, so its cleanup never ran — leaking orphaned connections (observed as 6+ live sockets from a single committed mount, especially when the provider is mounted high in a streaming/SSR tree).

  The pusher client now lives in a **module-level registry keyed by connection config**, making creation idempotent across every render attempt, StrictMode pass, and Fast Refresh remount: at most one socket exists per `(appKey, host, port, …)` regardless of render churn. A refcount disconnects the client when the last provider using that config unmounts. The previous `useMemo`→ref-guard fix (0.3.1) only deduped within a single committed fiber and did not cover discarded concurrent renders.

## 0.4.0

### Minor Changes

- 0c1dc10: `useCodeRunnerJob` now models a `"queued"` status and reconciles late joins.
  - **`JobStatusState` gains `"queued"`.** The hook seeds it the moment it has a job
    (the job is already enqueued — `/execute` returned `status:"queued"`) instead of
    showing `"idle"` until the worker's first event. This fixes the UI sitting at
    "idle" while a job waited behind worker capacity. The wire `queued` stage keeps the
    status at `"queued"`; only compiling/running/output advance it to `"running"`.
  - **New `onResolveStatus` callback.** Pull the persisted status (e.g. via sdk-node's
    `getStatus`) on subscribe to reconcile a late join — a job that already moved past
    `queued` before you subscribed missed those events. The hook adopts the pulled
    state only if it is _ahead_ of the live state, so a slow pull never regresses an
    event that already arrived.

### Patch Changes

- Updated dependencies [88523ba]
  - @teovilla/code-runner-contract@0.4.0

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
