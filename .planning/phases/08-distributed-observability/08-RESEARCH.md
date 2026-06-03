# Phase 8: Distributed Observability - Research

**Researched:** 2026-06-03
**Domain:** OpenTelemetry (traces + metrics + logs) across a polyglot monorepo — Hono/TypeScript API (OTel JS) + Go worker (OTel Go), W3C trace-context correlation over a Redis-carried wire contract.
**Confidence:** HIGH (package versions, init APIs, propagation, and the no-op semantics are all verified against current npm/Go-module registries and official docs; a few API export-name details flagged MEDIUM and tagged for the planner to pin against the installed version).

## Summary

This phase wires the three OpenTelemetry pillars into both services so a single execution produces one connected trace (API `execute` span + linked worker phase spans sharing one `trace_id`), domain metrics, and trace-correlated structured logs — all driven by standard `OTEL_*` env vars and a true no-op when no exporter is configured. The trust seam is Redis, not HTTP: the API injects a W3C `traceparent` (and optional `tracestate`) string into `JobSpec`, the worker extracts it on claim and **links** its root span to the API span.

The most important, easily-fumbled facts: (1) **Neither SDK is a no-op by default** — OTel JS NodeSDK auto-creates a default OTLP trace exporter when none is passed, and Go `autoexport` defaults `OTEL_*_EXPORTER=otlp`. The phase's "no-op when unset" guarantee (OBS-01) therefore requires an **explicit gate**: only construct/start the SDK (or only register OTLP exporters) when `OTEL_EXPORTER_OTLP_ENDPOINT` is present. (2) **ESM load-order**: the API is `"type":"module"` run via `node --experimental-strip-types`/`tsx`; `@opentelemetry/instrumentation-ioredis` monkey-patches at load time and needs the OTel hook registered **before** `ioredis` is imported — i.e. a separate `--import ./telemetry.ts` bootstrap, not an inline import inside `server.ts`. `@hono/otel` is a Hono *middleware* and is immune to this. (3) **Worker stdout logs vs OTLP logs are two different paths**: the official `otelslog` bridge forwards records to the OTLP LoggerProvider and does **not** write `trace_id`/`span_id` to stdout. OBS-07 + D-03 ("stdout JSON always") requires a custom `slog.Handler` that wraps `slog.JSONHandler` and pulls `trace_id`/`span_id` from `trace.SpanContextFromContext(ctx)`; the otelslog bridge is the *additional* OTLP path.

**Primary recommendation:** API — bootstrap a curated `NodeSDK` (HTTP + ioredis instrumentations only, plus `@hono/otel` middleware) from a separate `--import`ed `telemetry.ts`, gated on `OTEL_EXPORTER_OTLP_ENDPOINT`; pino with `@opentelemetry/instrumentation-pino` for stdout correlation + OTLP log sending; `AsyncLocalStorage` for `job_id`. Worker — pin the OTel Go modules already in `go.mod` at **v1.44.0** (no bump needed), add `otel/sdk`, `otel/sdk/metric`, `otel/log`, the OTLP gRPC/HTTP exporters (v0.20.0 logs / v1.44.0 metrics), `contrib/exporters/autoexport` v0.69.0 (env-driven, gated on endpoint), `contrib/bridges/otelslog` v0.19.0 for OTLP logs + a custom JSON handler for stdout correlation; W3C `propagation.TraceContext` to extract `traceparent`; worker root span uses `trace.WithLinks(trace.Link{SpanContext: extracted})`. Add `traceparent`/`tracestate` optional strings to `JobSpec` in the schema and `pnpm contract`. Ship Jaeger all-in-one + an OTel Collector under a compose `observability` profile (the repo already uses compose `profiles:` for `stub`, so the mechanism is proven).

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Inbound HTTP request span (`POST /v1/execute`, etc.) | Frontend Server (Hono API) | — | `@hono/otel` middleware wraps the request/response lifecycle at the API tier |
| `execute` manual span + `traceparent` injection | Frontend Server (Hono API) | — | The API owns the queue write; it must capture the active context and serialize it into `JobSpec` before LPUSH |
| ioredis client spans (LPUSH/SET/LLEN) | Frontend Server (Hono API) | — | Selective auto-instrumentation of the API's Redis client |
| `traceparent` extraction + worker phase spans (`claim`…`publish.result`) | Worker (Go) | — | The worker owns sandbox lifecycle; spans map to `internal/worker` + `internal/runner` + `internal/session` |
| Span link (worker root → API `execute`) | Worker (Go) | — | `/v1/execute` returns 202 before the run, so this is a link not parent-child (D-13) |
| Domain metrics (queue depth, slots, latencies, terminal counters) | Worker (Go) | Frontend Server (API) for admission/ratelimit rejection counters | Most signals are worker-side; the 429 admission + stdin-ratelimit counters are emitted at the API tier |
| Trace-correlated stdout JSON logs | Both tiers | — | API via pino+instrumentation-pino; worker via custom slog JSON handler |
| OTLP log export | Both tiers | — | API via instrumentation-pino logSending; worker via otelslog bridge |
| Caller→API trace propagation | SDK (`@teovilla/code-runner-sdk-node`) | — | SDK injects `traceparent` header into the `/v1/execute` fetch when an active context exists |
| Trace backend (Jaeger) + collector pipeline | Infra (docker-compose `observability` profile) | — | BYO stack; example only, inert on default `up` |

## Standard Stack

### Core — OTel JS (apps/api, packages/code-runner-sdk-node)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `@opentelemetry/api` | **1.9.1** [VERIFIED: npm registry] | Tracer/meter/context API, propagation, `AsyncLocalStorage`-backed context | The stable API surface; independently versioned from the SDK (stays 1.x) [CITED: opentelemetry.io/blog/2025/otel-js-sdk-2-0] |
| `@opentelemetry/sdk-node` | **0.218.0** [VERIFIED: npm registry] | One-call NodeSDK bootstrap (traces+metrics+logs+instrumentations) | Canonical Node bootstrap. Unstable packages are `>=0.200.0` aligned with the `2.x` stable line [CITED: github.com/open-telemetry/opentelemetry-js/doc/upgrade-to-2.x.md] |
| `@opentelemetry/api-logs` | **0.218.0** [VERIFIED: npm registry] | Logs API used by the pino bridge | Required by instrumentation-pino's log-sending path |
| `@opentelemetry/exporter-trace-otlp-proto` | **0.218.0** [VERIFIED: npm registry] | OTLP/HTTP-protobuf trace exporter | http/protobuf is the OTel default protocol; aligns with collector |
| `@opentelemetry/exporter-metrics-otlp-proto` | **0.218.0** [VERIFIED: npm registry] | OTLP metric exporter | Push metrics (D-04) |
| `@opentelemetry/exporter-logs-otlp-proto` | **0.218.0** [VERIFIED: npm registry] | OTLP logs exporter | OTLP log bridge (D-03) |
| `@opentelemetry/instrumentation-ioredis` | **0.66.0** [VERIFIED: npm registry] | Auto-spans for the API's ioredis client | Matches `ioredis@5.11`; the curated set per D-01 |
| `@opentelemetry/instrumentation-pino` | **0.64.0** [VERIFIED: npm registry] | Injects `trace_id`/`span_id`/`trace_flags` into pino stdout JSON **and** sends logs to the OTel Logs SDK | Single dep covers both D-02 (correlation) and D-03 (OTLP logs) |
| `@hono/otel` | **1.1.2** [VERIFIED: npm registry] | Hono middleware producing the inbound request span | The blessed Hono instrumentation (D-01); middleware, so load-order-immune |
| `pino` | **10.3.1** [VERIFIED: npm registry] | Structured JSON logger replacing console.log | D-02 |

