---
phase: 08-distributed-observability
verified: 2026-06-03T15:20:00Z
status: passed
human_verified: 2026-06-03
score: 8/8 must-haves verified
overrides_applied: 1
overrides:
  - must_have: "OBS-05: opt-in Prometheus /metrics scrape endpoint on a separate admin port"
    reason: "Founder decision (D-04) recorded in 08-CONTEXT.md and locked in 08-01-PLAN.md objective: OBS-05 is intentionally dropped. Export is OTLP push only. Domain metrics (OBS-06 set) are all emitted via OTLP. Worker stays HTTP-server-free. Deviation is documented and tracked for future opt-in."
    accepted_by: "phase-planner (locked decision D-04 in 08-CONTEXT.md)"
    accepted_at: "2026-06-03T00:00:00Z"
human_verification:
  - test: "Ran docker compose --profile observability up --build, triggered one interactive execute, confirmed the connected trace via the Jaeger UI/API"
    expected: "The API execute span and the worker phase spans (claim, sandbox.create, handshake.wait, run, publish.result — `compile` is absent for Python, which has no compile step) are connected. Worker root `claim` carries an OTel span LINK (Jaeger FOLLOWS_FROM) back to the API `execute` trace. The otel-collector debug exporter prints received domain metrics and trace-correlated logs."
    result: "PASSED (human-verified 2026-06-03). E2E execute returned exitCode=0 / 'hello World'. API execute trace f34fa5ad04ae9e1304b893e6dee717bd; worker phase trace d5df4c92a5b2af9459f23360e0cbc5d4; worker `claim` span FOLLOWS_FROM -> API execute trace. 9 code_runner.* metrics received by the collector (queue.depth, slots.used/max, queue.time, sandbox.create/kill.duration, jobs.terminal, output.bytes, publish.duration)."
    clarification: "ACCEPTED design per D-13: API and worker are TWO trace_ids joined by a span LINK (the correct OTel pattern for a Redis queue producer->consumer seam), NOT one shared trace_id. The earlier 'share one trace_id' wording is superseded by 'linked traces' — confirmed acceptable by the founder on 2026-06-03."
    why_human: "Live distributed trace topology (span linkage across the process boundary, metric/log ingestion) cannot be verified by static analysis. Founder confirmed the linked-trace topology in Jaeger."
---

# Phase 8: Distributed Observability Verification Report

**Phase Goal:** Make code-runner observable end-to-end across the polyglot seam — OpenTelemetry traces, metrics, and structured logs in both the Hono API and the Go worker — with a bring-your-own OTel stack model that is entirely env-gated (no-op when unset), so self-hosters can wire any backend without forced infrastructure.
**Verified:** 2026-06-03T15:20:00Z
**Status:** passed (8/8 — live trace human-verified 2026-06-03; linked-trace topology accepted per D-13)
**Re-verification:** No — initial verification

