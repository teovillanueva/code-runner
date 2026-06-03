# Phase 8: Distributed Observability — Specification

**Created:** 2026-06-03
**Ambiguity score:** 0.16 (gate: ≤ 0.20)
**Requirements:** 9 locked

## Goal

code-runner emits a connected distributed trace, domain metrics, and trace-correlated structured logs across the Hono API and the Go worker, driven entirely by standard `OTEL_*` env vars so that with no exporter configured the system behaves exactly as today (no-op), and a single execution produces one trace whose spans from API and worker share one `trace_id`.

## Background

The service is a polyglot monorepo with the trust/data seam at **Redis**, not HTTP: the API (`apps/api`, Hono/TS, `@hono/node-server`, `ioredis`) writes a `JobSpec` + `JobStatus` and `LPUSH`es a `jobId` onto `jobs:queue`; the Go worker (`apps/worker`, `slog`) `BRPOP`s the job, subscribes to `stdin:{id}`/`ctrl:{id}`, creates a hardened Docker sandbox, parks at a start-handshake, runs `internal/session`, and publishes `stage`/`stdout`/`stderr`/`result` events to soketi on `private-run-{id}`.

Current observability state:
- **API**: only `console.log`/`console.error`. No structured logging, no metrics, no tracing. `jobId` (from `randomUUID()`) is the de-facto correlation ID across all Redis keys/channels and the Docker container label.
- **Worker**: uses stdlib `log/slog` (startup + reaper). No metrics, no tracing. OpenTelemetry packages are already present transitively in `go.mod` (pulled by the Docker SDK) but never instrumented.
- **Contract** (`packages/contract`): `JobSpec`, `ControlMessage`, `StdinMessage`, and the soketi event shapes carry **no** trace-context field. The schema is the single source of truth (`packages/contract/schema/wire.schema.json`); TS/zod/Go are generated via `pnpm contract` and gated by `make contract-check` — `gen/**` is never hand-edited.
- **Metric-grade signals already exist**: `LLEN jobs:queue` (queue depth), `capacity:free` counter, worker heartbeat keys, `enqueuedAtMs`/`updatedAtMs` timestamps, per-job `seq`, and the terminal flags in `ResultEvent` (`exitCode`, `signal`, `timedOut`, `idleTimedOut`, `truncated`, `durationMs`).
- **Infra**: `docker-compose.yml` runs redis + soketi + api + worker + stub; no OTel Collector. Fly deploy has the API at `min_machines_running = 0` (scale-to-zero), and the worker scales by queue depth — which is why pull-based scraping is fragile and OTLP push is the default.
- **SDK**: `@teovilla/code-runner-sdk-node` is the published Node client that calls `/v1/execute`; it currently does not propagate an active caller trace.

This phase adds the three OpenTelemetry pillars without changing runtime behavior when telemetry is disabled.

## Requirements

1. **Env-gated OTel SDK (OBS-01)**: Both services initialize an OpenTelemetry SDK configured only by standard `OTEL_*` env vars.
   - Current: Neither service has any OTel initialization; API has no structured logger.
   - Target: API and worker each bootstrap an OTel SDK at startup reading `OTEL_*` (endpoint, service name, resource attrs, sampler). When no OTLP endpoint is configured the SDK installs no exporters and is a no-op.
   - Acceptance: With all `OTEL_*` unset, `docker compose up` starts both services and a full interactive execute completes successfully with no telemetry emitted and no startup error; with `OTEL_EXPORTER_OTLP_ENDPOINT` set, spans/metrics appear at the collector.

2. **Trace context across the Redis seam (OBS-02)**: W3C trace context is carried in the wire contract, not via HTTP.
   - Current: `JobSpec` has no trace-context field; API→worker correlation is only `jobId`.
   - Target: The wire schema gains a trace-context carrier (`traceparent` + optional `tracestate`) on `JobSpec` (and on `ControlMessage`/`StdinMessage` where needed for stdin/kill spans). The API injects the active context at `/v1/execute`; the worker extracts it on claim.
   - Acceptance: A unit/integration test asserts the `traceparent` written by the API into `JobSpec` is read by the worker and that the resulting API span and worker span share the same `trace_id`.

3. **Phase-level worker spans with span links (OBS-03)**: The worker emits spans per execution phase, not per output chunk.
   - Current: No spans anywhere in the worker.
   - Target: The worker creates spans `claim`, `sandbox.create`, `handshake.wait`, `compile`, `run`, `publish.result` mapped to `internal/session` + `internal/runner`, linked to the API's `execute` span (span link, not rigid parent-child, since `/v1/execute` returns 202 before execution). Output throughput is recorded as metrics (byte/seq counters), never as one span per chunk.
   - Acceptance: An execution produces the named phase spans under one trace; an interactive job emitting N stdout chunks produces 0 per-chunk spans and a nonzero output-bytes metric.

