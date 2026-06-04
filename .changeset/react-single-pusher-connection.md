---
"@teovilla/code-runner-react": patch
---

Fix duplicate/leaked soketi WebSocket connections from `CodeRunnerProvider`. The pusher-js client is now created exactly once via a ref-guard instead of inside `useMemo` (which React StrictMode double-invokes, spawning a second socket and orphaning the first), and the effect cleanup disconnects the captured instance rather than reading a ref that may already point at a newer client.
