# Phase 8: Distributed Observability - Pattern Map

**Mapped:** 2026-06-03
**Files analyzed:** 23 new/modified (11 TS, 9 Go, 1 schema, 2 infra/docs)
**Analogs found:** 19 / 23 (4 new files have no in-repo analog — green-field OTel plumbing; use RESEARCH.md patterns)

> Polyglot note: TS-new-files map ONLY to TS analogs (`apps/api/src/**`, `packages/code-runner-sdk-node/src/**`); Go-new-files map ONLY to Go analogs (`internal/**`, `apps/worker`). Never cross languages.
> Scope note: **OBS-05 (Prometheus `/metrics` pull / admin HTTP port) is OUT OF SCOPE** (dropped in CONTEXT D-04). No admin-server / Prometheus-exporter file is mapped. Worker stays HTTP-server-free.

## File Classification

| New/Modified File | Lang | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|------|-----------|----------------|---------------|
| `apps/api/src/telemetry.ts` **(new)** | TS | config/bootstrap | event-driven (startup) | `apps/api/src/config.ts` (env gate) + `apps/api/src/redis.ts` (lazy singleton) | role-match |
| `apps/api/src/logger.ts` **(new)** | TS | utility | transform | `apps/api/src/redis.ts` (lazy singleton accessor) | role-match |
| `apps/api/src/metrics.ts` **(new)** | TS | utility | event-driven (counters) | `apps/api/src/admission.ts` (domain-signal helpers) | partial |
| `apps/api/src/server.ts` (mod) | TS | bootstrap | request-response | self — `console.*` → pino, start-cmd note | self |
| `apps/api/src/app.ts` (mod) | TS | bootstrap/middleware | request-response | self — add `@hono/otel` middleware, `onError` → pino | self |
| `apps/api/src/routes/execute.ts` (mod) | TS | controller | request-response → CRUD (LPUSH) | self — `execute` span + `traceparent` inject before pipeline | self |
| `apps/api/src/routes/control.ts` (mod) | TS | controller | request-response (pub-sub publish) | self — `console.*` → pino; optional stdin/kill spans | self |
| `apps/api/src/routes/jobs.ts` (mod) | TS | controller | request-response | `apps/api/src/routes/control.ts` | exact |
| `apps/api/src/admission.ts` (mod) | TS | utility | event-driven | self — increment admission-429 counter | self |
| `apps/api/src/ratelimit.ts` (mod) | TS | middleware | request-response | self — increment ratelimit-429 counters | self |
| `apps/api/src/redis.ts` (mod) | TS | service | CRUD/pub-sub | self — `console.error` → pino | self |
| `packages/code-runner-sdk-node/src/client.ts` (mod) | TS | service (client) | request-response | self — inject `traceparent` header in `request()` | self |
| `internal/otelinit/otelinit.go` **(new)** | Go | config/bootstrap | event-driven (startup) | `internal/publisher/publisher.go` (`New(cfg)` + interface) + `internal/config/config.go` (env) | role-match |
| `internal/logging/handler.go` **(new)** | Go | utility | transform | (no analog — custom `slog.Handler`; see RESEARCH Pattern 4) | none |
| `apps/worker/main.go` (mod) | Go | bootstrap | event-driven | self — OTel init at boot, `defer shutdown`, install slog handler | self |
| `internal/worker/worker.go` (mod) | Go | service (run loop) | event-driven | self — extract traceparent on claim, phase spans, terminal counter | self |
| `internal/runner/docker.go` (mod) | Go | service (runner) | request-response (lifecycle) | self — `sandbox.create` span + create/kill latency histograms | self |
| `internal/session/interactive.go` (mod) | Go | service | streaming | self — `run`/`handshake.wait`/`compile` spans, output-byte counter (NOT per-chunk) | self |
| `internal/jobstore/queue.go` (mod) | Go | model/store | CRUD | self — time-in-queue histogram at Claim; add `QueueDepth(ctx)` (LLEN) | self |
| `internal/jobstore/capacity.go` (mod) | Go | model/store | CRUD | self — slots gauge source | self |
| `internal/reaper/reaper.go` (mod) | Go | service | batch (sweep) | self — reaper-orphans counter at `reapContainer` | self |
| `internal/publisher/publisher.go` (mod) | Go | service | pub-sub | self — soketi publish latency histogram + error counter on `Trigger` | self |
| `packages/contract/schema/wire.schema.json` (mod) | JSON | config (contract) | transform (codegen) | self — add optional `traceparent`/`tracestate` to JobSpec | self |
| `docker-compose.yml` (mod) | YAML | config (infra) | — | `stub` service block (`profiles:` usage) | exact |
| `.env.example` (mod) | env | config | — | self — append documented `OTEL_*` vars | self |

