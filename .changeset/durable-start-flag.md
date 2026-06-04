---
"@teovilla/code-runner-contract": minor
---

Add `keys.startFlag(jobId)` (`start:<jobId>`) to the shared Redis key conventions.

This backs the **durable start-handshake**: the API now persists this flag on
`POST /v1/jobs/:id/start` so a `start` sent while a job is still in `jobs:queue` (no
worker subscribed to its `ctrl:<id>` channel yet) is not lost. The worker reads the
flag when it claims the job, so the start is honoured even under queue backpressure.
Without this, every job that queued behind worker capacity would die at the warm-up
timeout — which made autoscaling and concurrent bursts unreliable.