4. **OTLP push export — default model (OBS-04)**: Traces, metrics, and logs export via OTLP push to an OTel Collector by default.
   - Current: No exporters of any kind.
   - Target: When configured, both services push OTLP (traces + metrics + logs) to `OTEL_EXPORTER_OTLP_ENDPOINT`. This is the documented primary "bring your own stack" path, robust under scale-to-zero.
   - Acceptance: With the example collector running, an execute produces spans and metrics received by the collector (verified via collector debug/export output).

5. **Opt-in Prometheus `/metrics` (OBS-05)**: A pull endpoint is available on a separate admin surface.
   - Current: No metrics endpoint exists.
   - Target: When enabled by env, each service exposes a Prometheus `/metrics` scrape endpoint on a separate admin port — NOT behind the public `EXECUTOR_API_TOKEN` bearer auth on the `/v1/*` gateway.
   - Acceptance: With the pull endpoint enabled, `GET /metrics` on the admin port returns Prometheus exposition format including the domain metrics; the public `/v1/*` surface and its auth are unchanged; with the endpoint disabled (default), no admin port is opened.

6. **Domain metrics (OBS-06)**: The code-runner-specific operational metrics are emitted.
   - Current: Signals exist in Redis/events but are not exposed as metrics.
   - Target: Emit at minimum — queue depth (`jobs:queue`), sandbox slots used/max, time-in-queue (`now − enqueuedAtMs` at claim), terminal-state counts including `timedOut`/`idleTimedOut`/`cpuExceeded`/`done`/`killed`/`error`, sandbox create/kill latency, warmup reclaims, reaper orphans removed, admission (`429` queue-depth) rejections, ratelimit (stdin frame/byte-cap) rejections, soketi publish latency and errors.
   - Acceptance: Each listed metric is present in OTLP export and on `/metrics`; a job killed by the idle clock increments the `idleTimedOut` terminal counter; a job rejected by admission increments the admission-rejection counter.

7. **Trace-correlated structured logs (OBS-07)**: Both services log structured JSON with correlation fields.
   - Current: API uses `console.log`/`console.error` (unstructured); worker uses `slog` without trace fields.
   - Target: API migrates to a structured JSON logger; both services include `trace_id`, `span_id`, and `job_id` fields on logs emitted within a job's context.
   - Acceptance: A logged line emitted during an execution (in each service) is valid JSON and contains `trace_id`, `span_id`, and `job_id` matching that execution's trace; no remaining `console.log`/`console.error` in `apps/api/src` request/job paths.

8. **SDK Node trace propagation (OBS-02 ext.)**: The Node SDK propagates an active caller trace into the request.
   - Current: `@teovilla/code-runner-sdk-node` makes plain HTTP calls with no trace propagation.
   - Target: When the caller's process has an active OTel context, the SDK injects W3C `traceparent` into the `/v1/execute` request so the API continues that trace; when there is no active context (or OTel absent), behavior is unchanged.
   - Acceptance: A test where the caller starts a span and invokes the SDK results in the API's `execute` span sharing the caller's `trace_id`; with no active caller span, the request still succeeds and the API starts a fresh trace.

9. **Sampling config + example Collector (OBS-08)**: Sampling is configurable and the BYO integration point ships as an example.
   - Current: No sampler; no collector in compose; `.env.example` has no `OTEL_*` vars.
   - Target: Sampler configurable via standard env (`parentbased_traceidratio` with a configurable ratio); a commented example `otel-collector` service is added to `docker-compose.yml` with a sample config (tail-sampling note for always-keeping error/anomalous-terminal traces documented); `.env.example` documents every new `OTEL_*` var.
   - Acceptance: Setting the sampler ratio env changes sampled-trace volume; uncommenting the collector service in compose yields a running collector that receives OTLP from both services; `.env.example` lists each `OTEL_*` var with a description.

## Boundaries

**In scope:**
- OpenTelemetry traces, metrics, and logs in `apps/api` (Hono/TS) and `apps/worker` (Go).
- W3C trace-context carrier added to the wire contract (schema + regenerated TS/zod/Go via `pnpm contract`).
- Phase-level worker spans mapped to `internal/session` + `internal/runner`; output as metrics.
- OTLP push (default) AND opt-in Prometheus `/metrics` on a separate admin port.
- The domain metrics set listed in OBS-06.
- Structured JSON logging in both services with `trace_id`/`span_id`/`job_id`; API migrated off `console.log`.
- Trace propagation in `@teovilla/code-runner-sdk-node` (caller → API).
- Configurable sampling; example `otel-collector` service in `docker-compose.yml`; `.env.example` + docs for all new `OTEL_*` vars.
- An end-to-end verification: example collector + a trace backend (Jaeger/Tempo) in compose proving one connected trace.

**Out of scope:**
- Instrumenting Redis and soketi themselves — documentation only (how a self-hoster adds `redis_exporter` / soketi metrics to their stack); keeps the phase focused on our own code.
- `@teovilla/code-runner-react` and the E2E `stub` — client/demo surfaces with little operational value; not instrumented.
- Shipping/operating a full backend (Grafana dashboards, alerting rules, long-term storage) — BYO stack; we provide the emission + an example collector only.
- Changing the runtime trust boundary, auth model, or the existing `/v1/*` contract semantics — telemetry is additive.
- Redis Streams / guaranteed-delivery changes (that is v2, V2-03) — unrelated to observability.
- Per-chunk output spans — explicitly excluded for cost/cardinality reasons (output is metrics).

