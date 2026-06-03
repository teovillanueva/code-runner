---
phase: 08-distributed-observability
plan: 02
subsystem: observability
tags: [opentelemetry, otel-go, traces, metrics, w3c-trace-context, span-link, slog, autoexport, tdd]

# Dependency graph
requires:
  - phase: 08-distributed-observability
    plan: 01
    provides: Optional W3C traceparent/tracestate on JobSpec + Go extract round-trip contract
provides:
  - Env-gated worker OTel SDK that is a TRUE no-op when OTEL_* unset (OBS-01)
  - Worker extracts the API traceparent on claim and starts a LINKED root span sharing the API trace_id (OBS-02, D-13)
  - Named worker phase spans (claim/sandbox.create/handshake.wait/compile/run/publish.result) with NO per-chunk spans (OBS-03)
  - code_runner.jobs.terminal counter (low-cardinality terminal_state) + code_runner.queue.time histogram (OBS-06, D-07)
  - Stdout JSON logs carrying trace_id/span_id/job_id within a span context + OTLP log fan-out when configured (OBS-07, D-03)
affects: [08-api-sdk-trace-inject, 08-04-sandbox-latency-histograms, distributed-observability]

# Tech tracking
tech-stack:
  added:
    - "go.opentelemetry.io/otel/sdk v1.44.0 (TracerProvider/resource)"
    - "go.opentelemetry.io/otel/sdk/metric v1.44.0 (MeterProvider/ManualReader)"
    - "go.opentelemetry.io/otel/log + sdk/log v0.20.0 (LoggerProvider)"
    - "go.opentelemetry.io/contrib/exporters/autoexport v0.69.0 (env-driven exporter/reader factory)"
    - "go.opentelemetry.io/contrib/bridges/otelslog v0.19.0 (slog -> OTLP logs bridge)"
    - "otlpmetrichttp v1.44.0 + otlploghttp v0.20.0 (pulled by autoexport)"
  patterns:
    - "No-op gate: Init early-returns a non-nil no-op shutdown when OTEL_EXPORTER_OTLP_ENDPOINT and OTEL_TRACES_EXPORTER are both unset (neither the SDK nor autoexport is a no-op by default)"
    - "Worker LINKS (trace.WithLinks) rather than parents the API span because /v1/execute returns 202 before the run starts (D-13); link shares the API trace_id, the worker root starts a fresh trace"
    - "Telemetry instruments + tracer resolved from the CURRENT global providers per job (not cached at package init) so a provider installed after init — boot or tracetest recorder — is honoured"
    - "Untrusted traceparent fails closed to a fresh trace (invalid SpanContext, no link, no panic)"
    - "Domain metric attributes stay low-cardinality (terminal_state only); job_id is on spans/logs, never metrics"
    - "sandbox.create is a REAL span: the run loop starts it and threads ctx into docker.go ContainerCreate/Attach/Start; the create-latency histogram is a disjoint 08-04 change in the same function"

key-files:
  created:
    - internal/otelinit/otelinit.go
    - internal/otelinit/otelinit_test.go
    - internal/logging/handler.go
    - internal/logging/handler_test.go
    - internal/worker/trace_phase_test.go
    - internal/worker/trace_metrics_test.go
  modified:
    - apps/worker/main.go
    - internal/worker/worker.go
    - internal/runner/docker.go
    - internal/worker/trace_test.go
    - go.mod
    - go.sum

key-decisions:
  - "D-03: stdout JSON always (custom slog handler) + OTLP logs when configured (otelslog fan-out); job_id via a logging ctx key"
  - "D-06/D-07: code_runner.* namespace, semconv unit s on queue.time; one code_runner.jobs.terminal counter keyed by low-cardinality terminal_state"
  - "D-13: worker phase spans LINK to the API execute span, not parent-child"
  - "OBS-05 stays dropped: worker remains HTTP-server-free (asserted by the no-listener grep gate)"

patterns-established:
  - "Pattern A: instruments/tracer resolved lazily from the global provider per use, so post-init provider installation (boot or test) routes measurements correctly"
  - "Pattern B: idle/terminal outcomes asserted through the REAL session clocks (the session overwrites result flags by termination reason), not by faking the sandbox Wait result"

requirements-completed: [OBS-01, OBS-03, OBS-04, OBS-07]

# Metrics
duration: ~40min
completed: 2026-06-03
---

# Phase 8 Plan 02: Worker OTel — One Connected Trace (Go side) Summary

**The Go worker now initializes OTel env-gated as a true no-op when OTEL_* is unset, extracts the API-injected W3C traceparent on claim, and emits a LINKED root span + named phase spans sharing the API trace_id, plus a low-cardinality terminal-state counter, a time-in-queue histogram, and trace-correlated stdout JSON — turning the 08-01 Go round-trip test GREEN against production code.**

