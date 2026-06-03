---
phase: 08-distributed-observability
plan: 04
subsystem: observability
tags: [opentelemetry, otel-go, metrics, observable-gauge, histogram, counter, manual-reader, low-cardinality]

# Dependency graph
requires:
  - phase: 08-distributed-observability
    plan: 02
    provides: Env-gated worker OTel SDK + meter, lazy global-provider instrument resolution, sandbox.create SPAN + ctx threading in docker.go
  - phase: 08-distributed-observability
    plan: 03
    provides: API-side traceparent inject (parallel dependency; no Go file overlap)
provides:
  - Worker observable gauges (OBS-06) code_runner.queue.depth (LLEN, skip-on-error) + code_runner.slots.used/.max (in-memory semaphore)
  - Sandbox latency HISTOGRAMS code_runner.sandbox.create.duration / .kill.duration (unit s, language attr) — disjoint from the 08-02 create SPAN
  - Output-bytes counter code_runner.output.bytes (forwarded bytes in the pump budget path; never per-chunk spans)
  - Soketi publish metrics code_runner.publish.duration histogram + code_runner.publish.errors counter at the Trigger chokepoint
  - Reaper orphans counter code_runner.reaper.orphans
affects: [distributed-observability, worker-operability]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Lazy per-call instrument resolution from the CURRENT global MeterProvider (mirrors 08-02 worker.go) so otelinit.Init at boot OR a ManualReader in tests routes measurements correctly; no instrument cached at package init"
    - "Observable gauges registered via Worker.RegisterMetrics (meter + semaphore + store all in scope); queue-depth callback reads LLEN under a 250ms short-timeout ctx and SKIPS the observation on error (Pitfall 5 — never block the export cycle, never force a stale zero)"
    - "slots.used/.max read the in-memory semaphore (MaxSandboxes − len(slots)); no Redis dependency, so a Redis outage cannot suppress the slots gauges"
    - "instrumentedTriggerer decorator wraps any triggerer (production pusherTriggerer OR test fake) so the soketi publish chokepoint is the single timing/error site"
    - "Sandbox create/kill latency recorded via deferred .Record so every return path is timed; the histogram is disjoint from the 08-02 sandbox.create SPAN (span context-threaded by 08-02, histogram .Record added here)"
    - "Metric attributes low-cardinality only: sandbox histograms carry language (bounded manifest set); queue/slots/output/reaper/publish carry NO attributes; job_id is NEVER a metric dimension"

key-files:
  created:
    - internal/runner/metrics_test.go
    - internal/publisher/metrics_test.go
    - internal/reaper/metrics_test.go
    - internal/session/pump_metrics_test.go
    - internal/worker/gauge_metrics_test.go
    - internal/worker/output_bytes_metrics_test.go
  modified:
    - internal/runner/docker.go
    - internal/publisher/publisher.go
    - internal/jobstore/queue.go
    - internal/reaper/reaper.go
    - internal/session/pump.go
    - internal/worker/worker.go
    - apps/worker/main.go

key-decisions:
  - "D-05: instrument-type mapping — Int64ObservableGauge for queue depth + slots used/max; Float64Histogram for sandbox create/kill + publish latency; Int64Counter for reaper orphans, publish errors, output bytes"
  - "D-06: code_runner.* dotted namespace + OTel semconv units (duration histograms unit s; bytes unit By; gauges {job}/{slot}; counters {container}/{error})"
  - "queue.depth gauge skips on Redis error (no stale/forced zero); slots gauges never touch Redis (T-08-11 mitigation)"
  - "output.bytes counts only FORWARDED (within-budget) bytes, excluding truncated overflow; never a per-chunk span (RESEARCH anti-pattern)"

patterns-established:
  - "Instrument-helper unit tests: where the production .Record/.Add site is Docker/Redis-gated (Create/Kill/reapContainer), an internal-package ManualReader test drives the SAME unexported instrument helper to assert name/unit/value/attributes deterministically without infra; the gated integration suite still exercises the real call site"

requirements-completed: [OBS-06, OBS-07]

# Metrics
duration: ~35min
completed: 2026-06-03
---

# Phase 8 Plan 04: Worker OBS-06 Domain Metrics Breadth Slice Summary

**The Go worker now emits the full OBS-06 domain-metric set over OTLP push — observable gauges for queue depth and sandbox slots used/max, latency histograms for sandbox create/kill and soketi publish, an output-bytes counter in the pump budget path, and counters for reaper orphans and publish errors — all with low-cardinality attributes only (job_id never on metrics), wired at the PATTERNS-mapped sites and reusing the 08-02 meter (no new dependencies).**