---

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | OBS-01: Both services are a true no-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset | VERIFIED | `internal/otelinit/otelinit.go`: `IsNone()` early-return installs no exporter/provider. `apps/api/src/telemetry.ts`: gates `new NodeSDK(...)` on `process.env["OTEL_EXPORTER_OTLP_ENDPOINT"]`. Unit tests pass in `go test ./internal/otelinit/` and `pnpm test telemetry`. |
| 2 | OBS-02: W3C traceparent/tracestate carried in JobSpec across Redis seam | VERIFIED | Schema: `packages/contract/schema/wire.schema.json` has optional `traceparent`/`tracestate`. Generated: `wire.gen.go` has `Traceparent *string`, `wire.gen.go` has `Tracestate *string`. Generated TS: `schemas.ts` marks both `.optional()`. API injects via `propagation.inject` in `execute.ts` before LPUSH. Worker extracts via `propagation.TraceContext{}.Extract` in `worker.go:extractLinkedSpanContext`. |
| 3 | OBS-03: Worker emits phase-level spans (no per-chunk spans); interactive output is metrics | VERIFIED | `worker.go` starts named spans: `claim` (line 423), `sandbox.create` (line 504), `handshake.wait` (line 579), `compile` (line 622), `run` (line 744), `publish.result` (line 549). Output counter at `pump.go` increments bytes, not spans. `go test ./internal/worker/` passes including anti-per-chunk-span test. |
| 4 | OBS-04: Telemetry exported via OTLP push (traces + metrics + logs) | VERIFIED | `otelinit.go` builds TracerProvider (WithBatcher/OTLP), MeterProvider (autoexport reader), LoggerProvider (BatchProcessor/OTLP logs). `telemetry.ts` builds NodeSDK with `OTLPTraceExporter`, `OTLPMetricExporter`, `BatchLogRecordProcessor(OTLPLogExporter)`. No pull endpoint added. |
| 5 | OBS-05: Prometheus /metrics pull endpoint on admin port | PASSED (override) | Intentionally dropped per founder decision D-04 (08-CONTEXT.md). Domain metrics (OBS-06 set) are fully emitted via OTLP push. Worker remains HTTP-server-free — confirmed: `grep -rn "http.ListenAndServe\|http.Server" apps/worker internal/` returns zero matches in non-test files. |
| 6 | OBS-06: Domain metrics emitted (queue depth, slots, sandbox latency, terminal counts, etc.) | VERIFIED | `worker.go`: `code_runner.jobs.terminal` (counter + terminal_state attr), `code_runner.queue.time` (histogram). `docker.go`: `code_runner.sandbox.create.duration`, `code_runner.sandbox.kill.duration` (Float64Histogram). `pump.go`: `code_runner.output.bytes` (counter). `reaper.go`: `code_runner.reaper.orphans` (counter). `publisher.go`: `code_runner.publish.duration` (histogram), `code_runner.publish.errors` (counter). `worker.go RegisterMetrics()`: `code_runner.queue.depth`, `code_runner.slots.used`, `code_runner.slots.max` (observable gauges). API: `metrics.ts` exports `code_runner.admission.rejected` and `code_runner.ratelimit.rejected` wired in `admission.ts` and `ratelimit.ts`. `go test ./internal/runner/ ./internal/publisher/` PASS. |
| 7 | OBS-07: Both services emit structured JSON logs with trace_id/span_id/job_id | VERIFIED | Go: `internal/logging/handler.go` `ctxHandler` injects `trace_id`/`span_id`/`job_id` from context. `apps/worker/main.go` installs fanout handler (stdout JSON + OTLP bridge). TS: `apps/api/src/logger.ts` uses pino + `AsyncLocalStorage<{jobId}>` mixin. `execute.ts` wraps handler body in `jobContext.run`. No `console.log`/`console.error` remains anywhere in `apps/api/src/` (full grep confirms zero matches). `go test ./internal/logging/` and `pnpm test telemetry` PASS. |
| 8 | OBS-08: Configurable sampling; example collector + Jaeger in compose; all OTEL_* vars documented | VERIFIED | `docker-compose.yml`: `otel-collector` and `jaeger` under `profiles: [observability]`, inert on default up. `observability/otel-collector.yaml`: OTLP receiver (http:4318, grpc:4317), batch processor, `otlp/jaeger` + `debug` exporters, three pipelines (traces/metrics/logs). `.env.example`: 14 OTEL_-prefixed lines covering endpoint, protocol, service names, resource attrs, sampler (`parentbased_traceidratio`), sampler arg (1.0), kill-switches. `docker compose config` (default) renders without otel-collector/jaeger. `docker compose --profile observability config` renders both services on the `code-runner` network. |

**Score:** 7/8 truths verified (OBS-05 PASSED via override; 1 human-verification item for OBS-02/08 live Jaeger proof)

