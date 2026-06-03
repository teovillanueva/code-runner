# Phase 8: Distributed Observability - Context

**Gathered:** 2026-06-03
**Status:** Ready for planning

<domain>
## Phase Boundary

code-runner emits a connected distributed trace, domain metrics, and trace-correlated structured logs across the Hono API and the Go worker, driven by standard `OTEL_*` env vars — a true no-op when no exporter is configured. A single execution produces one trace whose API and worker spans share one `trace_id`, with W3C trace context riding the wire contract across the Redis seam (not HTTP).

</domain>

<spec_lock>
## Requirements (locked via SPEC.md)

**9 requirements are locked.** See `08-SPEC.md` for full requirements, boundaries, and acceptance criteria.

Downstream agents MUST read `08-SPEC.md` before planning or implementing. Requirements are not duplicated here.

**⚠ SCOPE CHANGE this discussion (founder decision):** **OBS-05 (opt-in Prometheus `/metrics` pull endpoint) is DROPPED.** Telemetry export is **OTLP push only**. No admin port is opened on either service; the Go worker stays HTTP-server-free. The OBS-06 domain metrics are still emitted, but **only via OTLP push** — the "also on `/metrics`" half of the OBS-06 / E2E acceptance criteria no longer applies. The "pull is opt-in, must not be the only path" constraint is moot: push is the single path (already the spec's documented default).

**In scope (from SPEC.md, as amended above):**
- OpenTelemetry traces, metrics, and logs in `apps/api` (Hono/TS) and `apps/worker` (Go).
- W3C trace-context carrier added to the wire contract (schema + regenerated TS/zod/Go via `pnpm contract`).
- Phase-level worker spans mapped to `internal/session` + `internal/runner`; output as metrics.
- **OTLP push only** (Prometheus `/metrics` pull dropped — see scope change above).
- The domain metrics set listed in OBS-06.
- Structured JSON logging in both services with `trace_id`/`span_id`/`job_id`; API migrated off `console.log`.
- Trace propagation in `@teovilla/code-runner-sdk-node` (caller → API).
- Configurable sampling; example collector + trace backend in `docker-compose.yml`; `.env.example` + docs for all new `OTEL_*` vars.
- End-to-end verification: example collector + trace backend proving one connected trace.

**Out of scope (from SPEC.md):**
- Instrumenting Redis and soketi themselves — documentation only.
- `@teovilla/code-runner-react` and the E2E `stub`.
- Shipping/operating a full backend (dashboards, alerting, long-term storage) — BYO stack; example collector only.
- Changing the runtime trust boundary, auth model, or `/v1/*` contract semantics — telemetry is additive.
- Redis Streams / guaranteed-delivery changes (v2, V2-03).
- Per-chunk output spans — output is metrics.
- **The Prometheus `/metrics` pull endpoint + admin port (OBS-05)** — dropped this discussion.

</spec_lock>

<decisions>
## Implementation Decisions

### API OTel + logging stack
- **D-01:** API instrumentation = **selective auto-instrumentation + manual `execute` span.** Register a curated `NodeSDK` auto-instrumentation set (HTTP / `@hono/otel` + `ioredis`) so inbound requests and Redis ops get spans for free; add an explicit `execute` span at `POST /v1/execute` where the active context is **injected as `traceparent` into the `JobSpec`**. Avoid the full `auto-instrumentations-node` kitchen-sink bundle (noise + dep surface).
- **D-02:** Structured logger = **pino**, replacing all `console.log`/`console.error` in `apps/api/src` request/job paths. Use pino's OTel integration (e.g. `@opentelemetry/instrumentation-pino`) for automatic `trace_id`/`span_id` injection.
- **D-03:** Log export = **stdout JSON always + OTLP logs when configured.** Always write trace-correlated JSON to stdout (works with OTEL unset; Docker/collector can scrape; survives scale-to-zero). When OTLP is configured, ALSO bridge logs via the OTel logs exporter. `job_id` flows through **`AsyncLocalStorage`** so every log within a job's context carries it automatically.

### Telemetry export model (push-only)
- **D-04:** **OTLP push is the single export path** for traces, metrics, and logs (`OTEL_EXPORTER_OTLP_ENDPOINT`). The opt-in Prometheus pull endpoint (OBS-05) is **dropped** — no admin HTTP server, no admin port, on either service. Worker remains HTTP-server-free.

### Domain metric instruments & naming (OTLP push)
- **D-05:** Instrument types mapped by signal shape: **observable (async) gauges** for queue depth (`LLEN jobs:queue`) and slots used/max — callback reads Redis/capacity on collect, zero hot-path cost; **histograms** for time-in-queue, sandbox create/kill latency, soketi publish latency; **counters** for terminal-state counts, admission (`429`) rejections, ratelimit rejections, warmup reclaims, reaper orphans.
- **D-06:** Naming = **`code_runner.*` dotted namespace** (e.g. `code_runner.queue.depth`, `code_runner.sandbox.create.duration`, `code_runner.jobs.terminal`), durations per OTel semconv units with unit metadata set. Backend-agnostic; prom name translation is automatic if ever needed.
- **D-07:** Terminal states = **one counter (`code_runner.jobs.terminal`) with a low-cardinality `terminal_state` attribute** (`done`/`killed`/`timed_out`/`idle_timed_out`/`cpu_exceeded`/`error`). An idle-killed job increments it with `terminal_state=idle_timed_out`.

### Example collector + trace backend (E2E proof)
- **D-08:** Trace backend = **Jaeger all-in-one** (`jaegertracing/all-in-one`, native OTLP ingest + built-in UI) — lowest friction to SEE the one connected trace.
- **D-09:** Shipped via a **named docker-compose profile** (e.g. `--profile observability`) rather than commented-out YAML. Inert on default `docker compose up` (no-op-when-unset spirit), runnable as-is with the flag. **Deviates from SPEC's literal "commented-out" wording** for better UX — note for the planner.
- **D-10:** Sampling default = **`OTEL_TRACES_SAMPLER=parentbased_traceidratio`, `OTEL_TRACES_SAMPLER_ARG=1.0`** documented in `.env.example` so the example captures every trace (first-run visibility), with a comment to lower the ratio in prod. Document **tail-sampling at the collector** to always-keep error/anomalous-terminal traces.

### Trace-context carrier (wire contract seam)
- **D-11:** Carrier placement = **`JobSpec` now; `ControlMessage`/`StdinMessage` only if per-stdin/per-kill spans are actually emitted.** `JobSpec` is the mandatory cross-seam handoff that makes API and worker spans share a trace. Per OBS-03 output is metrics, so stdin/kill spans are optional — keep the schema change minimal and avoid trace headers on high-frequency stdin frames.
- **D-12:** Field shape = **two optional top-level strings, `traceparent` + `tracestate`**, matching the W3C header names exactly. Optional ⇒ absent when OTEL off (backward-compatible no-op). Maps 1:1 to the OTel `TextMapPropagator` inject/extract in both TS and Go — zero translation, minimal codegen surface. NOT a nested object.
- **D-13 (locked by SPEC, restated):** Worker phase spans (`claim`, `sandbox.create`, `handshake.wait`, `compile`, `run`, `publish.result`) **link** to the API `execute` span (span link, not rigid parent-child) because `/v1/execute` returns 202 before execution.

### Claude's Discretion
- Exact OTLP exporter packages/versions, resource-attribute set, propagator registration order, histogram bucket boundaries, and OTel Collector pipeline config — researcher/planner decide, grounded in current OTel JS + Go docs (use Context7).
- Whether the API's OTLP log bridge uses the pino transport vs the OTel Logs SDK appender — pick whatever is current/idiomatic; keep the stdout-JSON-always behavior (D-03) intact.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Spec & requirements (read first)
- `.planning/phases/08-distributed-observability/08-SPEC.md` — Locked requirements (OBS-01..08), boundaries, acceptance criteria. MUST read. Note the OBS-05 drop recorded in this CONTEXT's `<spec_lock>`.
- `.planning/REQUIREMENTS.md` — OBS-* requirement IDs.
- `.planning/PROJECT.md` — Key Decisions (ioredis/go-redis, worker↔API only via Redis+soketi, scale-to-zero deployment model).

### Wire contract (the generated seam — never hand-edit `gen/**`)
- `packages/contract/schema/wire.schema.json` — single source of truth; add `traceparent`/`tracestate` here, then `pnpm contract`, then `make contract-check`.
- `packages/contract/src/index.ts` — exported `keys`, `channelForJob`, `stdinChannel`, `controlChannel`, `events`; consumed by API + SDK.
- `internal/keys/keys.go` — Go mirror of contract keys/channels.

### API (Hono/TS) — instrument + migrate logging
- `apps/api/src/server.ts`, `apps/api/src/app.ts` — boot + Hono app wiring (OTel SDK bootstrap goes at startup; second listener NOT needed — pull dropped).
- `apps/api/src/routes/execute.ts` — where the `execute` span starts + `traceparent` is injected into JobSpec.
- `apps/api/src/routes/control.ts`, `apps/api/src/routes/jobs.ts` — stdin/kill/status paths (console.log → pino; optional stdin/kill spans).
- `apps/api/src/redis.ts`, `apps/api/src/admission.ts`, `apps/api/src/ratelimit.ts` — metric emission points (admission 429s, ratelimit rejections, queue ops).
- `apps/api/package.json` — current deps (hono 4.12, @hono/node-server 2.0, ioredis 5.11, pusher 5.3; no logger/OTel yet).

### Worker (Go) — spans + metrics + slog trace fields
- `apps/worker/main.go` — worker boot (OTel SDK init at startup).
- `internal/worker/worker.go` — claim → create → handshake → session → teardown run loop; extract `traceparent` on claim; phase spans + span link here.
- `internal/session/interactive.go`, `internal/session/pump.go`, `internal/session/clocks_test.go` — `run`/`handshake.wait`/`compile` spans; terminal-state counter increments; output-bytes/seq metrics (NOT per-chunk spans).
- `internal/runner/docker.go`, `internal/runner/cgroup.go` — `sandbox.create` span + create/kill latency histograms.
- `internal/jobstore/queue.go`, `internal/jobstore/capacity.go` — queue-depth / slots gauges (callback source), time-in-queue.
- `internal/reaper/reaper.go` — reaper-orphans counter.
- `internal/publisher/publisher.go` — soketi publish latency/error metrics.
- `internal/config/config.go` — env loader (add `OTEL_*` reads).

### SDK (Node) — caller trace propagation
- `packages/sdk-node` (`@teovilla/code-runner-sdk-node`) — inject W3C `traceparent` into `/v1/execute` when caller has an active OTel context; unchanged when absent.

### Infra & docs
- `docker-compose.yml` — add `otel-collector` + `jaeger` under a `observability` profile.
- `.env.example` — document every new `OTEL_*` var.
- `CLAUDE.md` — contract regen workflow, build/test commands.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- **`jobId` as de-facto correlation ID:** already threaded through every Redis key/channel and the Docker container label — reuse directly as the `job_id` log/span attribute.
- **Existing metric-grade signals:** `LLEN jobs:queue`, `capacity:free` counter, worker heartbeat keys, `enqueuedAtMs`/`updatedAtMs`, per-job `seq`, and `ResultEvent` terminal flags (`exitCode`/`signal`/`timedOut`/`idleTimedOut`/`truncated`/`durationMs`) — map straight onto the OBS-06 instruments; no new bookkeeping needed.
- **Worker `slog`:** already used (startup + reaper) — extend with `trace_id`/`span_id`/`job_id` fields rather than introducing a new logger.
- **OTel Go packages already transitively in `go.mod`** (pulled by the Docker SDK) — uninstrumented today.

### Established Patterns
- **Generated contract, never hand-edited:** schema → `pnpm contract` → `make contract-check` (TS+zod+Go). The `traceparent` field MUST be added this way.
- **Env-only config:** both services configured purely via env (12-factor). `OTEL_*` follows the same pattern; no config endpoints.
- **Worker talks to API only via Redis + soketi** — trace context MUST cross via the Redis-carried `JobSpec`, never an HTTP call.

### Integration Points
- API `POST /v1/execute` (`routes/execute.ts`): start trace, inject `traceparent` into JobSpec before LPUSH.
- Worker claim (`internal/worker/worker.go`): extract `traceparent` from JobSpec on BRPOP, start linked phase spans.
- OTel SDK bootstrap: API at `server.ts` startup; worker at `main.go` startup — both no-op when `OTEL_*` unset.

</code_context>

<specifics>
## Specific Ideas

- Founder explicitly cut the Prometheus pull endpoint ("pincho el endpoint de metrics, no lo hagamos") — push-only, keep it lean. This is the headline deviation from SPEC; planner must treat OBS-05 as out of scope.
- First-run experience matters: sampler ratio 1.0 in the example + Jaeger all-in-one + a one-flag `--profile observability` so a self-hoster can SEE their single connected trace immediately.

</specifics>

<deferred>
## Deferred Ideas

- **Prometheus `/metrics` pull endpoint + admin port (OBS-05)** — cut this phase; could return as a future opt-in if a pull-based stack is ever needed. The OBS-06 instruments are defined backend-agnostically (`code_runner.*`), so adding a Prometheus reader later is non-breaking.
- Redis/soketi self-instrumentation (exporters) — documentation only this phase (per SPEC out-of-scope).
- `@teovilla/code-runner-react` instrumentation — out of scope.
- Grafana/Tempo/Loki full backend, dashboards, alerting — BYO stack.

</deferred>

---

*Phase: 8-distributed-observability*
*Context gathered: 2026-06-03*