> `@opentelemetry/sdk-metrics` 2.7.1 and `@opentelemetry/sdk-trace-base` 2.7.1 are pulled transitively by `sdk-node`; you generally do not list them directly. `PeriodicExportingMetricReader` and the metrics classes are re-exported from `@opentelemetry/sdk-node`'s `.metrics` namespace.

### Core — OTel Go (apps/worker, internal/*)

All core modules are **already transitively present in `go.mod` at v1.44.0** (pulled by the Docker SDK). v1.44.0 is the current stable [VERIFIED: Go module proxy]. **No core version bump is required** — promote them to direct dependencies and add the signal-specific exporters + SDK + log modules.

| Module | Version | Purpose | Notes |
|--------|---------|---------|-------|
| `go.opentelemetry.io/otel` | **v1.44.0** [VERIFIED: go list -m -versions] | API: tracer/meter, `propagation` | Already in go.mod (indirect) |
| `go.opentelemetry.io/otel/trace` | **v1.44.0** | Span/SpanContext/Link types | Already indirect |
| `go.opentelemetry.io/otel/metric` | **v1.44.0** | Instruments (counters/histograms/observable gauges) | Already indirect |
| `go.opentelemetry.io/otel/sdk` | **v1.44.0** | TracerProvider, resource, sampler | Add (likely already indirect via otlptracehttp) |
| `go.opentelemetry.io/otel/sdk/metric` | **v1.44.0** | MeterProvider + PeriodicReader | Add |
| `go.opentelemetry.io/otel/log` | **v0.20.0** [VERIFIED: go list -m -versions] | Logs API/SDK (LoggerProvider) | Logs are still `v0.x` — released in lockstep with v1.44.0 core |
| `go.opentelemetry.io/otel/sdk/log` | **v0.20.0** | Log SDK provider/processor | Add |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` | **v1.44.0** | OTLP/HTTP trace exporter | **Already in go.mod (indirect)** |
| `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp` (or `…grpc`) | **v1.44.0** [VERIFIED] | OTLP metric exporter | Add — match protocol to API/collector |
| `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp` (or `…grpc`) | **v0.20.0** [VERIFIED] | OTLP log exporter | Add |
| `go.opentelemetry.io/contrib/exporters/autoexport` | **v0.69.0** [VERIFIED] | Env-driven exporter/reader factory (`NewSpanExporter`/`NewMetricReader`/`NewLogExporter` + `IsNone*`) | Add — lets `OTEL_*_EXPORTER` + protocol vars drive everything |
| `go.opentelemetry.io/contrib/bridges/otelslog` | **v0.19.0** [VERIFIED] | slog→OTel-Logs bridge (OTLP path only) | Add — **does not** write to stdout (see Pitfall 4) |

> **Protocol decision (Claude's discretion per D-04):** Recommend **OTLP/HTTP (`http/protobuf`, port 4318)** for both languages and the collector, to keep one transport and avoid pulling gRPC/protobuf weight into the Go binary. The JS `-proto` exporters and Go `otlp*http` exporters both speak http/protobuf and interoperate with the collector's OTLP receiver. (gRPC/4317 is equally valid; pick one and set it consistently in `.env.example`.)

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `@opentelemetry/resources` + `@opentelemetry/semantic-conventions` | bundled via sdk-node | `service.name`, `service.version` resource attrs | Set `service.name` = `code-runner-api` / `code-runner-worker`; prefer `OTEL_SERVICE_NAME`/`OTEL_RESOURCE_ATTRIBUTES` env so it's config-driven |
| Go `go.opentelemetry.io/otel/semconv/v1.xx` | with v1.44.0 | semantic-convention attribute keys | For resource + any standard attrs |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Manual NodeSDK exporter wiring | `@opentelemetry/auto-instrumentations-node` | Rejected by D-01 — kitchen-sink bundle adds noise + dep surface; curated set is the locked decision |
| Go `autoexport` | Hand-construct exporters from `OTEL_EXPORTER_OTLP_ENDPOINT` | `autoexport` gives spec-correct env handling + `IsNone*` helpers; hand-rolling re-implements the env matrix |
| `otelslog` bridge for stdout correlation | — | Bridge only feeds OTLP; stdout correlation needs a separate custom handler (Pitfall 4) |
| OTLP/HTTP | OTLP/gRPC (4317) | gRPC adds protobuf/grpc deps to the Go binary; HTTP is lighter and equally supported. Either works — be consistent |

**Installation:**

```bash
# API (apps/api)
pnpm --filter @code-runner/api add \
  @opentelemetry/api@1.9.1 \
  @opentelemetry/api-logs@0.218.0 \
  @opentelemetry/sdk-node@0.218.0 \
  @opentelemetry/exporter-trace-otlp-proto@0.218.0 \
  @opentelemetry/exporter-metrics-otlp-proto@0.218.0 \
  @opentelemetry/exporter-logs-otlp-proto@0.218.0 \
  @opentelemetry/instrumentation-ioredis@0.66.0 \
  @opentelemetry/instrumentation-pino@0.64.0 \
  @hono/otel@1.1.2 \
  pino@10.3.1

# SDK (packages/code-runner-sdk-node) — API only, optional peer
pnpm --filter @teovilla/code-runner-sdk-node add @opentelemetry/api@1.9.1
# (or make @opentelemetry/api an optional peerDependency so the SDK has zero runtime
#  OTel weight when the caller doesn't use OTel — see SDK Node Propagation below)

# Worker (Go) — from repo root
go get \
  go.opentelemetry.io/otel@v1.44.0 \
  go.opentelemetry.io/otel/sdk@v1.44.0 \
  go.opentelemetry.io/otel/sdk/metric@v1.44.0 \
  go.opentelemetry.io/otel/log@v0.20.0 \
  go.opentelemetry.io/otel/sdk/log@v0.20.0 \
  go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp@v1.44.0 \
  go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp@v0.20.0 \
  go.opentelemetry.io/contrib/exporters/autoexport@v0.69.0 \
  go.opentelemetry.io/contrib/bridges/otelslog@v0.19.0