---

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `internal/otelinit/otelinit.go` | Env-gated OTel provider (traces+metrics+logs) + W3C propagator + no-op gate | VERIFIED | `IsNone()` early-return; `autoexport` factory for all three signals; `otel.SetTextMapPropagator(propagation.TraceContext{})` always called on configured path |
| `internal/logging/handler.go` | Custom slog.Handler injecting trace_id/span_id/job_id from ctx | VERIFIED | `ctxHandler.Handle()` reads `trace.SpanContextFromContext(ctx)` + `JobIDFromContext(ctx)`; `NewFanout()` for stdout+OTLP bridge |
| `apps/worker/main.go` | Calls `otelinit.Init` + installs logging handler | VERIFIED | Lines 62–83 in `run()`: `otelinit.Init(ctx)` with defer shutdown; `logging.NewFanout(stdoutHandler, otelinit.OTLPLogHandler())` |
| `internal/worker/worker.go` | traceparent extract + linked root span + phase spans + terminal counter + queue time | VERIFIED | `extractLinkedSpanContext()` (line 98); `tracer().Start(ctx, "claim", trace.WithLinks(...))` (line 423); all six phase spans present; `terminalCounter()` + `queueTimeHist()` |
| `apps/api/src/telemetry.ts` | Env-gated NodeSDK bootstrap loaded via --import | VERIFIED | Gates on `OTEL_EXPORTER_OTLP_ENDPOINT`; `package.json` dev/start both have `--import ./src/telemetry.ts`; `Dockerfile` CMD has `--import ./src/telemetry.ts` |
| `apps/api/src/logger.ts` | pino singleton + AsyncLocalStorage job_id mixin | VERIFIED | `export const jobContext = new AsyncLocalStorage<{jobId:string}>()`; `mixin()` reads `jobContext.getStore()?.jobId` |
| `apps/api/src/routes/execute.ts` | execute span + propagation.inject into spec.traceparent before LPUSH | VERIFIED | `tracer.startActiveSpan("execute", ...)` wraps spec-build + enqueue; `propagation.inject(context.active(), carrier)` before `pipeline.lpush`; `jobContext.run({jobId}, ...)` wraps handler |
| `apps/api/src/metrics.ts` | API meter + admission/ratelimit rejection counters | VERIFIED | `metrics.getMeter("code-runner-api")`; `code_runner.admission.rejected` + `code_runner.ratelimit.rejected` exported; wired in `admission.ts:50` and `ratelimit.ts:63/115` |
| `observability/otel-collector.yaml` | OTLP receiver + traces→Jaeger + metrics/logs→debug pipelines | VERIFIED | http:4318 + grpc:4317 receivers; `otlp/jaeger` exporter pointing at `jaeger:4317`; `debug` exporter; three pipelines |
| `docker-compose.yml` | otel-collector + jaeger under profiles: [observability] | VERIFIED | Both services present with `profiles: [observability]`; `docker compose config` (default) excludes them; `--profile observability config` includes them on `code-runner` network |
| `.env.example` | All OTEL_* vars documented | VERIFIED | 14 OTEL_ lines covering endpoint, protocol, service names, resource attrs, sampler, sampler arg, kill-switches; all commented → true no-op |
| `packages/code-runner-sdk-node/src/client.ts` | Optional-peer traceparent injection on /v1/execute | VERIFIED | `injectTraceparent(headers)` via `try { const api = await import("@opentelemetry/api"); api.propagation.inject(api.context.active(), headers) } catch {}` guarded by `path === "/v1/execute"` |
| `packages/contract/schema/wire.schema.json` | Optional traceparent/tracestate on JobSpec.properties | VERIFIED | Lines 126–127: both fields present, type string, NOT in required array |
| `internal/runner/docker.go` | sandbox.create/kill latency histograms | VERIFIED | `sandboxCreateDuration()` + `sandboxKillDuration()` at lines 55–70; `.Record` calls inside `Create`/`Kill` bodies |
| `internal/publisher/publisher.go` | publish.duration histogram + publish.errors counter | VERIFIED | `publishDuration()` + `publishErrors()` defined; wraps `pusherTriggerer.Trigger` at publish chokepoint |
| `internal/jobstore/queue.go` | QueueDepth(ctx) LLEN wrapper | VERIFIED | `func (s *Store) QueueDepth(ctx context.Context) (int64, error)` at line 61; used in `worker.go RegisterMetrics()` gauge callback |
| `internal/session/pump.go` | output bytes counter (no per-chunk spans) | VERIFIED | `outputBytesCounter()` at line 29; incremented by byte count at pump budget path; no span creation in pump |
| `internal/reaper/reaper.go` | reaper.orphans counter | VERIFIED | `reaperOrphans()` at line 70; incremented after successful `ContainerRemove` |
| `scripts/observability-e2e.sh` | E2E observability script | VERIFIED | File exists and is executable |

