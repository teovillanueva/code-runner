---
"@teovilla/code-runner-sdk-node": minor
---

Add `CodeRunnerClient.getStatus(id)` — `GET /v1/jobs/:id/status`, returning the live
`JobStatus`. Intended for reconciling client state after a late soketi subscription:
pull the authoritative state instead of waiting for an event that may have already
fired. Functionally an alias of `getJob(id)` against the explicit `/status` sub-path
(mirroring `/output`).