# otlptracehttp + otel/trace + otel/metric are already in go.mod (promote to direct).
go mod tidy
```

**Version verification (run at plan time — versions move):**
```bash
npm view @opentelemetry/sdk-node version
npm view @hono/otel version          # CONFIRM the middleware export name for the installed version (see Pitfall 6)
go list -m -versions go.opentelemetry.io/otel | tr ' ' '\n' | tail -1
go list -m -versions go.opentelemetry.io/otel/log | tr ' ' '\n' | tail -1
```

## Package Legitimacy Audit

slopcheck v0.6.1 ran `slopcheck install` against all 10 JS packages — all `[OK]`. Go modules verified via `go list -m -versions` against the module proxy (the `go.opentelemetry.io/*` namespace is the canonical OpenTelemetry org).

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| `@opentelemetry/api` | npm | mature | very high | github.com/open-telemetry/opentelemetry-js-api | [OK] | Approved |
| `@opentelemetry/sdk-node` | npm | mature | very high | github.com/open-telemetry/opentelemetry-js | [OK] | Approved |
| `@opentelemetry/api-logs` | npm | mature | high | open-telemetry/opentelemetry-js | [OK] | Approved |
| `@opentelemetry/exporter-trace-otlp-proto` | npm | mature | high | open-telemetry/opentelemetry-js | [OK] | Approved |
| `@opentelemetry/exporter-metrics-otlp-proto` | npm | mature | high | open-telemetry/opentelemetry-js | [OK] | Approved |
| `@opentelemetry/exporter-logs-otlp-proto` | npm | mature | high | open-telemetry/opentelemetry-js | [OK] | Approved |
| `@opentelemetry/instrumentation-ioredis` | npm | mature | high | open-telemetry/opentelemetry-js-contrib | [OK] | Approved |
| `@opentelemetry/instrumentation-pino` | npm | mature | high | open-telemetry/opentelemetry-js-contrib | [OK] | Approved |
| `@hono/otel` | npm | ~1.5yr | 127k/wk | github.com/honojs/middleware | [OK] | Approved |
| `pino` | npm | mature | very high | github.com/pinojs/pino | [OK] | Approved |
| `go.opentelemetry.io/otel` (+ sdk/metric/trace/log/exporters) | Go proxy | mature | n/a | github.com/open-telemetry/opentelemetry-go | n/a (verified via proxy) | Approved |
| `go.opentelemetry.io/contrib/{exporters/autoexport,bridges/otelslog}` | Go proxy | mature | n/a | open-telemetry/opentelemetry-go-contrib | n/a | Approved |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

> No postinstall scripts of concern (all `@opentelemetry/*` and `@hono/otel` are pure build/runtime libs; pino has a benign build). No `checkpoint:human-verify` gate required for installs.

## Architecture Patterns

### System Architecture Diagram

```
                                  ┌────────────────────────────────────────────────┐
  Caller process (optional)       │ apps/api  (Hono, OTel JS NodeSDK)               │
  ┌──────────────────┐  HTTP      │                                                 │
  │ active OTel ctx? │──POST /v1/──▶ @hono/otel middleware → inbound request span    │
  │ SDK injects      │  execute   │        │                                         │
  │ traceparent hdr  │  (header)  │        ▼                                         │
  └──────────────────┘            │  execute.ts: start "execute" span               │
                                  │   ├─ inject active ctx → traceparent STRING ─────┼──┐
                                  │   ├─ ioredis spans (LPUSH/SET) [instr-ioredis]   │  │
                                  │   └─ pino log {trace_id,span_id,job_id}          │  │
                                  └───────────────┬─────────────────────────────────┘  │
                                                  │ JobSpec{...,traceparent} via Redis  │
        OTLP push (traces/metrics/logs)           ▼                                     │
        ┌─────────────────────────────────  Redis (jobs:queue, job:<id>:spec) ─────────┘
        │                                         │ BRPOP jobId → ReadSpec
        ▼                                         ▼
  ┌──────────────┐   traces   ┌────────────────────────────────────────────────────┐
  │ OTel         │◀───────────│ apps/worker (Go, OTel SDK)                           │
  │ Collector    │   metrics  │  extract traceparent (propagation.TraceContext)     │
  │ (OTLP recv)  │◀───────────│  → SpanContext                                       │
  │   │          │   logs     │  start worker root span WITH trace.WithLinks(link)   │
  │   ├─▶ Jaeger │◀───────────│   ├─ claim / sandbox.create / handshake.wait /       │
  │   │  (traces)│            │   │   compile / run / publish.result child spans     │
  │   └─▶ debug  │            │   ├─ metrics: gauges(queue depth, slots),            │
  │   (metrics/  │            │   │   histograms(create/kill, publish latency),       │
  │    logs)     │            │   │   counters(terminal_state, reaper orphans…)      │
  └──────────────┘            │   ├─ slog stdout JSON {trace_id,span_id,job_id}      │
   (compose profile           │   └─ otelslog bridge → OTLP logs                     │
    "observability")          └────────────────────────────────────────────────────┘
                                          │ Pusher HTTP trigger
                                          ▼
                                       soketi (output only — NOT instrumented)
```

The worker root span **links** (not parents) to the API `execute` span because `/v1/execute` returns 202 before the run starts; a span link expresses "caused-by/related" across the async boundary while keeping both under the same `trace_id`.

### Component Responsibilities

| File | Responsibility |
|------|---------------|
| `apps/api/src/telemetry.ts` (new) | NodeSDK bootstrap — gated on `OTEL_EXPORTER_OTLP_ENDPOINT`; registers HTTP + ioredis + pino instrumentations; trace/metric/log OTLP exporters. Loaded via `--import`. |
| `apps/api/src/server.ts` | Keep; the `--import telemetry.ts` precedes it (start command change). Replace `console.log` with pino. |
| `apps/api/src/app.ts` | `app.use('*', honoOtelMiddleware)`; replace `onError`'s `console.error` with pino. |
| `apps/api/src/routes/execute.ts` | Start `execute` span; `propagation.inject` active ctx into a carrier; set `spec.traceparent`/`spec.tracestate`. Wrap body in `AsyncLocalStorage` run with `{ jobId }`. |
| `apps/api/src/redis.ts`, `admission.ts`, `ratelimit.ts`, `routes/control.ts`, `routes/jobs.ts` | `console.*` → pino; admission/ratelimit 429 → counter increments. |
| `apps/api/src/logger.ts` (new) | pino instance + `AsyncLocalStorage<{jobId}>` mixin so every log carries `job_id`. |
| `apps/api/src/metrics.ts` (new) | API-side meter + admission/ratelimit rejection counters. |
| `apps/worker/main.go` | OTel SDK init at startup (gated on endpoint); set global TracerProvider/MeterProvider/LoggerProvider + propagator; `defer shutdown`. Install custom slog JSON handler. |
| `internal/otelinit/otelinit.go` (new) | Provider construction via `autoexport`; `IsNone*` → skip provider; returns shutdown func. |
| `internal/logging/handler.go` (new) | Custom `slog.Handler` wrapping `slog.NewJSONHandler` that injects `trace_id`/`span_id`/`job_id` from ctx (see Pitfall 4). |
| `internal/worker/worker.go` | Extract `traceparent` from `spec`; start linked root span; create phase child spans; emit terminal-state + time-in-queue metrics; use `slog.*Context(ctx,...)`. |
| `internal/runner/docker.go` | `sandbox.create` span + create/kill latency histograms. |
| `internal/session/{interactive,pump}.go` | `compile`/`run`/`handshake.wait` spans; output byte/seq counters (NOT per-chunk spans). |
| `internal/jobstore/{queue,capacity}.go` | Async-gauge callback source for queue depth (`LLEN`) + slots; time-in-queue at claim. |
| `internal/reaper/reaper.go` | reaper-orphans counter. |
| `internal/publisher/publisher.go` | soketi publish latency histogram + error counter. |
| `internal/config/config.go` | (Optional) read OTel endpoint presence for the gate; OTel SDK reads `OTEL_*` directly though. |
| `packages/contract/schema/wire.schema.json` | Add optional `traceparent`/`tracestate` strings to `JobSpec`. |
| `packages/code-runner-sdk-node/src/client.ts` | Inject `traceparent` request header when an active OTel context exists. |
| `docker-compose.yml`, `.env.example` | Add `otel-collector` + `jaeger` under `observability` profile; document `OTEL_*`. |

### Pattern 1: No-op-when-unset gate (OBS-01) — JS

**What:** NodeSDK and `autoexport` are NOT no-ops by default (they auto-create OTLP exporters). Gate SDK start on `OTEL_EXPORTER_OTLP_ENDPOINT`.
**When to use:** Both services, at startup.
**Example (apps/api/src/telemetry.ts):**
```typescript
// Source: github.com/open-telemetry/opentelemetry-js/.../opentelemetry-sdk-node/README.md
// CRITICAL: loaded via `node --import ./src/telemetry.ts` BEFORE any instrumented module.
import { NodeSDK } from "@opentelemetry/sdk-node";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-proto";
import { OTLPMetricExporter } from "@opentelemetry/exporter-metrics-otlp-proto";
import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-proto";
import { PeriodicExportingMetricReader } from "@opentelemetry/sdk-node/metrics"; // re-export
import { BatchLogRecordProcessor } from "@opentelemetry/sdk-node/logs";          // re-export
import { IORedisInstrumentation } from "@opentelemetry/instrumentation-ioredis";
import { PinoInstrumentation } from "@opentelemetry/instrumentation-pino";
import { HttpInstrumentation } from "@opentelemetry/instrumentation-http";

const endpoint = process.env.OTEL_EXPORTER_OTLP_ENDPOINT;
if (endpoint) {
  const sdk = new NodeSDK({
    // service.name/version come from OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES
    traceExporter: new OTLPTraceExporter(),
    metricReader: new PeriodicExportingMetricReader({ exporter: new OTLPMetricExporter() }),
    logRecordProcessors: [new BatchLogRecordProcessor(new OTLPLogExporter())],
    instrumentations: [
      new HttpInstrumentation(),
      new IORedisInstrumentation(),
      new PinoInstrumentation(), // correlation (stdout) + logSending (OTLP) both on by default
    ],
  });
  sdk.start();
  process.on("SIGTERM", () => void sdk.shutdown());
}
// When endpoint is unset: SDK never starts → API behaves exactly as today (true no-op).
```
> Belt-and-suspenders: also document `OTEL_SDK_DISABLED=true` and `OTEL_TRACES_EXPORTER=none` as the spec-standard escape hatches.

### Pattern 2: No-op-when-unset gate (OBS-01) — Go via autoexport

**What:** `autoexport` defaults each `OTEL_*_EXPORTER` to `otlp`; use the `IsNone*` helpers and an endpoint gate to install nothing when unset.
**Example (internal/otelinit/otelinit.go):**
```go
// Source: pkg.go.dev/go.opentelemetry.io/contrib/exporters/autoexport
func Init(ctx context.Context) (shutdown func(context.Context) error, err error) {
    if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" &&
       os.Getenv("OTEL_TRACES_EXPORTER") == "" {
        // No endpoint configured → install nothing. slog stays plain stdout.
        return func(context.Context) error { return nil }, nil
    }
    res, _ := resource.New(ctx, resource.WithFromEnv()) // OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES
    otel.SetTextMapPropagator(propagation.TraceContext{}) // W3C — REQUIRED for cross-lang

    spanExp, _ := autoexport.NewSpanExporter(ctx)
    tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spanExp), sdktrace.WithResource(res))
    otel.SetTracerProvider(tp) // sampler comes from OTEL_TRACES_SAMPLER / _ARG

    reader, _ := autoexport.NewMetricReader(ctx)
    mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
    otel.SetMeterProvider(mp)

    logExp, _ := autoexport.NewLogExporter(ctx)
    lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)), sdklog.WithResource(res))
    global.SetLoggerProvider(lp) // go.opentelemetry.io/otel/log/global

    return func(c context.Context) error {
        return errors.Join(tp.Shutdown(c), mp.Shutdown(c), lp.Shutdown(c))
    }, nil
}
```

### Pattern 3: Cross-language traceparent (inject TS → extract Go)

**Inject (apps/api/src/routes/execute.ts):**
```typescript
// Source: opentelemetry.io/docs/languages/js (W3CTraceContextPropagator is the default)
import { context, propagation, trace } from "@opentelemetry/api";

const tracer = trace.getTracer("code-runner-api");
await tracer.startActiveSpan("execute", async (span) => {
  const carrier: Record<string, string> = {};
  propagation.inject(context.active(), carrier); // writes "traceparent" (+ "tracestate")
  spec.traceparent = carrier.traceparent;
  if (carrier.tracestate) spec.tracestate = carrier.tracestate;
  // ... build spec, pipeline.set/lpush ...
  span.end();
});
```
**Extract + link (internal/worker/worker.go, on claim):**
```go
// Source: pkg.go.dev/go.opentelemetry.io/otel/propagation + otel/trace
carrier := propagation.MapCarrier{}
if spec.Traceparent != nil { carrier["traceparent"] = *spec.Traceparent }
if spec.Tracestate != nil  { carrier["tracestate"]  = *spec.Tracestate }
parentCtx := propagation.TraceContext{}.Extract(context.Background(), carrier)
linkedSC := trace.SpanContextFromContext(parentCtx) // same trace_id as the API span

tr := otel.Tracer("code-runner-worker")
ctx, root := tr.Start(ctx, "claim",
    trace.WithLinks(trace.Link{SpanContext: linkedSC}))
// child spans started from ctx: sandbox.create, handshake.wait, compile, run, publish.result
```
The W3C `traceparent` is `00-<32 hex trace_id>-<16 hex span_id>-<flags>`; both SDKs decode the same 16-byte trace_id, so the assertion `apiSpan.traceId === workerSpan.traceId` holds by construction (OBS-02 acceptance).

### Pattern 4: Worker stdout trace correlation (custom slog handler)

**What:** The official `otelslog` bridge feeds OTLP only; for stdout JSON correlation (D-03) wrap `slog.JSONHandler`.
**Example (internal/logging/handler.go):**
```go
// Source: pkg.go.dev/log/slog + otel/trace SpanContextFromContext
type ctxHandler struct{ slog.Handler }
func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
    if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
        r.AddAttrs(slog.String("trace_id", sc.TraceID().String()),
                   slog.String("span_id", sc.SpanID().String()))
    }
    if jid, ok := ctx.Value(jobIDKey).(string); ok {
        r.AddAttrs(slog.String("job_id", jid))
    }
    return h.Handler.Handle(ctx, r)
}
// main.go: base := slog.NewJSONHandler(os.Stdout, nil)
//          slog.SetDefault(slog.New(ctxHandler{base}))
```
**Requires** swapping `slog.Info(...)`/`log.Info(...)` to the `*Context` variants (`slog.InfoContext(ctx, ...)`) on hot paths so the ctx carries the span. The existing `log := slog.With("jobID", jobID)` pattern in `worker.go` stays, but logging calls must pass `ctx`.

### Pattern 5: Domain metrics (OBS-06) — Go instruments

```go
// Source: pkg.go.dev/go.opentelemetry.io/otel/metric
m := otel.Meter("code-runner-worker")
// async gauges (read on collect — zero hot-path cost) — D-05
queueDepth, _ := m.Int64ObservableGauge("code_runner.queue.depth", metric.WithUnit("{job}"))
slotsUsed,  _ := m.Int64ObservableGauge("code_runner.slots.used",  metric.WithUnit("{slot}"))
m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
    n, err := store.QueueDepth(ctx) // LLEN jobs:queue — see Pitfall 5
    if err == nil { o.ObserveInt64(queueDepth, n) }
    o.ObserveInt64(slotsUsed, int64(maxSandboxes-len(slots)))
    return nil
}, queueDepth, slotsUsed)
// histograms (semconv: durations in seconds) — D-06
createDur, _ := m.Float64Histogram("code_runner.sandbox.create.duration", metric.WithUnit("s"))
// counters — D-05/D-07
terminal,  _ := m.Int64Counter("code_runner.jobs.terminal", metric.WithUnit("{job}"))
terminal.Add(ctx, 1, metric.WithAttributes(attribute.String("terminal_state", "idle_timed_out")))
```

### Anti-Patterns to Avoid
- **Importing `ioredis` before the OTel hook is registered** → its spans never appear (ESM monkey-patch missed). Use `--import` bootstrap (Pitfall 1).
- **Per-chunk spans** for stdout/stderr — explicitly forbidden (OBS-03); use byte/seq counters.
- **High-cardinality attributes** (`job_id` on metrics, raw error strings on counters) — keep metric attributes low-cardinality (`terminal_state`, `language`). `job_id` belongs on spans/logs, not metrics.
- **Calling `autoexport` without the endpoint gate** — would silently try to push to `localhost:4318` and break OBS-01.
- **Opening an admin/metrics HTTP port** — OBS-05 is dropped; the worker stays HTTP-server-free.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| W3C traceparent parse/format | Custom hex parser | `propagation.TraceContext` (Go) / `propagation.inject` (JS) | Spec edge cases (flags, version, tracestate) |
| Env→exporter selection (Go) | Manual `OTEL_*` switch | `contrib/exporters/autoexport` + `IsNone*` | Spec-correct protocol/endpoint/none handling |
| NodeSDK exporter/processor wiring | Manual provider plumbing | `@opentelemetry/sdk-node` `NodeSDK` | One bootstrap, correct defaults, env-driven |
| Trace-id in pino logs | Custom serializer | `@opentelemetry/instrumentation-pino` | Correlation + OTLP send in one dep |
| Hono request span | Manual middleware | `@hono/otel` | Maintained, lifecycle-correct |
| slog→OTLP logs | Custom exporter | `contrib/bridges/otelslog` | Official bridge |

**Key insight:** The only thing you legitimately hand-roll is the **stdout** slog wrapper (Pattern 4), because the official bridge intentionally targets OTLP, not stdout — and D-03 requires stdout-always.

## Runtime State Inventory

Not a rename/refactor/migration phase — telemetry is additive. **No runtime-state migration required.** The only persisted-shape change is the wire contract gaining two **optional** fields (`traceparent`/`tracestate`); absent on old messages → backward-compatible (Go unmarshals to nil pointers, zod treats as optional). No stored data, live-service config, OS-registered state, secrets, or build artifacts carry observability identifiers today. **Verified by:** schema is the single source of truth and the fields are new+optional.

## Common Pitfalls

### Pitfall 1: ESM auto-instrumentation load order (API)
**What goes wrong:** ioredis spans never appear; only `@hono/otel` (middleware) works.
**Why:** `instrumentation-ioredis` monkey-patches `ioredis` at module load. The API is `"type":"module"` (`node --experimental-strip-types`/`tsx`), so the OTel ESM hook must be registered before `ioredis` is imported.
**How to avoid:** Put the SDK in a separate `telemetry.ts` and launch with `node --import ./src/telemetry.ts src/server.ts` (Node ≥20). The `--import` runs the bootstrap and registers the loader hook before `server.ts`→`redis.ts`→`ioredis` loads. Update the `dev`/`start` scripts and the API `Dockerfile` CMD.
**Warning signs:** HTTP/Hono spans present, Redis spans missing.

### Pitfall 2: SDK is not a no-op by default (both languages)
**What goes wrong:** With `OTEL_*` "unset" the app still tries to export to `localhost:4318`, adding startup cost / connection errors — violating OBS-01.
**Why:** NodeSDK auto-creates a default OTLP trace exporter; Go `autoexport` defaults `OTEL_*_EXPORTER=otlp`.
**How to avoid:** Gate SDK construction/start on `OTEL_EXPORTER_OTLP_ENDPOINT` presence (Patterns 1 & 2). Document `OTEL_SDK_DISABLED=true` / `OTEL_TRACES_EXPORTER=none` as standard alternatives.
**Warning signs:** "connection refused :4318" logs when telemetry is supposedly off; nonzero startup latency delta.

### Pitfall 3: Default propagator may not be W3C in Go
**What goes wrong:** Worker extracts an empty SpanContext; trace_id mismatch.
**Why:** Go's global propagator defaults to a no-op unless set; you must `otel.SetTextMapPropagator(propagation.TraceContext{})`.
**How to avoid:** Set it in `otelinit.Init` (Pattern 2). JS default already includes W3C trace context.
**Warning signs:** `linkedSC.IsValid()` is false even though `spec.traceparent` is populated.

### Pitfall 4: otelslog does not write to stdout
**What goes wrong:** Plan assumes the bridge satisfies OBS-07's stdout requirement; stdout JSON has no trace_id.
**Why:** The bridge converts slog records to OTel LogRecords for the OTLP pipeline only [CITED: pkg.go.dev/.../otelslog].
**How to avoid:** Custom JSON handler (Pattern 4) for stdout; otelslog is the *additional* OTLP path. Either fan-out to both handlers or run the JSON handler always + otelslog when configured.
**Warning signs:** OTLP logs correlate but `docker logs worker` JSON lacks `trace_id`.

### Pitfall 5: Reading Redis inside an async-gauge callback
**What goes wrong:** The metric collection cycle blocks or errors if `LLEN` hangs.
**Why:** The observable-gauge callback runs on the export interval; a blocking/slow Redis call stalls collection.
**How to avoid:** Pass a short-timeout `context` (the callback receives one) to a dedicated `store.QueueDepth(ctx)` (`LLEN jobs:queue`); on error, skip the observation (don't observe stale/zero). Slots-used can be read from the in-memory semaphore (`cap(slots)-len(slots)`) with no Redis call.
**Warning signs:** Export interval jitter; missing data points under Redis latency.

### Pitfall 6: @hono/otel export name is version-sensitive
**What goes wrong:** `import { otel } from "@hono/otel"` fails — current README (v1.1.x) uses `httpInstrumentationMiddleware`.
**Why:** The middleware export was renamed across versions (older docs show `otel()`).
**How to avoid:** At plan/impl time confirm the export for the **installed** 1.1.2 against `node -e "console.log(Object.keys(require('@hono/otel')))"` or the package's `dist` types. [ASSUMED → verify] current export is `httpInstrumentationMiddleware`.
**Warning signs:** Import/type error on the middleware.

### Pitfall 7: pino logSending requires the Logs SDK configured
**What goes wrong:** `disableLogSending` not needed, but logs won't reach OTLP if no LoggerProvider/log processor is set.
**Why:** `instrumentation-pino` sends to whatever Logs SDK is registered; correlation (stdout) works regardless, but OTLP send needs `logRecordProcessors` in NodeSDK.
**How to avoid:** Configure `logRecordProcessors:[new BatchLogRecordProcessor(new OTLPLogExporter())]` (Pattern 1). When endpoint unset and SDK not started, pino still emits plain stdout JSON (D-03 stdout-always holds) — just without trace fields outside a span.
**Warning signs:** Stdout has trace_id but collector receives no logs.

### Pitfall 8: contract drift / additionalProperties on JobSpec
**What goes wrong:** `make contract-check` fails after schema edit, or the worker rejects the new field.
**Why:** `JobSpec` has `additionalProperties:false`; the generator inlines `$defs`. New fields must be declared (optional, not in `required`).
**How to avoid:** Add `"traceparent": {"type":"string"}` and `"tracestate":{"type":"string"}` to `JobSpec.properties`, leave them out of `required`, run `pnpm contract`, commit `gen/**`, verify `make contract-check` green. Confirm zod emits `.optional()` and Go emits `*string`.
**Warning signs:** `git diff --exit-code -- packages/contract/gen` nonzero; Go unmarshal error on old specs.

## Code Examples

### SDK Node propagation (packages/code-runner-sdk-node/src/client.ts)
```typescript
// Source: opentelemetry.io/docs/languages/js — inject only when a context is active.
// Make @opentelemetry/api an OPTIONAL peer so non-OTel callers pull zero OTel weight.
private injectTrace(headers: Record<string, string>): void {
  try {
    // dynamic/optional: if the caller hasn't installed @opentelemetry/api, skip silently
    const api = globalThis.__OTEL_API__ ?? require("@opentelemetry/api");
    api.propagation.inject(api.context.active(), headers);
  } catch { /* OTel absent or no active span → unchanged behavior */ }
}
// in request(): if (path === "/v1/execute") this.injectTrace(headers);
```
> With no active span, `propagation.inject` writes nothing (or a non-sampled traceparent the API will start fresh from). With OTel absent, the catch makes it a no-op — satisfying OBS-02-ext acceptance.

### Jaeger + Collector compose profile (docker-compose.yml)
```yaml
# Source: jaegertracing.io + opentelemetry.io collector docs. Inert on default `up`.
  otel-collector:
    image: otel/opentelemetry-collector-contrib:latest   # pin a tag at plan time
    command: ["--config=/etc/otelcol/config.yaml"]
    volumes: ["./observability/otel-collector.yaml:/etc/otelcol/config.yaml:ro"]
    networks: [code-runner]
    profiles: [observability]
  jaeger:
    image: jaegertracing/all-in-one:1.62   # pin; OTLP enabled by default in recent tags
    environment: { COLLECTOR_OTLP_ENABLED: "true" }
    ports: ["16686:16686"]   # UI
    networks: [code-runner]
    profiles: [observability]