---

## Pattern Assignments

### `apps/api/src/telemetry.ts` (new — TS bootstrap)

**Analogs:** env-gate pattern from `apps/api/src/config.ts`; lazy-singleton + reconnect-tolerant init from `apps/api/src/redis.ts`.

**Env-gate pattern to copy** (`config.ts` lines 24-32 — `requireEnv`/optional read style). For OTel, the gate is **optional presence**, not required:
```typescript
// config.ts:24 pattern — adapt: read OTEL_EXPORTER_OTLP_ENDPOINT, do NOT throw when absent.
const endpoint = process.env["OTEL_EXPORTER_OTLP_ENDPOINT"];
if (!endpoint) { /* no-op: never construct/start the SDK (OBS-01) */ return; }
```

**Singleton-with-error-tolerance pattern to copy** (`redis.ts` lines 7-23): a module-level `let _x: T | null`, constructed once, with a SIGTERM/shutdown hook mirroring `disconnectRedis` (lines 26-31).

**Core SDK wiring:** use RESEARCH §"Pattern 1" verbatim (NodeSDK + OTLP proto exporters + HTTP/ioredis/pino instrumentations, gated on endpoint). NOT in any existing file — this is green-field.

**CRITICAL load-order constraint (RESEARCH Pitfall 1):** this file is loaded via `node --import ./src/telemetry.ts` BEFORE `server.ts` so the ioredis ESM hook registers before `redis.ts` imports `ioredis`. Update both start commands:
- `apps/api/package.json` lines 8-9: `dev` = `node --experimental-strip-types`, `start` = `tsx src/server.ts`.
- `apps/api/Dockerfile` line 38: `CMD ["node_modules/.bin/tsx", "src/server.ts"]`.

---

### `apps/api/src/logger.ts` (new — TS utility)

**Analog:** `apps/api/src/redis.ts` (lazy-singleton accessor pattern, lines 7-23).

**Pattern to copy** — module-level singleton getter, applied to a pino instance + `AsyncLocalStorage<{jobId}>` (D-03):
```typescript
// mirror redis.ts:9 getRedis() shape
let _logger: Logger | null = null;
export const jobContext = new AsyncLocalStorage<{ jobId: string }>();
export function getLogger(): Logger { /* construct pino once with a mixin reading jobContext */ }
```
The pino `mixin` reads `jobContext.getStore()?.jobId` so every log within a job carries `job_id` automatically. Trace fields are injected by `@opentelemetry/instrumentation-pino` (registered in `telemetry.ts`).

---

### `apps/api/src/metrics.ts` (new — TS utility)

**Analog:** `apps/api/src/admission.ts` — domain-signal helper module exporting small pure functions (`atCapacity`, `admissionError`, lines 29-49).

**Pattern to copy** — a focused module that owns the API-side meter + the two rejection counters, exporting named increment functions the routes call:
```typescript
// admission.ts:41 admissionError() is the shape to mirror: small named exports, no class.
import { metrics } from "@opentelemetry/api";
const meter = metrics.getMeter("code-runner-api");
export const admissionRejections = meter.createCounter("code_runner.admission.rejected", { unit: "{request}" });
export const ratelimitRejections = meter.createCounter("code_runner.ratelimit.rejected", { unit: "{request}" });
```
Low-cardinality attrs only (e.g. `reason`); never `job_id` on metrics (RESEARCH anti-pattern).

---

### `apps/api/src/routes/execute.ts` (modified — TS controller)

**Analog:** self. Inject the `execute` span + `traceparent` at the existing enqueue point.

**Existing injection site** (lines 101-137): `jobId` minted, `spec: JobSpec` built (101-119), pipeline `set`/`set`/`lpush` (133-137). Wrap span around spec-build, inject carrier into `spec.traceparent`/`spec.tracestate` BEFORE `pipeline.set(keys.jobSpec...)` at line 134.