## Performance
- **Duration:** ~35 min
- **Completed:** 2026-06-03
- **Tasks:** 1
- **Files:** 13 (6 created, 7 modified)

## Accomplishments
- **Observable gauges (OBS-06).** Added `Worker.RegisterMetrics()` registering three `Int64ObservableGauge`s against the current global MeterProvider: `code_runner.queue.depth` (callback reads `store.QueueDepth` — a new `LLEN jobs:queue` wrapper — under a 250 ms short-timeout ctx and SKIPS the observation on error, never forcing a stale zero; Pitfall 5 / T-08-11), and `code_runner.slots.used` + `code_runner.slots.max` (read from the in-memory semaphore `MaxSandboxes − len(slots)`; no Redis call). Registered from `apps/worker/main.go` after `worker.New`, with a deferred deregister. When OTEL is unconfigured the callback is never invoked (no-op provider).
- **Sandbox latency histograms.** Added `code_runner.sandbox.create.duration` and `code_runner.sandbox.kill.duration` (`Float64Histogram`, unit `s`, low-cardinality `language` attribute) recorded via deferred `.Record` inside `docker.go`'s `Create`/`Kill` so every return path is timed. These are HISTOGRAMS, disjoint from the 08-02 `sandbox.create` SPAN — the span lines and ctx-threading from 08-02 were left untouched (clean file ownership, as the plan mandated).
- **Output-bytes counter.** Added `code_runner.output.bytes` (`Int64Counter`, unit `By`) incremented by the forwarded-byte count in `session/pump.go`'s budget/forward path — counting only bytes actually forwarded to the sink (within budget), excluding truncated overflow, and EXPLICITLY never a per-chunk span (RESEARCH anti-pattern).
- **Soketi publish metrics.** Wrapped the triggerer in an `instrumentedTriggerer` decorator (applied in `newWithTriggerer`, so both the production `pusherTriggerer` and any test fake are measured) that records `code_runner.publish.duration` (`Float64Histogram`, unit `s`) per Trigger and increments `code_runner.publish.errors` (`Int64Counter`) on a non-nil Trigger error.
- **Reaper orphans counter.** Added `code_runner.reaper.orphans` (`Int64Counter`) incremented once per successfully removed orphan container in `reapContainer`, after the `ContainerRemove`.
- **Low-cardinality + no new surface.** Every metric carries only low-cardinality attributes (`language` on sandbox histograms; none on gauges/output/reaper/publish); `job_id` is never a metric attribute (grep gate PASS). No HTTP/admin listener was added — OBS-05 stays dropped (grep gate PASS). No `go.mod`/`go.sum` change — the entire OBS-06 set reuses the OTel metric API already pulled in by 08-02.
- **Deterministic ManualReader tests.** Added `sdkmetric.NewManualReader()` tests asserting instrument names/values/units/attributes: publisher (duration recorded + errors increment on a forced Trigger error), runner (create/kill histograms with `language`, no `job_id`), session pump (forwarded-bytes count; truncated bytes excluded), reaper (orphans counter, no attributes), and worker gauges (slots from semaphore; queue-depth observes seeded LLEN against local Redis and SKIPS on a dead-Redis error path; output.bytes nonzero with ZERO per-chunk spans).

## Task Commits
1. **Task 1: Worker OBS-06 domain metrics — gauges, latency histograms, output-bytes counter, reaper + publish metrics** — `51807df` (feat)

## Files Created/Modified
- `internal/runner/docker.go` (mod) — `sandboxCreateDuration()`/`sandboxKillDuration()` histogram helpers + `langAttr()`; deferred `.Record` in `Create` and `Kill`. Did NOT touch the 08-02 span/ctx-threading lines.
- `internal/publisher/publisher.go` (mod) — `publishDuration()`/`publishErrors()` helpers + `instrumentedTriggerer` decorator wired in `newWithTriggerer`.
- `internal/jobstore/queue.go` (mod) — new `QueueDepth(ctx) (int64, error)` `LLEN jobs:queue` wrapper feeding the queue-depth gauge.
- `internal/reaper/reaper.go` (mod) — `reaperOrphans()` helper + `.Add(1)` after a successful `ContainerRemove` in `reapContainer`.
- `internal/session/pump.go` (mod) — `outputBytesCounter()` helper + increment by forwarded bytes in `Run()`'s budget path.
- `internal/worker/worker.go` (mod) — `Worker.RegisterMetrics()` registering queue-depth + slots-used/max observable gauges with a short-timeout, skip-on-error callback.
- `apps/worker/main.go` (mod) — call `w.RegisterMetrics()` after `worker.New` with a deferred deregister.
- `internal/{runner,publisher,reaper}/metrics_test.go`, `internal/session/pump_metrics_test.go`, `internal/worker/{gauge,output_bytes}_metrics_test.go` (new) — ManualReader assertions.