```
```yaml
# observability/otel-collector.yaml
receivers:
  otlp: { protocols: { http: { endpoint: 0.0.0.0:4318 }, grpc: { endpoint: 0.0.0.0:4317 } } }
processors:
  batch: {}
  # tail-sampling note (D-10): keep all error/anomalous-terminal traces in prod.
  # tail_sampling: { policies: [{name: errors, type: status_code, status_code:{status_codes:[ERROR]}}] }
exporters:
  otlp/jaeger: { endpoint: jaeger:4317, tls: { insecure: true } }   # traces → Jaeger
  debug: { verbosity: detailed }                                     # metrics/logs visibility
service:
  pipelines:
    traces:  { receivers: [otlp], processors: [batch], exporters: [otlp/jaeger, debug] }
    metrics: { receivers: [otlp], processors: [batch], exporters: [debug] }
    logs:    { receivers: [otlp], processors: [batch], exporters: [debug] }
```
> Services point `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318`. Jaeger ingests traces only; metrics/logs land in the collector `debug` exporter for E2E proof (OBS-04). The repo already uses compose `profiles:` for `stub`, so `--profile observability` is a proven mechanism (D-09).

### .env.example additions (D-04, D-10)
```bash
# ── OpenTelemetry (all UNSET by default → true no-op; nothing is exported) ──
# Set the endpoint to enable OTLP push for traces+metrics+logs.
# OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
# OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
# OTEL_SERVICE_NAME=code-runner-api            # worker: code-runner-worker
# OTEL_RESOURCE_ATTRIBUTES=service.namespace=code-runner
# Sampling: capture every trace in the example; lower the ratio in prod.
# OTEL_TRACES_SAMPLER=parentbased_traceidratio
# OTEL_TRACES_SAMPLER_ARG=1.0
# Hard kill-switches (standard): OTEL_SDK_DISABLED=true | OTEL_TRACES_EXPORTER=none
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| OTel JS unstable pkgs at `0.5x` | `2.x` stable + `0.2xx` unstable, released together | JS SDK 2.0 (2025) [CITED: opentelemetry.io/blog/2025/otel-js-sdk-2-0] | Use `sdk-node@0.218.0` with `api@1.9.1`; don't mix old `0.5x` exporters |
| Manual Go exporter env switch | `contrib/exporters/autoexport` + `IsNone*` | matured pre-2026 | Less boilerplate; spec-correct no-op detection |
| `console.log` (API) | pino + instrumentation-pino | this phase | Structured + correlated logs |
| Jaeger via deprecated Jaeger exporter | Native OTLP into Jaeger all-in-one | Jaeger 1.35+ / v2 | No jaeger-specific exporter; OTLP everywhere |

