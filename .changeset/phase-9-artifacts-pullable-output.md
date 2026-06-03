---
"@teovilla/code-runner-contract": minor
"@teovilla/code-runner-sdk-node": minor
"@teovilla/code-runner-react": minor
---

Phase 9 — pullable run output & artifacts.

- **contract**: add artifact wire types and the artifact event/`RunResult` surface to the shared schema.
- **sdk-node**: add `CodeRunnerClient.getOutput(id)` returning a typed `RunResult` (Redis-backed pull of a finished run).
- **react**: `useCodeRunnerJob` now exposes `artifacts[]` from soketi artifact events.