## Decisions Made
- **Instrument-helper unit tests for infra-gated record sites.** `Create`/`Kill`/`reapContainer` talk to Docker (and the real reaper path needs Redis), so their `.Record`/`.Add` call sites live under `//go:build docker` / `//go:build reaper_integration`. To assert the OBS-06 instruments deterministically in the DEFAULT suite, the new internal-package tests drive the SAME unexported instrument helper the production code uses (`sandboxCreateDuration().Record(...)`, `reaperOrphans().Add(...)`), proving the instrument name/unit/value/attribute contract; the gated integration suites continue to exercise the real call sites. The publisher, pump, and worker-gauge tests DO drive the production code path end-to-end (no infra needed for the fake triggerer, the pump, or the semaphore).
- **queue-depth skip vs. slots always-on.** The queue-depth gauge intentionally reads Redis (the authoritative queue length) and tolerates a Redis outage by skipping; the slots gauges intentionally read only the in-memory semaphore so capacity visibility survives a Redis outage — a deliberate split matching the T-08-11 mitigation.
- **instrumentedTriggerer over instrumenting only pusherTriggerer.** Wrapping at `newWithTriggerer` (rather than only inside the concrete `pusherTriggerer.Trigger`) lets the publisher's existing fake-triggerer test path drive the forced-error case, satisfying the acceptance criterion without a live soketi.

## Deviations from Plan
None — plan executed exactly as written. The create/kill histograms were added strictly as `.Record` calls (no span/ctx-threading edits) per the 08-02/08-04 file-ownership split; no architectural changes; no scope creep; no new dependencies.

## Threat Surface
No new inbound surface. The worker still talks only to Redis + soketi + (outbound-only) OTLP collector — no HTTP listener added (grep gate PASS, OBS-05 stays dropped, T-08-13 accept). High-cardinality DoS (T-08-10) is mitigated: every metric uses low-cardinality attributes and `job_id` is never a metric dimension (grep gate PASS). Async-gauge Redis DoS (T-08-11) is mitigated: the queue-depth callback uses a 250 ms short-timeout ctx and skips on error; slots read the in-memory semaphore. No threat flags beyond the plan's register.

## Known Stubs
None. Every instrument is wired to a real source signal (LLEN, in-memory semaphore, pump forwarded bytes, ResultEvent-driven publish path, reaper removal) with no placeholder/empty data.

## Deferred Issues
None introduced by this plan. The pre-existing `internal/stdintransport` pub/sub round-trip timeout flake (byte-identical to pre-phase-8, tracked in `deferred-items.md`) was NOT touched and is out of scope.

## Verification Evidence
- `go build ./...` clean; no `go.mod`/`go.sum` drift (reused existing OTel metric API).
- `go test ./internal/runner/ ./internal/session/ ./internal/jobstore/ ./internal/reaper/ ./internal/publisher/ ./internal/worker/` all GREEN (worker gauge test exercised the real LLEN path against local Redis at 127.0.0.1:6379).
- `go vet` clean on all changed packages + `apps/worker`.
- AC grep — `grep -rn 'job_id\|jobID\|jobId' internal/ | grep -i 'WithAttributes\|attribute.String'` → NONE (no job_id on metric attributes).
- AC grep — `grep -rn "http.ListenAndServe\|http.Server" apps/worker internal/ | grep -v '_test.go'` → NONE (no new listener; OBS-05 dropped).
- ManualReader tests: publish.duration recorded + publish.errors increments on forced error; sandbox.create/.kill duration histograms with `language` attr; output.bytes == forwarded payload length AND zero per-chunk spans; queue.depth observes seeded LLEN==3 and skips on dead-Redis; slots.used==0 / slots.max==MaxSandboxes from the semaphore; reaper.orphans counter with no attributes.

## Self-Check: PASSED
All six created test files exist on disk and the task commit (`51807df`) is present in the branch history. STATE.md / ROADMAP.md were intentionally NOT modified (orchestrator owns those writes after the wave).