**Deprecated/outdated:**
- `@opentelemetry/exporter-jaeger` / Go `jaeger` exporter — removed; use OTLP → Jaeger.
- `auto-instrumentations-node` kitchen-sink — explicitly rejected (D-01).

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `@hono/otel` v1.1.2 export is `httpInstrumentationMiddleware` | Pitfall 6 / Pattern setup | Import error — trivially fixed by checking installed export name |
| A2 | `PeriodicExportingMetricReader`/`BatchLogRecordProcessor` are re-exported from `@opentelemetry/sdk-node` `.metrics`/`.logs` subpaths | Pattern 1 | May need direct `@opentelemetry/sdk-metrics`/`sdk-logs` import; verify subpath at impl |
| A3 | OTLP/HTTP (4318) chosen over gRPC for both langs | Standard Stack note | Pure preference; switch exporter pkg + port if gRPC preferred |
| A4 | Jaeger all-in-one tag `1.62` ingests OTLP by default | compose example | Pin a verified tag at plan time; `COLLECTOR_OTLP_ENABLED=true` set explicitly anyway |
| A5 | Making `@opentelemetry/api` an optional peer in the SDK keeps zero-OTel callers weightless | SDK propagation | If awkward, ship `api` as a normal (light) dep — it's API-only, no SDK weight |