**Pattern to copy** — RESEARCH §"Pattern 3 (Inject)":
```typescript
import { context, propagation, trace } from "@opentelemetry/api";
const tracer = trace.getTracer("code-runner-api");
await tracer.startActiveSpan("execute", async (span) => {
  const carrier: Record<string, string> = {};
  propagation.inject(context.active(), carrier);  // writes traceparent (+ tracestate)
  spec.traceparent = carrier.traceparent;
  if (carrier.tracestate) spec.tracestate = carrier.tracestate;
  // existing pipeline.set/set/lpush (execute.ts:133-137)
  span.end();
});
```
Also wrap the handler body in `jobContext.run({ jobId }, ...)` (from `logger.ts`) so logs carry `job_id`. Existing admission-429 branch (lines 96-99) calls the new `admissionRejections.add(1)`.

---

### `apps/api/src/app.ts` (modified — TS bootstrap/middleware)

**Analog:** self. Two changes:
1. Add the Hono OTel middleware after app construction (line 16 area, before route registration):
   ```typescript
   // @hono/otel — confirm export name for installed v1.1.2 (RESEARCH Pitfall 6 → likely httpInstrumentationMiddleware)
   app.use("*", httpInstrumentationMiddleware());
   ```
2. Replace `onError`'s `console.error` (line 37) with `getLogger().error(...)`.

---

### `apps/api/src/server.ts` (modified — TS bootstrap)

**Analog:** self. Replace `console.log` (lines 12, 22) and `console.error` (line 28) with the pino logger from `logger.ts`. The OTel SDK is NOT imported here — it is `--import`ed ahead of this file (see `telemetry.ts`).

---

### `apps/api/src/routes/control.ts`, `routes/jobs.ts`, `redis.ts` (modified — TS)

**Analog:** self / each other.
- `control.ts`: replace any `console.*`; optional stdin/kill spans are **discretionary** (D-11 — only if per-stdin/per-kill spans are actually emitted; output is metrics so likely skip). The PUBLISH points (lines 22, 50, 60, 67) are the only candidate span sites.
- `redis.ts` line 19-20: `console.error("[redis] connection error:"...)` → `getLogger().error(...)`.
- `ratelimit.ts`: the two 429 branches (lines 60-68 frame-rate, 108-118 byte-cap) call `ratelimitRejections.add(1, {reason})`.

---

### `packages/code-runner-sdk-node/src/client.ts` (modified — TS client)

**Analog:** self — the existing `request()` method builds headers at lines 112-122.

**Injection site** (lines 112-116): the `headers` object is assembled here. Inject the active trace context into `headers` only on `/v1/execute`.

**Pattern to copy** — RESEARCH §"SDK Node propagation" (optional-peer, catch-on-absent):
```typescript
// in request(), after building headers (client.ts:113):
if (path === "/v1/execute") {
  try {
    const api = await import("@opentelemetry/api");  // optional peer
    api.propagation.inject(api.context.active(), headers);
  } catch { /* OTel absent or no active span → unchanged behavior (OBS-02-ext) */ }
}
```
Make `@opentelemetry/api` an **optional peerDependency** so non-OTel callers pull zero OTel weight (RESEARCH A5).

---

### `internal/otelinit/otelinit.go` (new — Go bootstrap)

**Analogs:** `internal/publisher/publisher.go` — `New(cfg config.Config) (*Publisher, error)` constructor + interface-behind-impl shape (lines 71-89); `internal/config/config.go` env-model.

**Constructor pattern to copy** (`publisher.go:71` `New`): a package-level `Init(ctx) (shutdown func, err error)` constructor that reads config/env and returns a typed value + error, exactly like `publisher.New`.

**Env-gate + provider wiring:** RESEARCH §"Pattern 2" verbatim — `autoexport.NewSpanExporter/NewMetricReader/NewLogExporter`, `IsNone*` early-return, `otel.SetTextMapPropagator(propagation.TraceContext{})` (W3C — REQUIRED, RESEARCH Pitfall 3), `defer`-able combined shutdown via `errors.Join`. Green-field; no in-repo analog for the provider construction itself.

---

### `internal/logging/handler.go` (new — Go utility)