## Constraints

- **No behavioral change when disabled**: with no `OTEL_*` exporter configured, the SDK must be a true no-op — no new ports opened, no measurable startup regression, identical execute behavior.
- **Contract is generated, never hand-edited**: the trace-context field is added by editing `packages/contract/schema/wire.schema.json` and running `pnpm contract`; `make contract-check` must stay green (TS types + zod + Go structs regenerated, no drift).
- **Cross-language correlation must hold** between TS (`@hono/node-server`, `ioredis`) and Go OTel SDKs — same `trace_id` byte format over the Redis-carried `traceparent`.
- **Pull is fragile under scale-to-zero**: OTLP push is the default; `/metrics` pull is opt-in and must not be the only path.
- **Admin/metrics surface must not sit behind the public bearer gateway** nor be exposed on the internet-facing `/v1/*` path.
- **Standard env conventions**: prefer the OpenTelemetry-standard `OTEL_*` env vars over bespoke names wherever an SDK supports them.
- **MIT / self-hostable**: no proprietary or paid backend assumed; everything works against any OTLP-compatible stack.

## Acceptance Criteria

- [ ] With all `OTEL_*` unset, both services start and a full interactive execute completes with zero telemetry emitted and no admin port opened.
- [ ] With `OTEL_EXPORTER_OTLP_ENDPOINT` set, a single execute produces one trace whose API `execute` span and worker phase spans (`claim`, `sandbox.create`, `handshake.wait`, `compile`, `run`, `publish.result`) share one `trace_id`.
- [ ] The `traceparent` is propagated through the wire contract (`JobSpec`) across Redis — verified by a test asserting shared `trace_id` between API and worker.
- [ ] `make contract-check` passes after the schema change (regenerated TS/zod/Go, no drift).
- [ ] An interactive job emitting many stdout chunks produces zero per-chunk spans and a nonzero output-bytes metric.
- [ ] All OBS-06 domain metrics appear both in OTLP export and on the opt-in `/metrics` endpoint; idle-killed job increments `idleTimedOut`; admission-rejected request increments the admission-rejection counter.
- [ ] The `/metrics` endpoint is served on a separate admin port and is reachable without the `EXECUTOR_API_TOKEN`; the public `/v1/*` auth is unchanged; endpoint is absent when disabled.
- [ ] A log line from each service during an execution is valid JSON containing matching `trace_id`/`span_id`/`job_id`; no `console.log`/`console.error` remains in `apps/api/src` request/job paths.
- [ ] With an active caller span, an `@teovilla/code-runner-sdk-node` call yields an API `execute` span sharing the caller's `trace_id`; with no active span the call still succeeds.
- [ ] Changing the sampler ratio env var changes sampled-trace volume; the example `otel-collector` in compose receives OTLP from both services; `.env.example` documents every new `OTEL_*` var.
- [ ] End-to-end: with the example collector + trace backend (Jaeger/Tempo) running in compose, one execution is visible as a single connected trace spanning API and worker.

## Ambiguity Report

| Dimension          | Score | Min  | Status | Notes                                                        |
|--------------------|-------|------|--------|--------------------------------------------------------------|
| Goal Clarity       | 0.88  | 0.75 | ✓      | One connected trace, env-gated no-op, 3 pillars, both svcs   |
| Boundary Clarity   | 0.86  | 0.70 | ✓      | API+worker+SDK Node in; React/stub out; infra doc-only       |
| Constraint Clarity | 0.75  | 0.65 | ✓      | Generated contract, push-default, no-op-when-disabled        |
| Acceptance Criteria| 0.82  | 0.70 | ✓      | E2E connected-trace assertion + per-metric checks            |
| **Ambiguity**      | 0.16  | ≤0.20| ✓      |                                                              |

Status: ✓ = met minimum, ⚠ = below minimum (planner treats as assumption)

## Interview Log

| Round | Perspective     | Question summary                              | Decision locked                                                        |
|-------|-----------------|-----------------------------------------------|------------------------------------------------------------------------|
| 0     | Pre-spec (chat) | Integration model + pillar scope              | BOTH (OTLP push default + opt-in Prometheus pull); all 3 pillars        |
| 0     | Pre-spec (chat) | GSD structure                                 | Register as Phase 8 of current roadmap                                  |
| 1     | Researcher      | Which components get instrumented?            | apps/api + apps/worker + SDK Node (caller propagation); React/stub out  |
| 1     | Boundary Keeper | How is a connected trace proven?              | E2E real: collector + backend (Jaeger/Tempo) in compose, one trace_id  |
| 1     | Boundary Keeper | Redis/soketi observability in scope?          | Documentation only — no exporters shipped this phase                    |

---

*Phase: 08-distributed-observability*
*Spec created: 2026-06-03*
*Next step: /gsd:discuss-phase 8 — implementation decisions (OTel package choices, Hono middleware, admin port, metric instrument types, collector config)*
