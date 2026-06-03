# Phase 8: Distributed Observability - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-06-03
**Phase:** 08-distributed-observability
**Areas discussed:** API OTel + logging stack, Admin /metrics port (DROPPED), Metric instruments & naming, Collector + trace backend, Trace-context carrier shape

---

## Area selection

| Option | Description | Selected |
|--------|-------------|----------|
| API OTel + logging stack | OTel wiring in Hono/Node + structured logging library | ✓ |
| Admin /metrics port | Opt-in Prometheus pull surface on both services | ✓ (then dropped — see below) |
| Metric instruments & naming | Instrument types, namespace, terminal-state counting | ✓ |
| Collector + trace backend (E2E) | Trace backend, compose shipping, sampling default | ✓ |

**User's choice:** All four areas.

---

## API OTel + logging stack

| Option | Description | Selected |
|--------|-------------|----------|
| Selective auto + manual execute span | Curated auto-instrumentation (http/@hono/otel + ioredis) + explicit execute span injecting traceparent | ✓ |
| Full auto-instrumentations-node | Everything bundle | |
| Fully manual | Hand-write every span | |

| Option | Description | Selected |
|--------|-------------|----------|
| pino | Fast JSON logger, first-class OTel trace injection | ✓ |
| OTel Logs SDK directly | No separate logger lib | |
| winston | Slower, weaker OTel integration | |

| Option | Description | Selected |
|--------|-------------|----------|
| stdout JSON always + OTLP when configured; AsyncLocalStorage for job_id | Dual path; ALS carries job_id | ✓ |
| OTLP logs only | Single path, no stdout when enabled | |
| stdout JSON only | No OTLP log push | |

**Notes:** All recommended options. job_id propagation via AsyncLocalStorage.

---

## Admin /metrics port — DROPPED

**User's choice (free-text):** "pincho el endpoint de metrics, no lo hagamos" — drop the opt-in Prometheus `/metrics` pull endpoint (OBS-05) entirely. Telemetry export becomes **OTLP push only**.

**Notes:** This is a deliberate founder-level change to a locked SPEC requirement (OBS-05). Recorded as a scope reduction in CONTEXT.md `<spec_lock>`. Consequences: no admin HTTP server / port on either service; Go worker stays HTTP-server-free; OBS-06 metrics emitted via OTLP push only; "metrics on /metrics" acceptance-criteria half is void. The admin-port follow-up questions were not asked.

---

## Metric instruments & naming

| Option | Description | Selected |
|--------|-------------|----------|
| Gauges + histograms + counters, mapped by signal shape | Async gauges (queue/slots), histograms (latencies), counters (terminal/rejections) | ✓ |
| Counters + histograms only | Up/down counters instead of async gauges | |

| Option | Description | Selected |
|--------|-------------|----------|
| code_runner.* prefix, OTel semconv units | Dotted namespace, semconv units | ✓ |
| coderunner_* flat prom-style | Flat snake_case | |

| Option | Description | Selected |
|--------|-------------|----------|
| One counter with terminal_state attribute | Single code_runner.jobs.terminal + low-cardinality attribute | ✓ |
| One counter per terminal state | Separate counters | |

**Notes:** All recommended.

---

## Collector + trace backend (E2E)

| Option | Description | Selected |
|--------|-------------|----------|
| Jaeger all-in-one | Native OTLP ingest + built-in UI, lowest friction | ✓ |
| Grafana Tempo (+ Grafana) | More prod-shaped, more containers | |
| Collector debug exporter only | No UI backend | |

| Option | Description | Selected |
|--------|-------------|----------|
| Compose profile (--profile observability) | Inert by default, one-flag enable | ✓ |
| Commented-out services (as SPEC wrote) | Literal SPEC wording | |

| Option | Description | Selected |
|--------|-------------|----------|
| parentbased_traceidratio arg=1.0 + prod-lower guidance | Capture all traces in example; tail-sample errors at collector | ✓ |
| Lower default ratio (0.1) | Prod-realistic default | |

**Notes:** Profile choice is a minor, documented deviation from SPEC's "commented-out" wording (better UX).

---

## Trace-context carrier shape

| Option | Description | Selected |
|--------|-------------|----------|
| JobSpec now; Control/Stdin only if spans need them | Minimal schema change; no trace headers on stdin frames | ✓ |
| All three messages now | Future-proof but bloats stdin frames | |

| Option | Description | Selected |
|--------|-------------|----------|
| Optional traceparent + tracestate strings | W3C header names, 1:1 propagator mapping | ✓ |
| Nested traceContext object | More codegen surface | |

**Notes:** Span-linking (not parent-child) already locked by SPEC since /v1/execute returns 202 before the run.

---

## Claude's Discretion

- Exact OTLP exporter packages/versions, resource attributes, propagator registration order, histogram bucket boundaries, OTel Collector pipeline config.
- Whether the API OTLP log bridge uses the pino transport vs the OTel Logs SDK appender (keep stdout-JSON-always intact either way).

## Deferred Ideas

- Prometheus `/metrics` pull endpoint + admin port (OBS-05) — cut this phase; non-breaking to add later (instruments are backend-agnostic).
- Redis/soketi self-instrumentation — docs only.
- React SDK instrumentation; full Grafana/Tempo/Loki backend, dashboards, alerting — out of scope / BYO.