**Verified (not assumed):** all package versions (npm + Go proxy), NodeSDK auto-exporter-default behavior, autoexport `OTEL_*_EXPORTER=otlp` default + `IsNone*`, otelslog forwarding-only semantics, instrumentation-pino dual correlation+logSending, ESM hook load-order requirement, W3C `traceparent` byte-format equivalence, compose `profiles:` already used by `stub`, OTel Go core already at v1.44.0 in go.mod.

## Open Questions (RESOLVED)

1. **gRPC vs HTTP transport** — both work; recommend HTTP/4318 for a lighter Go binary. Planner/founder may prefer gRPC. *Recommendation:* default HTTP, document the gRPC swap. **RESOLVED:** HTTP/4318 (`http/protobuf`) OTLP transport chosen for both API and worker; gRPC swap documented as the alternative.
2. **stdout-always vs OTLP logs fan-out (worker)** — run the custom JSON handler unconditionally and add otelslog only when configured, OR fan-out via a multi-handler. *Recommendation:* JSON handler always (D-03); otelslog gated behind the endpoint check inside `otelinit`. **RESOLVED:** stdout JSON handler always-on (D-03); the otelslog OTLP bridge is added only when the OTLP endpoint is configured, gated inside `otelinit`.
3. **Jaeger v1 all-in-one vs Jaeger v2 (`jaegertracing/jaeger`)** — v2 is collector-based and also OTLP-native. *Recommendation:* v1 all-in-one `1.62` for lowest friction + built-in UI (D-08); revisit if v1 is EOL at plan time. **RESOLVED:** Jaeger v1 all-in-one `1.62` chosen for the example BYO stack per D-08 (lowest friction, built-in UI).

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | worker OTel build | ✓ | 1.26.x | — |
| pnpm / node | API + SDK OTel build | ✓ | pnpm 10 / node 22-24 | — |
| OTel Go core modules | worker | ✓ (already in go.mod) | v1.44.0 | — |
| Docker daemon | compose `observability` profile E2E | ✓ | Docker Desktop | — |
| `otel/opentelemetry-collector-contrib` image | E2E proof | pulled on demand | pin tag | core collector image (no tail-sampling) |
| `jaegertracing/all-in-one` image | trace UI | pulled on demand | pin 1.62 | any OTLP backend |

