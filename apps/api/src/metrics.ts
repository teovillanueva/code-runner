// API rejection counters (OBS-06 / D-05 / D-06).
//
// Small named-export module mirroring admission.ts: a module-level meter from
// the OTel API plus the two rejection counters. The meter is obtained from the
// global MeterProvider — a true no-op when telemetry.ts did not install a real
// provider (OBS-01): with no provider the API's default no-op meter records
// nothing and never connects anywhere.
//
//   code_runner.admission.rejected — queue-depth admission gate (429 on /execute)
//   code_runner.ratelimit.rejected — per-job stdin guards (429 on /stdin),
//     carrying a low-cardinality `reason` attribute (`frame_rate` | `byte_cap`).
//
// Cardinality contract (T-08-10b / RESEARCH anti-pattern): metric attributes
// stay low-cardinality. `reason` is a small fixed enum; `job_id` (or any other
// unbounded string) is NEVER attached to a metric point. Counter values are
// the only per-job-correlatable signal we emit — correlation lives on traces
// and logs, not on metric attributes.

import { metrics } from "@opentelemetry/api";

const meter = metrics.getMeter("code-runner-api");

/**
 * Counter: a request was rejected by the queue-depth admission gate (429).
 * Incremented inside admission.ts's `admissionError()` helper (the only call
 * site is the 429 rejection path), so callers/execute.ts stay unchanged.
 * No attributes — admission rejections are a single dimension.
 */
export const admissionRejections = meter.createCounter(
  "code_runner.admission.rejected",
  {
    description:
      "Requests rejected by the queue-depth admission gate (429 on POST /v1/execute).",
    unit: "{request}",
  },
);

/**
 * Counter: a per-job stdin frame was rejected by a rate-limit guard (429).
 * Increment with a low-cardinality `reason` attribute:
 *   { reason: "frame_rate" }  — stdin frame-rate window exceeded
 *   { reason: "byte_cap" }    — pending-byte cap exceeded
 * Never attach `job_id` or any unbounded value (cardinality contract).
 */
export const ratelimitRejections = meter.createCounter(
  "code_runner.ratelimit.rejected",
  {
    description:
      "Per-job stdin frames rejected by a rate-limit guard (429 on POST /v1/jobs/:id/stdin).",
    unit: "{request}",
  },
);

/** Low-cardinality `reason` values for the ratelimit rejection counter. */
export type RatelimitReason = "frame_rate" | "byte_cap";