**Analog:** NONE in repo. Use RESEARCH §"Pattern 4" verbatim — a `ctxHandler struct{ slog.Handler }` wrapping `slog.NewJSONHandler`, pulling `trace_id`/`span_id` from `trace.SpanContextFromContext(ctx)` and `job_id` from a ctx key. The `otelslog` bridge is the **separate** OTLP path (it does NOT write stdout — Pitfall 4).

**Existing logging style to preserve** (`worker.go:231`): `log := slog.With("jobID", jobID)` stays; calls must switch to `slog.*Context(ctx, ...)` so the span rides the context.

---

### `apps/worker/main.go` (modified — Go bootstrap)

**Analog:** self. The `run(ctx)` boot sequence (lines 55-168) is the insertion point.

**Insertion site** (top of `run`, before line 56 `cfg := configFromEnv()` or right after): call `shutdown, err := otelinit.Init(ctx)`, `defer shutdown(ctx)`, then install the custom slog handler:
```go
// mirror the existing slog usage at main.go:50,73 — set the default handler at boot.
base := slog.NewJSONHandler(os.Stdout, nil)
slog.SetDefault(slog.New(logging.NewCtxHandler(base)))
```
All existing `slog.Info(...)` calls (lines 50, 73-81, 95, 116, 123, 159) keep working; hot-path ones gain a ctx variant where a span exists.

---

### `internal/worker/worker.go` (modified — Go run loop)

**Analog:** self. This is the trace backbone.

**Extract + link site** — `runJobFromSpec` start (line 229-231), right after `jobID := spec.JobId`. Use RESEARCH §"Pattern 3 (Extract+link)":
```go
carrier := propagation.MapCarrier{}
if spec.Traceparent != nil { carrier["traceparent"] = *spec.Traceparent }
if spec.Tracestate != nil  { carrier["tracestate"]  = *spec.Tracestate }
parentCtx := propagation.TraceContext{}.Extract(context.Background(), carrier)
linkedSC := trace.SpanContextFromContext(parentCtx)
ctx, root := otel.Tracer("code-runner-worker").Start(ctx, "claim",
    trace.WithLinks(trace.Link{SpanContext: linkedSC}))
defer root.End()
```

**Phase-span sites** (map to existing structure, NOT per-chunk):
| Span | Existing site in worker.go |
|------|----------------------------|
| `claim` | run loop / start of `runJobFromSpec` (line 229) — the root span |
| `sandbox.create` | wraps `w.runner.Create` (line 295) — better placed inside `docker.go`, see below |
| `handshake.wait` | the `parkLoop` (lines 363-388) |
| `compile` | the `spec.Compile != nil` block (lines 397-427) |
| `run` | wraps `session.RunInteractive` (line 517) |
| `publish.result` | the teardown `w.pub.Result` (lines 333-336) |

**Terminal-state counter** (D-07) at the terminal-state decision (lines 522-526) + every `teardown(...)` call: increment `code_runner.jobs.terminal` with a low-cardinality `terminal_state` attr derived from `wire.JobState` / `result.TimedOut`/`IdleTimedOut`. The warmup-timeout path (line 370-374) → `terminal_state=error` (or a `warmup` reclaim counter).

**Time-in-queue** (OBS-06): at claim, `now - spec.EnqueuedAtMs` → histogram (spec already carries `enqueuedAtMs`, schema line 125 / `execute.ts:103`).

---

### `internal/runner/docker.go` (modified — Go runner)

**Analog:** self. `Create` (line 158) and `Kill` (line 505).

**`sandbox.create` span + create-latency histogram:** wrap the body of `Create` (lines 158-294, around `ContainerCreate` line 292 + `ContainerStart`). Record `code_runner.sandbox.create.duration` (unit `s`) on return.
**Kill-latency histogram:** wrap `Kill` (lines 505-516) → `code_runner.sandbox.kill.duration`. Tolerate-and-record on the existing not-found-tolerant path.

---

### `internal/session/interactive.go` (modified — Go session)

**Analog:** self. `RunInteractive` (line 39) / `superviseInteractive` (line 64).

- `run` / `handshake.wait` / `compile` spans are started in `worker.go` (above); session emits **output-byte/seq counters**, NOT spans.
- **Output-bytes counter site:** the Pump sink budget accounting — `internal/session/pump.go` `Run()` (line 58+) is where bytes are forwarded to the sink; increment a `code_runner.output.bytes` counter there (the `budget`/forwarded-bytes path, pump.go lines 20-26 describe the shared budget). EXPLICITLY no per-chunk span (OBS-03 anti-pattern).