No missing blocking dependencies. The collector/Jaeger images are pulled only under the `observability` profile.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework (JS) | **vitest 3.2** (`apps/api/vitest.config.ts`); SDK uses `node --test` |
| Framework (Go) | stdlib `testing` + `testify v1.11` |
| Config file | `apps/api/vitest.config.ts`; Go has none (convention) |
| Quick run (JS) | `pnpm --filter @code-runner/api test` |
| Quick run (Go) | `go test ./internal/... ./apps/worker/...` |
| Full suite | `make test` (Go + JS); `make contract-check` for the schema gate |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| OBS-01 | SDK no-op when `OTEL_*` unset (no exporter, no error) | unit | `pnpm --filter @code-runner/api test` (assert SDK not started when endpoint unset); `go test ./internal/otelinit -run NoEndpoint` | ❌ Wave 0 |
| OBS-02 | `traceparent` injected into JobSpec at /v1/execute | unit | `pnpm --filter @code-runner/api test test/execute.test.ts` (assert `spec.traceparent` set under active span) | ⚠ extend `execute.test.ts` |
| OBS-02 | Cross-lang shared trace_id (inject TS / extract Go round-trip) | unit | `go test ./internal/worker -run TraceparentExtract` (extract a known TS-format traceparent, assert SpanContext.TraceID matches) | ❌ Wave 0 |
| OBS-02-ext | SDK Node injects header only with active context | unit | `node --test packages/code-runner-sdk-node/test/*.ts` | ⚠ add test |
| OBS-03 | Phase spans emitted; 0 per-chunk spans | unit | `go test ./internal/worker -run PhaseSpans` using an in-memory `tracetest.SpanRecorder` (assert span names + N chunks → 0 chunk spans + nonzero output-bytes metric) | ❌ Wave 0 |
| OBS-04 | OTLP push reaches collector | integration | E2E under `--profile observability`; assert collector debug output (manual/CI smoke) | ❌ Wave 0 (doc + script) |
| OBS-06 | Domain metrics increment correctly | unit | `go test ./internal/worker -run Metrics` with `metric/sdk/metric/metricdata` + `manualreader` (assert `code_runner.jobs.terminal{terminal_state=idle_timed_out}` increments; queue-depth gauge observes LLEN); API admission-rejection counter via vitest | ❌ Wave 0 |
| OBS-07 | stdout log line is valid JSON with trace_id/span_id/job_id | unit | Go: capture custom handler output to a buffer, assert JSON keys; JS: pino to a stream, assert keys present within a span | ❌ Wave 0 |
| OBS-08 | Sampler ratio changes volume; `.env.example` documents vars | unit/doc | assert `.env.example` lists each `OTEL_*`; sampler honored via `OTEL_TRACES_SAMPLER_ARG` (probabilistic — assert config wiring, not statistical volume) | ❌ Wave 0 |
| (contract) | schema change keeps generators green | gate | `make contract-check` | ✓ exists |

### Sampling Rate
- **Per task commit:** `go test ./internal/<pkg>/...` (the touched package) + `pnpm --filter @code-runner/api test`.
- **Per wave merge:** `make test` + `make contract-check`.
- **Phase gate:** full suite green + an E2E run under `--profile observability` showing one connected trace in the Jaeger UI before `/gsd:verify-work`.

### Test Tooling Notes (in-memory span/metric assertions — the OBS-02/03/06 backbone)
- **Go traces:** `go.opentelemetry.io/otel/sdk/trace/tracetest.NewSpanRecorder()` registered as a `SpanProcessor` on a test TracerProvider → assert span names, links (`spans[i].Links()`), and shared TraceID without a collector.
- **Go metrics:** `sdk/metric.NewManualReader()` + `reader.Collect(ctx, &rm)` → assert instrument names/values/attributes deterministically.
- **JS traces:** `InMemorySpanExporter` + `SimpleSpanProcessor` (from `@opentelemetry/sdk-trace-base`) → assert the `execute` span and the injected traceparent.
- **JS metrics:** `InMemoryMetricExporter` or a manual `PeriodicExportingMetricReader` flush.