---

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `apps/api/src/routes/execute.ts` | `spec.traceparent` | `propagation.inject(context.active(), carrier)` | WIRED | Line 140: carrier built, propagation.inject called, `spec.traceparent = carrier["traceparent"]` before pipeline.lpush |
| `apps/api/package.json` + `Dockerfile` | `apps/api/src/telemetry.ts` | `node --import ./src/telemetry.ts` | WIRED | Both `dev` and `start` scripts and `Dockerfile` CMD contain `--import ./src/telemetry.ts` before `src/server.ts` |
| `apps/worker/main.go` | `internal/otelinit` | `otelinit.Init(ctx)` | WIRED | Line 62 in `run()`: `otelinit.Init(ctx)` with deferred shutdown |
| `internal/worker/worker.go` | `spec.Traceparent` | `propagation.TraceContext{}.Extract` | WIRED | `extractLinkedSpanContext()` builds `propagation.MapCarrier{}`, calls `propagation.TraceContext{}.Extract`, then `trace.SpanContextFromContext` |
| `internal/worker/worker.go` | `otel.Tracer("code-runner-worker")` | `trace.WithLinks(trace.Link{SpanContext: linkedSC})` | WIRED | Line 423: `tracer().Start(ctx, "claim", trace.WithLinks(trace.Link{SpanContext: linkedSC}))` |
| `internal/jobstore/queue.go` | `code_runner.queue.depth` observable gauge | `QueueDepth(ctx)` LLEN in callback | WIRED | `QueueDepth` method calls `s.client.LLen(ctx, keys.JobQueue)`; used in `RegisterMetrics()` gauge callback |
| `internal/runner/docker.go` | `code_runner.sandbox.create.duration` / `.kill.duration` | `Float64Histogram.Record` inside Create/Kill | WIRED | `sandboxCreateDuration().Record(...)` and `sandboxKillDuration().Record(...)` in respective function bodies |
| `internal/publisher/publisher.go` | `code_runner.publish.duration` / `.errors` | wrap `pusherTriggerer.Trigger` | WIRED | `publishDuration()` + `publishErrors()` wrap the Trigger call at the publish chokepoint |
| `apps/api/src/admission.ts` | `code_runner.admission.rejected` | `admissionRejections.add(1)` | WIRED | Line 50: `admissionRejections.add(1)` in `admissionError()` helper |
| `apps/api/src/ratelimit.ts` | `code_runner.ratelimit.rejected` | `ratelimitRejections.add(1, {reason})` | WIRED | Lines 63 and 115: `ratelimitRejections.add(1, { reason: "frame_rate" })` and `{ reason: "byte_cap" }` |
| `docker-compose.yml` | `observability/otel-collector.yaml` | `otel-collector` + `jaeger` under `profiles: [observability]` | WIRED | `otel-collector` service mounts `./observability/otel-collector.yaml`; both under `profiles: [observability]`; api/worker have `OTEL_EXPORTER_OTLP_ENDPOINT` env var wired |
| `packages/code-runner-sdk-node/src/client.ts` | `/v1/execute` request headers | `propagation.inject(context.active(), headers)` | WIRED | `injectTraceparent(headers)` called inside `if (path === "/v1/execute")` guard; `@opentelemetry/api` is optional peerDependency |

