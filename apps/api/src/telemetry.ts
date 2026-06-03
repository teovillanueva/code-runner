// Env-gated OpenTelemetry NodeSDK bootstrap (OBS-01, D-01, D-03).
//
// CRITICAL LOAD ORDER (RESEARCH Pitfall 1): this module is loaded via
//   node --import ./src/telemetry.ts src/server.ts
// BEFORE server.ts -> redis.ts -> ioredis is imported. The ioredis ESM
// instrumentation monkey-patches ioredis at module load, so the OTel ESM hook
// MUST register before ioredis loads or Redis spans never appear. Do NOT import
// this module from inside app code after ioredis.
//
// NO-OP GATE (OBS-01, RESEARCH Pitfall 2): the NodeSDK is NOT a no-op by
// default — it auto-creates a default OTLP exporter and tries to reach
// localhost:4318. We gate construction/start on OTEL_EXPORTER_OTLP_ENDPOINT, so
// with that env var unset the API behaves exactly as today (no exporter, no
// connection attempt, no new port). OTEL_SDK_DISABLED=true and
// OTEL_TRACES_EXPORTER=none remain the spec-standard escape hatches.
//
// Curated instrumentations (D-01 — NOT the kitchen-sink auto bundle):
//   - HTTP            (outbound/inbound http; @hono/otel handles the Hono span)
//   - ioredis         (Redis command spans for the API's client)
//   - pino            (injects trace_id/span_id into pino stdout JSON AND, with
//                      logRecordProcessors configured, ships logs over OTLP)
//
// service.name / service.version come from OTEL_SERVICE_NAME /
// OTEL_RESOURCE_ATTRIBUTES (config-driven, not hardcoded).

import {
  NodeSDK,
  metrics as sdkMetrics,
  logs as sdkLogs,
} from "@opentelemetry/sdk-node";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-proto";
import { OTLPMetricExporter } from "@opentelemetry/exporter-metrics-otlp-proto";
import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-proto";
import { IORedisInstrumentation } from "@opentelemetry/instrumentation-ioredis";
import { PinoInstrumentation } from "@opentelemetry/instrumentation-pino";
import { HttpInstrumentation } from "@opentelemetry/instrumentation-http";

let _started = false;

const endpoint = process.env["OTEL_EXPORTER_OTLP_ENDPOINT"];

if (endpoint) {
  const sdk = new NodeSDK({
    // service.name/version come from OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES
    traceExporter: new OTLPTraceExporter(),
    metricReader: new sdkMetrics.PeriodicExportingMetricReader({
      exporter: new OTLPMetricExporter(),
    }),
    // Pitfall 7: pino logSending only reaches OTLP if a LoggerProvider /
    // log record processor is configured here.
    logRecordProcessors: [
      new sdkLogs.BatchLogRecordProcessor(new OTLPLogExporter()),
    ],
    instrumentations: [
      new HttpInstrumentation(),
      new IORedisInstrumentation(),
      // correlation (stdout) + logSending (OTLP) both on by default
      new PinoInstrumentation(),
    ],
  });

  sdk.start();
  _started = true;

  process.on("SIGTERM", () => {
    void sdk.shutdown();
  });
}
// When endpoint is unset: SDK never starts → API behaves exactly as today (true no-op).

/** Observable gate state for tests / introspection (OBS-01). */
export function isTelemetryStarted(): boolean {
  return _started;
}