### Wave 0 Gaps
- [ ] `internal/otelinit/otelinit_test.go` — no-op-when-unset + provider construction (OBS-01)
- [ ] `internal/worker/trace_test.go` — traceparent extract + link + phase span recorder (OBS-02, OBS-03)
- [ ] `internal/worker/metrics_test.go` (or per-package) — manualreader metric assertions (OBS-06)
- [ ] `internal/logging/handler_test.go` — stdout JSON correlation (OBS-07)
- [ ] `apps/api/test/telemetry.test.ts` — gate + InMemory span/metric assertions (OBS-01, OBS-02, OBS-06, OBS-07)
- [ ] `packages/code-runner-sdk-node/test/trace.test.ts` — header injection only with active context (OBS-02-ext)
- [ ] extend `apps/api/test/execute.test.ts` — assert `spec.traceparent` written
- [ ] `scripts/observability-e2e.sh` (or doc) — `--profile observability` connected-trace proof (OBS-04, OBS-08)
- No framework install needed (vitest + Go testing already present).

## Security Domain

`security_enforcement` is not set to `false` in config, so this section is included. This phase is **telemetry-additive** and explicitly does not change the auth model or trust boundary (SPEC out-of-scope). Still, observability introduces specific risks:

### Applicable ASVS Categories
| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | No new auth surface (OBS-05 admin port dropped) |
| V3 Session Management | no | — |
| V4 Access Control | no | Telemetry egress is outbound-only OTLP push |
| V5 Input Validation | yes | `traceparent`/`tracestate` arrive over the wire — validate via the generated zod/Go contract (optional strings); a malformed value simply fails W3C extract → fresh trace, never a crash |
| V6 Cryptography | no | No new crypto |
| V7 Error/Logging | yes | **Do not log secrets**: keep `SOKETI_APP_SECRET`, `EXECUTOR_API_TOKEN`, and user code/stdin OUT of logs and span attributes. pino/slog must redact; never put file contents or stdin chunks on spans |
| V8 Data Protection | yes | Span/log attributes must avoid PII / user source code; cap attribute cardinality and size |

### Known Threat Patterns for OTel-in-untrusted-code-runner
| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Malicious/oversized `traceparent` in JobSpec | Tampering / DoS | W3C extract is bounded + fails closed to a fresh trace; field is an optional string validated by the contract; no eval |
| Secret leakage into logs/spans | Information Disclosure | Redact secrets in pino/slog; never attach stdin/source/env to spans or log bodies |
| High-cardinality attribute explosion (e.g. job_id on metrics) | DoS (backend) | Keep metric attributes low-cardinality (`terminal_state`, `language`); job_id only on spans/logs |
| Telemetry endpoint as exfil channel | Information Disclosure | OTLP push only to an operator-configured endpoint; off by default (OBS-01); document that the endpoint is trusted infra |
| Trust-boundary violation via observability | Elevation/Tampering | Worker still talks ONLY to Redis + soketi + (new) the OTLP collector; it makes NO HTTP call to the API — preserved (the grep gate in worker tests still holds) |

## Sources

### Primary (HIGH confidence)
- npm registry (`npm view <pkg> version`) — exact current versions: `@opentelemetry/api` 1.9.1, `sdk-node`/`api-logs`/`exporter-*-otlp-proto` 0.218.0, `instrumentation-ioredis` 0.66.0, `instrumentation-pino` 0.64.0, `@hono/otel` 1.1.2, `pino` 10.3.1, `sdk-metrics`/`sdk-trace-base` 2.7.1 — verified 2026-06-03.
- Go module proxy (`go list -m -versions`) — `go.opentelemetry.io/otel` v1.44.0 (latest stable, already in go.mod), `otel/log` v0.20.0, `contrib/bridges/otelslog` v0.19.0, `contrib/exporters/autoexport` v0.69.0, otlpmetric/otlplog exporters — verified 2026-06-03.
- pkg.go.dev/go.opentelemetry.io/contrib/exporters/autoexport — `NewSpanExporter`/`NewMetricReader`/`NewLogExporter`/`IsNone*` signatures, `OTEL_*_EXPORTER` default `otlp` + `none`.
- pkg.go.dev/go.opentelemetry.io/contrib/bridges/otelslog — `NewHandler`/`NewLogger`/`WithLoggerProvider`; forwards to LoggerProvider (not stdout).
- github.com/open-telemetry/opentelemetry-js opentelemetry-sdk-node/README.md — NodeSDK auto-creates default OTLP exporter; `OTEL_TRACES_EXPORTER` default otlp; `OTEL_SDK_DISABLED`.
- github.com/honojs/middleware/packages/otel — `httpInstrumentationMiddleware`, requires NodeSDK init separately.
- github.com/open-telemetry/opentelemetry-js-contrib instrumentation-pino README — correlation (trace_id/span_id/trace_flags into stdout) + logSending both default-on; `logKeys`, `disableLogSending`.
- pkg.go.dev/go.opentelemetry.io/otel/propagation + otel/trace — `TraceContext`, `MapCarrier`, `SpanContextFromContext`, `trace.Link`/`WithLinks`.
- Local codebase (read): `wire.schema.json`, `routes/execute.ts`, `internal/worker/worker.go`, `server.ts`, `main.go`, `jobstore/{queue,capacity}.go`, `publisher.go`, `docker-compose.yml` (confirms `profiles:` for `stub`), `.env.example`, `go.mod` (OTel v1.44.0 already indirect), `apps/api/package.json`.

### Secondary (MEDIUM confidence)
- opentelemetry.io blog "Announcing the OpenTelemetry JavaScript SDK 2.0" — 2.x/0.2xx version alignment.
- opentelemetry.io/docs/languages/js ESM support + Node getting-started — `--import` + loader-hook requirement.
- jaegertracing.io configuration + community compose guides — all-in-one OTLP ports (4317/4318), `COLLECTOR_OTLP_ENABLED`, UI 16686.
- slopcheck v0.6.1 `install` run — all 10 JS packages `[OK]`.

### Tertiary (LOW confidence)
- `@hono/otel` export name `httpInstrumentationMiddleware` (A1) — confirm against installed v1.1.2.
- `PeriodicExportingMetricReader`/`BatchLogRecordProcessor` sdk-node subpath re-exports (A2) — confirm import path at impl.

## Metadata

**Confidence breakdown:**
- Standard stack (versions/packages): HIGH — verified against npm + Go proxy on 2026-06-03; slopcheck clean.
- Architecture (propagation, links, no-op gate, ESM order): HIGH — verified against official docs + codebase shape.
- Pitfalls: HIGH — each rooted in a verified doc/registry fact; the two LOW items (A1/A2) are import-name details flagged for the planner.
- Metrics instrument mapping: HIGH — instrument types map cleanly onto existing signals (LLEN, semaphore, ResultEvent flags, enqueuedAtMs).

**Research date:** 2026-06-03
**Valid until:** ~2026-07-03 (30 days; OTel JS unstable packages move on a 4-6 week cadence — re-verify versions and the `@hono/otel` export name at plan time).

## RESEARCH COMPLETE
