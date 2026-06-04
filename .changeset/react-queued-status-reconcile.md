---
"@teovilla/code-runner-react": minor
---

`useCodeRunnerJob` now models a `"queued"` status and reconciles late joins.

- **`JobStatusState` gains `"queued"`.** The hook seeds it the moment it has a job
  (the job is already enqueued — `/execute` returned `status:"queued"`) instead of
  showing `"idle"` until the worker's first event. This fixes the UI sitting at
  "idle" while a job waited behind worker capacity. The wire `queued` stage keeps the
  status at `"queued"`; only compiling/running/output advance it to `"running"`.
- **New `onResolveStatus` callback.** Pull the persisted status (e.g. via sdk-node's
  `getStatus`) on subscribe to reconcile a late join — a job that already moved past
  `queued` before you subscribed missed those events. The hook adopts the pulled
  state only if it is *ahead* of the live state, so a slow pull never regresses an
  event that already arrived.
