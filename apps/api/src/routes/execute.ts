// POST /v1/execute
//
// Validates the request body against the generated ExecuteRequestSchema,
// resolves the manifest, builds a full JobSpec, writes spec+status to Redis,
// LPUSHes the jobId to the job queue, and returns 202 with the jobId+channel.
//
// CRITICAL: this endpoint returns 202 BEFORE any worker/process starts.
// The worker parks until POST /v1/jobs/:id/start is called (start-handshake,
// API-01/SESS-01). The API NEVER calls the worker directly (API-11).

import { Hono } from "hono";
import { zValidator } from "@hono/zod-validator";
import { randomUUID } from "node:crypto";
import { context, propagation, trace } from "@opentelemetry/api";
import {
  ExecuteRequestSchema,
  channelForJob,
  keys,
  type JobSpec,
  type JobStatus,
} from "@teovilla/code-runner-contract";
import { getRedis } from "../redis.ts";
import { getManifests, resolveManifest } from "../manifests.ts";
import { atCapacity, admissionError } from "../admission.ts";
import { config } from "../config.ts";
import { validateFiles } from "../files.ts";
import { jobContext } from "../logger.ts";
import type { LimitsOverride, Limits } from "@teovilla/code-runner-contract";

// Tracer for the API's manual `execute` span (OBS-04). When no SDK is active
// (OTEL unset) this is a no-op tracer: startActiveSpan still runs the body and
// propagation.inject writes nothing meaningful — spec.traceparent stays absent
// and enqueue behaves exactly as today (telemetry is strictly additive).
const tracer = trace.getTracer("code-runner-api");

/**
 * Merge manifest defaultLimits with optional per-request overrides (LANG-04).
 * Overrides are applied field-by-field; absent fields fall back to manifest defaults.
 */
function mergeLimits(
  defaults: Limits,
  overrides: LimitsOverride | undefined,
): Limits {
  if (!overrides) return { ...defaults };
  return {
    wallTimeMs: overrides.wallTimeMs ?? defaults.wallTimeMs,
    idleMs: overrides.idleMs ?? defaults.idleMs,
    cpuMs: overrides.cpuMs ?? defaults.cpuMs,
    memoryMb: overrides.memoryMb ?? defaults.memoryMb,
    pids: overrides.pids ?? defaults.pids,
    outputKb: overrides.outputKb ?? defaults.outputKb,
    maxArtifacts: overrides.maxArtifacts ?? defaults.maxArtifacts,
    maxArtifactBytes: overrides.maxArtifactBytes ?? defaults.maxArtifactBytes,
  };
}

export function registerExecuteRoutes(app: Hono): void {
  // POST /v1/execute — validate with generated zod schema (never hand-written)
  app.post(
    "/v1/execute",
    zValidator("json", ExecuteRequestSchema, (result, c) => {
      if (!result.success) {
        return c.json(
          {
            error: "Validation error",
            details: result.error.issues.map((i) => ({
              path: i.path.join("."),
              message: i.message,
            })),
          },
          400,
        );
      }
    }),
    async (c) => {
      const req = c.req.valid("json");

      // Resolve manifest → or return 400 with a clear error (API-09)
      let manifest: Awaited<ReturnType<typeof resolveManifest>>;
      try {
        const manifests = await getManifests();
        manifest = resolveManifest(manifests, req.language, req.version);
      } catch (err) {
        const msg = (err as Error).message ?? "Unknown error resolving language";
        // Distinguish: "version ... not found" vs "language ... not found"
        // resolveManifest throws: `language "${lang}" not found` or
        //                         `language "${lang}" version "${ver}" not found`
        if (req.version && msg.includes("version")) {
          return c.json(
            {
              error: `Unknown version "${req.version}" for language "${req.language}". Check GET /v1/languages.`,
            },
            400,
          );
        }
        return c.json(
          {
            error: `Unknown language "${req.language}". Check GET /v1/languages.`,
          },
          400,
        );
      }

      // File-input validation (FILES-06/07). AFTER manifest resolution so an
      // invalid request gets 400/413, and BEFORE the capacity check so a
      // malformed body never returns 429 (400/413-before-429 ordering). The
      // worker re-sanitizes every path regardless — this is a fast caller-
      // facing rejection, not a trust boundary.
      const fileCheck = validateFiles(req.files);
      if (fileCheck.error) {
        const { kind, message } = fileCheck.error;
        return c.json(
          {
            error:
              kind === "base64"
                ? `Invalid file content: ${message}`
                : `Invalid file path: ${message}`,
          },
          400,
        );
      }
      if (fileCheck.totalBytes > config.maxFilesBytes) {
        return c.json(
          {
            error: `Input files too large: ${fileCheck.totalBytes} decoded bytes exceeds the ${config.maxFilesBytes}-byte limit (MAX_FILES_BYTES).`,
          },
          413,
        );
      }

      // Job-admission backpressure: reject before writing anything to Redis (SCALE-03).
      // Check AFTER manifest resolution so invalid requests get 400, not 429.
      // The spec/status pipeline MUST NOT run on a rejected request.
      if (await atCapacity()) {
        const depth = await getRedis().llen(keys.jobQueue);
        return c.json(admissionError(depth, config.maxQueueDepth), 429);
      }

      const jobId = randomUUID();

      // Run the spec-build + enqueue inside a job context (so any pino log
      // carries job_id) and an `execute` span (so the worker's spans share its
      // trace_id via the injected traceparent — OBS-04/OBS-07).
      return jobContext.run({ jobId }, () =>
        tracer.startActiveSpan("execute", async (span) => {
          try {
            const channel = channelForJob(jobId);
            const enqueuedAtMs = Date.now();

            // Build the full JobSpec (the worker must decode this as-is).
            const spec: JobSpec = {
              jobId,
              channel,
              language: manifest.language,
              version: manifest.version,
              image: manifest.image,
              entrypoint: manifest.entrypoint,
              compile: manifest.compile,
              run: manifest.run,
              interactive: manifest.interactive,
              files: req.files as JobSpec["files"],
              limits: mergeLimits(manifest.defaultLimits, req.limits),
              enqueuedAtMs,
              // Opt-in output/artifact collection. Always persist an explicit
              // boolean so the Go worker's spec.CollectOutput read is unambiguous
              // across the JSON seam (an absent field would be ambiguous).
              collectOutput: req.collectOutput ?? false,
            };

            // Inject the W3C traceparent into the JobSpec BEFORE the LPUSH so
            // the worker (08-02) extracts it and links its spans to this trace.
            // No-op when no SDK is active (carrier stays empty).
            const carrier: Record<string, string> = {};
            propagation.inject(context.active(), carrier);
            if (carrier["traceparent"]) spec.traceparent = carrier["traceparent"];
            if (carrier["tracestate"]) spec.tracestate = carrier["tracestate"];

            // Initial job status
            const status: JobStatus = {
              jobId,
              channel,
              language: manifest.language,
              version: manifest.version,
              state: "queued",
              updatedAtMs: enqueuedAtMs,
            };

            // Atomically write spec, status, and enqueue the job via pipeline
            const redis = getRedis();
            const pipeline = redis.pipeline();
            pipeline.set(keys.jobSpec(jobId), JSON.stringify(spec));
            pipeline.set(keys.jobStatus(jobId), JSON.stringify(status));
            pipeline.lpush(keys.jobQueue, jobId);
            await pipeline.exec();

            // Return 202 BEFORE any process starts (start-handshake, API-01)
            return c.json(
              {
                jobId,
                channel,
                status: "queued" as const,
              },
              202,
            );
          } finally {
            span.end();
          }
        }),
      );
    },
  );
}