---

### `internal/jobstore/queue.go` + `capacity.go` (modified — Go store)

**Analog:** self.
- **Add `QueueDepth(ctx)`** in `queue.go` mirroring the existing method shape (`Claim` line 29, `Enqueue` line 47): a short `s.client.LLen(ctx, keys.JobQueue).Result()` wrapper. This is the **async-gauge callback source** (RESEARCH Pitfall 5 — short-timeout ctx, skip observation on error). The async-gauge registration lives in `otelinit`/worker wiring, not here.
- **Slots gauge** reads the in-memory semaphore `cap(w.slots)-len(w.slots)` in `worker.go` (no Redis). `capacity.go` (`IncrFreeSlots`/`DecrFreeSlots` lines 78-93) is the existing capacity-accounting analog but the gauge prefers the in-memory channel per Pitfall 5.

---

### `internal/reaper/reaper.go` (modified — Go service)

**Analog:** self. `reapContainer` (line 186) is called once per orphan removed.

**Counter site:** increment `code_runner.reaper.orphans` after the successful `ContainerRemove` (line 190-197), alongside the existing `slog.Info("reaper: container removed"...)` (line 197).

---

### `internal/publisher/publisher.go` (modified — Go publisher)

**Analog:** self. The `triggerer.Trigger` call sites (lines 103, 122, 134) and the `pusherTriggerer.Trigger` adapter (lines 37-39).

**Latency histogram + error counter site:** wrap `pusherTriggerer.Trigger` (lines 37-39) — time the `p.c.Trigger(...)` call → `code_runner.publish.duration` (unit `s`); on non-nil err → `code_runner.publish.errors` counter. This is the single chokepoint all events flow through.

---

### `packages/contract/schema/wire.schema.json` (modified — contract)

**Analog:** self. The `JobSpec.properties` block (lines 113-126) and `required` list (line 127).

**Edit (RESEARCH Pitfall 8):** add to `JobSpec.properties` (after line 125, `enqueuedAtMs`), and DO NOT add to `required` (line 127):
```json
"traceparent": { "type": "string", "description": "W3C trace-context header for cross-seam trace correlation (optional)." },
"tracestate":  { "type": "string", "description": "W3C tracestate header (optional)." }
```
`additionalProperties:false` (line 112) means these MUST be declared. Then run `pnpm contract` (Makefile:20) and verify `make contract-check` (Makefile:25-27) green. Confirm zod emits `.optional()` and Go emits `*string`. NEVER hand-edit `packages/contract/gen/**`.

---

### `docker-compose.yml` (modified — infra)

**Analog:** the `stub` service block (lines 145-167) — the **proven `profiles:` mechanism** (lines 166-167) and the `networks: [code-runner]` attachment (line 170-171).

**Pattern to copy** — add `otel-collector` + `jaeger` services under `profiles: [observability]`, exactly mirroring `stub`'s `profiles:`/`networks:` shape. Use RESEARCH §"Jaeger + Collector compose profile" for service definitions + the `observability/otel-collector.yaml` config. Inert on default `docker compose up`; runnable via `--profile observability` (D-09). Services point at `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318`.

---

### `.env.example` (modified — config/docs)

**Analog:** self. Append the documented `OTEL_*` block from RESEARCH §".env.example additions" (all commented/unset → true no-op; D-04 push-only; D-10 sampler `parentbased_traceidratio` arg `1.0`; kill-switches `OTEL_SDK_DISABLED`/`OTEL_TRACES_EXPORTER=none`).

---

## Shared Patterns

### No-op-when-unset env gate (OBS-01) — BOTH services
**Source (TS):** `apps/api/src/config.ts:24-32` (optional `process.env[...]` read) → RESEARCH Pattern 1.
**Source (Go):** RESEARCH Pattern 2 (`autoexport.IsNone*` + endpoint check).
**Apply to:** `apps/api/src/telemetry.ts`, `internal/otelinit/otelinit.go`.
The SDK is constructed/started ONLY when `OTEL_EXPORTER_OTLP_ENDPOINT` is present. Neither SDK is a no-op by default (RESEARCH Pitfall 2).

