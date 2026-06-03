// Tests for the env-gated NodeSDK bootstrap (telemetry.ts) and the pino
// logger + AsyncLocalStorage job_id mixin (logger.ts).
//
// OBS-01 (no-op gate): when OTEL_EXPORTER_OTLP_ENDPOINT is unset, importing
// telemetry.ts MUST NOT construct or start a NodeSDK — true no-op, no exporter,
// no connection attempt to localhost:4318.
//
// OBS-07 (job_id correlation): a logger built from the same production options
// (loggerOptions/mixin) carries job_id when run inside jobContext.run.
//
// Security (T-08-07 / RESEARCH V7): no secret (EXECUTOR_API_TOKEN,
// SOKETI_APP_SECRET) appears in logger output.

import { describe, it, expect } from "vitest";
import { Writable } from "node:stream";

/**
 * A pino DestinationStream that captures each newline-delimited JSON record.
 * pino's default sink writes to fd 1 directly (sonic-boom), bypassing any
 * process.stdout.write monkey-patch — so we inject this stream via
 * createLogger() instead, which shares the exact production loggerOptions().
 */
function captureStream(): { stream: Writable; lines: string[] } {
  const lines: string[] = [];
  const stream = new Writable({
    write(chunk, _enc, cb) {
      lines.push(chunk.toString());
      cb();
    },
  });
  return { stream, lines };
}

describe("telemetry.ts — env-gated NodeSDK (OBS-01 no-op gate)", () => {
  it("does NOT start a NodeSDK when OTEL_EXPORTER_OTLP_ENDPOINT is unset", async () => {
    // The vitest env (vitest.config.ts) never sets OTEL_EXPORTER_OTLP_ENDPOINT,
    // so this import must be a true no-op.
    expect(process.env["OTEL_EXPORTER_OTLP_ENDPOINT"]).toBeUndefined();

    const mod = await import("../src/telemetry.ts");

    // The module exports its initialized state so the gate is observable.
    expect(mod.isTelemetryStarted()).toBe(false);
  });
});

describe("logger.ts — pino + AsyncLocalStorage job_id mixin (OBS-07)", () => {
  it("emits valid JSON carrying job_id when inside jobContext.run", async () => {
    const { createLogger, jobContext } = await import("../src/logger.ts");
    const { stream, lines } = captureStream();
    const logger = createLogger(stream);
    const jobId = "11111111-2222-3333-4444-555555555555";

    jobContext.run({ jobId }, () => {
      logger.info("job log line");
    });

    const jobLine = lines.find((l) => l.includes("job log line"));
    expect(jobLine, "expected a log line for the message").toBeDefined();

    const parsed = JSON.parse(jobLine!) as Record<string, unknown>;
    expect(parsed["job_id"]).toBe(jobId);
    expect(parsed["msg"]).toBe("job log line");
  });

  it("does NOT include job_id outside a job context", async () => {
    const { createLogger } = await import("../src/logger.ts");
    const { stream, lines } = captureStream();
    const logger = createLogger(stream);

    logger.info("no-context line");

    const line = lines.find((l) => l.includes("no-context line"));
    expect(line).toBeDefined();
    const parsed = JSON.parse(line!) as Record<string, unknown>;
    expect(parsed["job_id"]).toBeUndefined();
  });

  it("getLogger() returns a stable singleton", async () => {
    const { getLogger } = await import("../src/logger.ts");
    expect(getLogger()).toBe(getLogger());
  });

  it("never logs secrets (T-08-07)", async () => {
    const { createLogger, jobContext } = await import("../src/logger.ts");
    const { stream, lines } = captureStream();
    const logger = createLogger(stream);
    const SECRET = process.env["EXECUTOR_API_TOKEN"]!;

    jobContext.run({ jobId: "abc" }, () => {
      logger.info("admitted job");
    });

    const joined = lines.join("\n");
    expect(joined).not.toContain(SECRET);
  });
});