---

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|-------------------|--------|
| `worker.go` root span | `linkedSC` (SpanContext) | `extractLinkedSpanContext(spec)` → `propagation.TraceContext{}.Extract` on `spec.Traceparent` | Yes — real W3C hex trace_id from API inject | FLOWING |
| `worker.go` terminal counter | `terminal_state` attribute | derived from `result.TimedOut`, `result.IdleTimedOut`, `result.ExitCode`, kill path | Yes — real job outcome | FLOWING |
| `pump.go` output bytes | byte count | `n` from `io.CopyN` / chunk write in budget path | Yes — real forwarded bytes | FLOWING |
| `queue.go` queue depth gauge | `int64` LLEN result | `s.client.LLen(ctx, keys.JobQueue)` live Redis call | Yes — real Redis LLEN | FLOWING |
| `execute.ts` traceparent inject | `carrier["traceparent"]` | `propagation.inject(context.active(), carrier)` from active span | Yes — real W3C traceparent when SDK active; absent when no SDK (no-op) | FLOWING |
| `metrics.ts` admission counter | counter increment | `admissionRejections.add(1)` at real 429 rejection path | Yes — triggered by real admission gate | FLOWING |

---

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Go build succeeds with new OTel deps | `go build ./...` | exit 0, no output | PASS |
| otelinit unit tests: no-op gate + W3C propagator | `go test ./internal/otelinit/` | ok, 0.606s | PASS |
| logging handler: trace_id/span_id/job_id injection | `go test ./internal/logging/` | ok, 2.672s | PASS |
| worker: traceparent extract + linked span + phase spans | `go test ./internal/worker/` | ok, 2.746s | PASS |
| runner: sandbox.create/kill latency histograms | `go test ./internal/runner/` | ok, 1.842s | PASS |
| publisher: publish.duration + publish.errors | `go test ./internal/publisher/` | ok, 2.017s | PASS |
| reaper: orphans counter | `go test ./internal/reaper/` | ok, 2.395s | PASS |
| session/pump: output bytes counter, no per-chunk spans | `go test ./internal/session/` | ok, 1.720s | PASS |
| API tests (all 61): telemetry no-op gate, execute span+inject, metrics counters, pino migration | `REDIS_URL=redis://127.0.0.1:6379 pnpm --filter @code-runner/api test --run` | 61 passed (8 files) | PASS |
| SDK Node tests (15): caller traceparent injection, no-op path, execute-only guard | `pnpm --filter @teovilla/code-runner-sdk-node test` | 15 passed | PASS |
| Default compose profile excludes observability services | `docker compose config` | no otel-collector/jaeger rendered | PASS |
| Observability profile includes otel-collector + jaeger | `docker compose --profile observability config` | both services rendered on code-runner network | PASS |
| .env.example documents OTEL_* vars (>= 6) | `grep -c OTEL_ .env.example` | 14 | PASS |
| No job_id on metric attributes | `grep -rn "job_id.*WithAttributes\|attribute.String.*job" internal/` | zero matches | PASS |
| No new HTTP listener in worker | `grep -rn "http.ListenAndServe\|http.Server" apps/worker internal/` (non-test) | zero matches | PASS |
| No console.* in apps/api/src | `grep -rn "console\.(log\|error)" apps/api/src/` | zero matches | PASS |

---

### Probe Execution

No conventional `scripts/*/tests/probe-*.sh` probes declared or found for this phase. Step 7c: SKIPPED (no probe files for phase 08).