## Performance
- **Duration:** ~40 min
- **Completed:** 2026-06-03
- **Tasks:** 2
- **Files:** 12 (6 created, 6 modified)

## Accomplishments
- `internal/otelinit.Init` is a true no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_TRACES_EXPORTER` are both unset (OBS-01): it installs no exporter, sets no global provider, opens no port, and returns a non-nil no-op shutdown. When configured it builds trace/metric/log providers via the env-driven `autoexport` factory and ALWAYS sets the W3C `TraceContext` propagator (RESEARCH Pitfall 3 — Go's default propagator is a no-op).
- `internal/logging.NewCtxHandler` wraps `slog.JSONHandler` and injects `trace_id`/`span_id` (only within a valid span context) and `job_id` (from a ctx key) into every JSON line — stdout-always, valid JSON in all OTEL states (D-03). A `NewFanout` handler additionally feeds the otelslog OTLP bridge at boot, so logs go to OTLP when configured (a no-op against the SDK no-op LoggerProvider when off).
- `internal/worker/worker.go` `runJobFromSpec` extracts the untrusted traceparent (fail-closed, never panics) and starts a root `claim` span LINKED to the API span (D-13, shared `trace_id`, NOT parented), then named child spans `sandbox.create`, `handshake.wait`, `compile`, `run`, `publish.result`. `sandbox.create` is a REAL span around `runner.Create` with its ctx threaded into `docker.go`'s `ContainerCreate`/`Attach`/`Start`. No per-chunk spans (OBS-03).
- Domain metrics: `code_runner.jobs.terminal` Int64Counter incremented exactly once per terminal path with a low-cardinality `terminal_state` attribute (`done`/`killed`/`idle_timed_out`/`timed_out`/`error`, D-07), and `code_runner.queue.time` Float64Histogram of `now − enqueuedAtMs` in seconds (OBS-06). `job_id` is never a metric attribute.
- `apps/worker/main.go` calls `otelinit.Init(ctx)` at boot with a deferred shutdown and installs the fan-out slog handler. The worker opens no HTTP port (OBS-05 dropped — verified by the grep gate).
- The 08-01 `trace_test.go` round-trip is re-pointed at the production `extractLinkedSpanContext` (GREEN). New recorder test proves the linked root + named phase spans share the injected trace_id and that N stdout chunks produce zero per-chunk spans; a `ManualReader` metrics test proves the `terminal_state=idle_timed_out`/`done` increments and the queue-time sample, and asserts no `job_id` leaks onto metrics.

## Task Commits
1. **Task 1: Env-gated OTel init + stdout trace-correlation slog handler** — `4a291ef` (feat)
2. **Task 2: Worker traceparent extract + linked root span, phase spans, terminal counter** — `8bbcf2a` (feat)

## Files Created/Modified
- `internal/otelinit/otelinit.go` (new) — `Init`, `IsNone`, `OTLPLogHandler`; no-op gate + W3C propagator + autoexport providers + combined `errors.Join` shutdown.
- `internal/otelinit/otelinit_test.go` (new) — no-op-when-unset gate; W3C propagator set when configured.
- `internal/logging/handler.go` (new) — `NewCtxHandler` (trace_id/span_id/job_id injection), `WithJobID`/`JobIDFromContext`, `NewFanout` (stdout + OTLP).
- `internal/logging/handler_test.go` (new) — within-span fields, omit-without-span, job_id, valid JSON in all states.
- `internal/worker/worker.go` (mod) — extract+link, phase spans, terminal counter, queue-time histogram, lazy tracer/instrument resolution.
- `internal/worker/trace_test.go` (mod) — re-pointed at production `extractLinkedSpanContext`.
- `internal/worker/trace_phase_test.go` (new) — SpanRecorder: linked root, named phase spans, zero per-chunk spans.
- `internal/worker/trace_metrics_test.go` (new) — ManualReader: terminal_state counter, queue.time, no job_id on metrics.
- `internal/runner/docker.go` (mod) — doc comment documenting the `sandbox.create` span context-threading contract (08-02 vs 08-04 ownership). No logic change.
- `apps/worker/main.go` (mod) — OTel boot (Init + shutdown) + fan-out slog handler install.
- `go.mod` / `go.sum` — OTel Go SDK/metric/log + autoexport + otelslog at pinned versions.

## Decisions Made
- Resolved the global tracer/meter instruments **lazily per job** instead of caching them at package init. The OTel global delegating instruments created at init bind to the no-op provider and do NOT re-delegate when a `MeterProvider` is later installed; lazy resolution makes both the boot-time `otelinit.Init` provider and the test-time `ManualReader`/`SpanRecorder` route measurements correctly. Hot-path cost is a cheap instrument lookup; the no-op provider returns no-op instruments.
- Gave `otelslog` a real call site via `otelinit.OTLPLogHandler()` + a `logging.NewFanout` so the dependency is genuinely wired (D-03 OTLP-logs path), not just present in `go.mod`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Lazy instrument/tracer resolution instead of package-init caching**
- **Found during:** Task 2 (ManualReader metrics test returned 0 increments).
- **Issue:** Capturing `otel.Tracer(...)`/`otel.Meter(...).Int64Counter(...)` into package vars at init bound them to the global **no-op** provider; instruments created before a real `MeterProvider` is registered do not re-delegate, so metrics were never recorded against the test reader (and would not record against the boot-time provider either).
- **Fix:** Replaced the cached package vars with `tracer()`, `terminalCounter()`, `queueTimeHist()` helpers that resolve from the CURRENT global provider on each call.
- **Files modified:** `internal/worker/worker.go`
- **Committed in:** `8bbcf2a`

**2. [Rule 1 - Test correctness] Drive the idle-terminal outcome through the real session clock**
- **Found during:** Task 2 (idle_timed_out counter test).
- **Issue:** The session's `terminate` overwrites `result.IdleTimedOut`/`TimedOut` by **termination reason** (session/lifecycle.go), so faking the sandbox's `Wait` result with `IdleTimedOut:true` does not produce an idle outcome.
- **Fix:** Made the test sandbox silent with a long `Wait` and relied on `testSpec`'s short `IdleMs` to fire the REAL idle clock, producing a genuine `idle_timed_out` terminal state.
- **Files modified:** `internal/worker/trace_metrics_test.go`
- **Committed in:** `8bbcf2a`

**Total deviations:** 2 auto-fixed (1 blocking Rule 3, 1 test-correctness Rule 1). No architectural changes; no scope creep.

## Threat Surface
No new inbound surface. The worker still talks only to Redis + soketi + (new, outbound-only) OTLP collector — no HTTP listener (OBS-05 grep gate PASS). The untrusted `traceparent` extract fails closed (T-08-03). No secret or user code/stdin is logged or attributed (T-08-04): only trace_id/span_id/job_id are injected, and metric attributes are low-cardinality with no job_id. No threat flags beyond the plan's register.

## Known Stubs
None. The `internal/runner/docker.go` create-latency histogram is intentionally LEFT to 08-04 (documented as such); 08-02 only threads the span context. This is planned plan-boundary ownership, not an unresolved stub.

## Deferred Issues
Pre-existing, environment-dependent Redis integration-test failures in `internal/jobstore` and `internal/stdintransport` (packages NOT touched by this plan) were observed during the full `go test ./...` run and logged to `deferred-items.md`. They persist after `redis-cli FLUSHALL`, the failing subtest varies run-to-run (flaky), and the go-redis warning `minimal supported value is 1s` points to a Redis-version/test-harness mismatch — not a regression from the OTel changes. All packages this plan touched (`internal/worker`, `internal/otelinit`, `internal/logging`, `apps/worker`, `internal/runner`) are GREEN.

## Verification Evidence
- `go build ./...` clean.
- `go test ./internal/worker/ ./internal/otelinit/ ./internal/logging/ ./apps/worker/ ./internal/runner/` all GREEN.
- `go test ./internal/worker/ -run Traceparent` (08-01 round-trip) GREEN against production extract.
- Recorder test: linked `claim` root (link TraceID == API trace, root trace_id differs) + `sandbox.create`/`handshake.wait`/`run`/`publish.result` under the worker trace; zero per-chunk spans.
- ManualReader test: `code_runner.jobs.terminal{terminal_state=idle_timed_out}` and `{done}` increment; `code_runner.queue.time` sample recorded; no `job_id` on metrics.
- `grep -rn "http.ListenAndServe\|http.Server" apps/worker internal/ | grep -v _test.go` → no listener (OBS-05 dropped).
- `go.mod` lists otel/sdk v1.44.0, otel/sdk/metric v1.44.0, otel/log + sdk/log v0.20.0, autoexport v0.69.0, otelslog v0.19.0.

## Next Phase Readiness
- The Go half of the one connected trace is complete and committed. The API/TS side (parallel wave-2 plan) injects the traceparent the worker now consumes; together they produce one trace_id across API + worker.
- 08-04 can add the `code_runner.sandbox.create.duration` / `.kill.duration` histograms inside `internal/runner/docker.go` `Create`/`Kill` — disjoint from the span context-threading this plan added (documented in the `Create` doc comment).

## Self-Check: PASSED
All claimed files exist on disk and both task commits (`4a291ef`, `8bbcf2a`) are present in the branch history.