### Lazy singleton accessor
**Source:** `apps/api/src/redis.ts:7-23` (`getRedis()` + module-level `let _x: T | null`).
**Apply to:** `apps/api/src/logger.ts` (pino), `apps/api/src/metrics.ts` (meter), `apps/api/src/telemetry.ts` (SDK).

### Env-only config (12-factor)
**Source:** `apps/api/src/config.ts:34-54` and `internal/config/config.go` + `apps/worker/main.go:172-226` (`configFromEnv`).
**Apply to:** all `OTEL_*` reads. Standard `OTEL_*` vars are read by the SDK directly; only the endpoint-presence gate is read by app code. No config endpoints (CFG-01).

### Interface-behind-impl for testability
**Source:** `internal/publisher/publisher.go:28-39` (`triggerer` interface + adapter); `internal/worker/worker.go:52-57` (`Transport` interface).
**Apply to:** `internal/otelinit` (return providers/shutdown so tests can inject a `tracetest.SpanRecorder` / `metric.NewManualReader`). Metric/span emit points should be testable via in-memory recorders (RESEARCH Validation Architecture).

### Structured logging — replace console/extend slog (OBS-07)
**Source (TS):** every `console.log`/`console.error` in `apps/api/src` (`server.ts:12,22,28`, `app.ts:37`, `redis.ts:20`) → pino via `logger.ts`.
**Source (Go):** existing `slog` usage (`worker.go:231`, `main.go:50,73`) → extend with custom ctx handler (RESEARCH Pattern 4); switch hot paths to `slog.*Context(ctx,...)`.
**Apply to:** all request/job paths in both services. Never log secrets (`EXECUTOR_API_TOKEN`, `SOKETI_APP_SECRET`) or user code/stdin (RESEARCH Security V7/V8).

### Cross-seam trace context (W3C over Redis, never HTTP)
**Source:** RESEARCH Pattern 3 — inject (`execute.ts`) / extract+link (`worker.go`).
**Apply to:** `execute.ts` (inject into `spec.traceparent`), `worker.go` (extract on claim, `trace.WithLinks`). Worker still talks ONLY to Redis + soketi + (new) OTLP collector — NO HTTP to API (trust boundary preserved; the grep gate in worker tests still holds).

### Generated contract — never hand-edit gen
**Source:** Makefile:20-27 (`contract` / `contract-check`); CONTEXT "Established Patterns".
**Apply to:** the `wire.schema.json` edit only; regenerate + verify drift gate.

### Compose profile (inert by default)
**Source:** `docker-compose.yml:166-167` (`stub` `profiles:`).
**Apply to:** `otel-collector` + `jaeger` under `profiles: [observability]` (D-09).

### Low-cardinality metric attributes
**Source:** RESEARCH anti-patterns + D-07.
**Apply to:** all counters/histograms — `terminal_state`, `language`, `reason` only. `job_id` belongs on spans/logs, NEVER on metrics.

---

## No Analog Found

These have no close in-repo match; the planner should use the cited RESEARCH.md pattern instead.

| File | Lang | Role | Reason | Use Instead |
|------|------|------|--------|-------------|
| `internal/logging/handler.go` | Go | utility | No custom `slog.Handler` exists in repo | RESEARCH §Pattern 4 (verbatim) |
| `internal/otelinit/otelinit.go` (provider wiring) | Go | bootstrap | No OTel provider construction exists (pkgs are uninstrumented transitive deps) | RESEARCH §Pattern 2; constructor *shape* from `publisher.New` |
| `apps/api/src/telemetry.ts` (SDK wiring) | TS | bootstrap | No NodeSDK bootstrap exists | RESEARCH §Pattern 1; gate/singleton *shapes* from `config.ts`/`redis.ts` |
| `apps/api/src/metrics.ts` (OTel meter) | TS | utility | No OTel meter exists | RESEARCH §Pattern 5 (JS meter); module *shape* from `admission.ts` |

---

## Metadata

**Analog search scope:** `apps/api/src/**`, `apps/api/src/routes/**`, `packages/code-runner-sdk-node/src/**`, `apps/worker/**`, `internal/**` (config, worker, runner, session, jobstore, reaper, publisher), `packages/contract/schema/**`, `docker-compose.yml`, `Makefile`, `apps/api/package.json`, `apps/api/Dockerfile`.
**Files scanned (read in full or targeted):** ~22 source files.
**Pattern extraction date:** 2026-06-03
