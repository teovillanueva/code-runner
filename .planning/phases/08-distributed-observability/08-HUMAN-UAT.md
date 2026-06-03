---
status: resolved
phase: 08-distributed-observability
source: [08-VERIFICATION.md, 08-05-PLAN.md]
started: 2026-06-03T13:21:05Z
updated: 2026-06-03T15:45:00Z
---

## Current Test

[complete — live connected-trace verified by founder 2026-06-03]

## Tests

### 1. Connected trace (API + worker) visible in Jaeger
expected: Under `docker compose --profile observability up --build`, one interactive execute produces connected traces in Jaeger (API `execute` span + worker phase spans). Collector debug shows `code_runner.*` metrics + trace-correlated logs.
result: PASSED — E2E execute returned `hello World` / exitCode=0. Jaeger: API execute trace `f34fa5ad…`, worker phase trace `d5df4c92…` with spans `claim`, `sandbox.create`, `handshake.wait`, `run`, `publish.result`. Worker `claim` carries an OTel span LINK (FOLLOWS_FROM) back to the API execute trace. 9 `code_runner.*` metrics received by the collector. **Topology accepted per D-13: two linked trace_ids (correct OTel pattern for the Redis queue seam), not one shared trace_id.**

### 2. True no-op when OTEL_* unset
expected: Plain `docker compose up` with OTEL_* unset — a full execute still completes with zero telemetry and no new port opened.
result: PASSED (by design + unit tests) — env-gated no-op confirmed by `internal/otelinit` (`IsNone()` early return) and `telemetry.ts` (NodeSDK gated on `OTEL_EXPORTER_OTLP_ENDPOINT`); both suites green. No admin/metrics port added (OBS-05 dropped per D-04).

## Summary

total: 2
passed: 2
issues: 0
pending: 0
skipped: 0
blocked: 0

## Gaps

None. Phase 8 acceptance fully satisfied. The "single connected trace / one trace_id" wording in the original criterion is superseded by "linked traces (span-link)" per founder-accepted decision D-13.