---

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|-------------|-------------|--------|----------|
| OBS-01 | 08-02, 08-03 | No-op when OTEL_EXPORTER_OTLP_ENDPOINT unset | SATISFIED | `otelinit.IsNone()` gate in Go; `if (endpoint)` gate in `telemetry.ts`; unit tests confirm no exporter constructed when unset |
| OBS-02 | 08-01, 08-02, 08-03, 08-05 | W3C traceparent/tracestate in wire contract + propagated API→worker | SATISFIED | Schema + generated artifacts carry optional fields; API injects; worker extracts + links; SDK propagates caller context |
| OBS-03 | 08-02 | Phase-level worker spans; interactive output as metrics not spans | SATISFIED | Six named spans in `worker.go`; `code_runner.output.bytes` counter in `pump.go`; recorder test asserts zero per-chunk spans |
| OBS-04 | 08-02, 08-03 | OTLP push export (traces + metrics + logs) | SATISFIED | Both Go and TS use OTLP proto exporters for all three signals via autoexport/sdk-node |
| OBS-05 | (none — descoped) | Prometheus /metrics pull endpoint | PASSED (override) | Intentionally dropped per D-04 (founder decision). OTLP push is the single export path. No admin port opened on either service. |
| OBS-06 | 08-04, 08-04b | Domain metrics set | SATISFIED | All instruments present: queue.depth, slots.used/max, queue.time, jobs.terminal, sandbox.create/kill.duration, output.bytes, reaper.orphans, publish.duration/errors, admission.rejected, ratelimit.rejected |
| OBS-07 | 08-02, 08-03, 08-04b | Structured JSON logs with trace_id/span_id/job_id; API off console.* | SATISFIED | Go `ctxHandler` injects correlation fields; pino + AsyncLocalStorage in API; zero console.* in apps/api/src |
| OBS-08 | 08-05 | Configurable sampling; example collector in compose; OTEL_* documented | SATISFIED | `parentbased_traceidratio` + `1.0` default in compose env and .env.example; otel-collector.yaml has tail-sampling note; 14 OTEL_ vars documented |

---

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `apps/api/src/redis.ts` (pre-08-03) | was line 19 | `console.error` | — | Resolved: migrated to `getLogger().error(...)` in 08-04b; zero console.* remain in apps/api/src |
| `apps/api/src/channelAuth.ts` (pre-08-03) | was line 75 | `console.error` | — | Resolved: migrated to `getLogger().error(...)` in 08-04b |

No active anti-patterns found in the phase-08 modified files. Zero TBD/FIXME/XXX debt markers in any of the modified files.

---

### Human Verification Required

#### 1. Connected Trace in Jaeger (OBS-02 / OBS-08 live proof)

**Test:** Run `bash scripts/observability-e2e.sh` (or manually: `cp .env.example .env`, uncomment `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318` and the service-name/sampler lines, then `docker compose --profile observability up --build` and trigger one interactive execute)

**Expected:**
1. Jaeger UI at http://localhost:16686 shows a trace for service `code-runner-api` containing BOTH the API `execute` span AND the worker phase spans (`claim`, `sandbox.create`, `handshake.wait`, `compile`, `run`, `publish.result`) under ONE shared `trace_id` (worker spans linked to the execute span).
2. `docker compose --profile observability logs otel-collector` shows the `debug` exporter printing received metrics (e.g. `code_runner.jobs.terminal`, `code_runner.queue.depth`) and trace-correlated log records.
3. Stopping the observability stack and running plain `docker compose up` with `OTEL_*` unset completes a full execute with zero telemetry overhead and no new port opened.

**Why human:** Live distributed trace topology in a UI (span linkage, shared trace_id across process boundaries, metrics + log ingestion visible in Jaeger/collector output) cannot be verified by static code analysis or unit tests alone. All code and config required is present and statically validated; this checkpoint confirms the runtime wiring produces the observable outcome.

---

### Gaps Summary

No code or configuration gaps were found. All eight OBS requirements are either verified in the codebase (OBS-01 through OBS-04, OBS-06 through OBS-08) or covered by a locked founder-decision override (OBS-05). The single outstanding item is the live Jaeger trace confirmation, which requires a human to run the observability stack and visually confirm the connected trace in the UI.

---

_Verified: 2026-06-03T15:20:00Z_
_Verifier: Claude (gsd-verifier)_
