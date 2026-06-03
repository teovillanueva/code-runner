---
status: partial
phase: 09-artifacts-pullable-run-output
source: [09-VERIFICATION.md]
started: 2026-06-03T15:30:28Z
updated: 2026-06-03T15:30:28Z
---

## Current Test

[awaiting human testing]

## Tests

### 1. Python matplotlib artifact capture (full compose stack)
expected: Submit a `collectOutput: true` Python job running `import matplotlib.pyplot as plt; plt.plot([1,2,3]); plt.savefig('plot.png')` through the API; `GET /v1/jobs/:id/output` returns `RunResult.artifacts[0].name == 'plot.png'` with a presigned MinIO URL that resolves to PNG bytes. Requires building the python-3.12 image (matplotlib baked) + `docker compose up` with MinIO/Redis/worker.
result: [pending]

### 2. R graphics artifact capture (full compose stack)
expected: Submit a `collectOutput: true` R job running `png('chart.png'); plot(1:3); dev.off()`; `GET /v1/jobs/:id/output` returns `RunResult.artifacts[0].name == 'chart.png'` with a presigned URL; no popen/seccomp stderr noise; jsonlite/data.table still import. Requires building the r-4.4 image (reconciled R_DEFAULT_PACKAGES) + compose stack.
result: [pending]

### 3. Worker lifecycle-rule install on compose boot
expected: `docker compose up` starts the minio service; worker logs show `EnsureLifecycle` completed; `mc ls`/console shows the bucket auto-created with a lifecycle expiration rule on the `artifacts/` prefix. (NOTE: the lifecycle install itself is already orchestrator-proven against live MinIO via `TestFreshBucketEndToEnd`; only the full compose env→S3Store→boot wiring remains.)
result: [pending]

### 4. React hook artifacts[] from live soketi
expected: In a browser running a soketi-connected React app, a collected plot-producing job exposes `useCodeRunnerJob().artifacts == [{ name, mimeType, bytes, url }]` at job end; the browser fetches each presigned `url` directly with no bearer token.
result: [pending]

## Summary

total: 4
passed: 0
issues: 0
pending: 4
skipped: 0
blocked: 0

## Gaps
