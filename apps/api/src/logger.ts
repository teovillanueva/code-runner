// pino structured logger (D-02) + AsyncLocalStorage job_id correlation (D-03).
//
// Mirrors the lazy-singleton accessor shape of redis.ts:getRedis(). Every log
// emitted inside `jobContext.run({ jobId }, ...)` automatically carries
// `job_id` via the pino `mixin`. Trace fields (trace_id/span_id/trace_flags)
// are injected by @opentelemetry/instrumentation-pino, which is registered in
// telemetry.ts when OTEL is configured — so correlation is automatic and the
// logger has no OTel coupling itself.
//
// Security (T-08-07 / RESEARCH V7): never log EXECUTOR_API_TOKEN,
// SOKETI_APP_SECRET, or user code/stdin. The mixin emits only the allow-listed
// `job_id`; callers must not pass secrets/user payloads as log fields.

import { AsyncLocalStorage } from "node:async_hooks";
import {
  pino,
  type DestinationStream,
  type Logger,
  type LoggerOptions,
} from "pino";

/** Per-request job context so logs within a job carry job_id. */
export const jobContext = new AsyncLocalStorage<{ jobId: string }>();

/**
 * Shared pino options — the `mixin` is the correlation contract under test:
 * every log emitted inside `jobContext.run({ jobId }, ...)` carries `job_id`.
 * Trace fields (trace_id/span_id) are injected separately by
 * instrumentation-pino (registered in telemetry.ts when OTEL is configured).
 */
export function loggerOptions(): LoggerOptions {
  return {
    level: process.env["LOG_LEVEL"] ?? "info",
    mixin() {
      const store = jobContext.getStore();
      return store ? { job_id: store.jobId } : {};
    },
  };
}

let _logger: Logger | null = null;

/** Lazy singleton pino logger (mirrors redis.ts:getRedis()). */
export function getLogger(): Logger {
  if (!_logger) {
    _logger = pino(loggerOptions());
  }
  return _logger;
}

/**
 * Build a one-off logger writing to a caller-supplied destination, sharing the
 * exact production options (mixin/level). Used by tests to capture output
 * deterministically (pino's default sink writes to fd 1, bypassing stream
 * monkey-patches). Not used in production code paths.
 */
export function createLogger(destination: DestinationStream): Logger {
  return pino(loggerOptions(), destination);
}
