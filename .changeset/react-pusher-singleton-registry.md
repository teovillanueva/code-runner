---
"@teovilla/code-runner-react": patch
---

Fix duplicate/leaked soketi WebSocket connections from `CodeRunnerProvider` under React's concurrent renderer.

The client was created during render with a `useRef` guard. `new Pusher()` opens a WebSocket in its constructor (a side effect), and React's concurrent renderer can run a component's render multiple times before commit and **discard** the intermediate passes (StrictMode, interrupted/replayed renders, Suspense retries). Each discarded render still ran `new Pusher()` and opened a socket, but never reached an effect, so its cleanup never ran — leaking orphaned connections (observed as 6+ live sockets from a single committed mount, especially when the provider is mounted high in a streaming/SSR tree).

The pusher client now lives in a **module-level registry keyed by connection config**, making creation idempotent across every render attempt, StrictMode pass, and Fast Refresh remount: at most one socket exists per `(appKey, host, port, …)` regardless of render churn. A refcount disconnects the client when the last provider using that config unmounts. The previous `useMemo`→ref-guard fix (0.3.1) only deduped within a single committed fiber and did not cover discarded concurrent renders.
