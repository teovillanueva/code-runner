// Job-admission gate: global backpressure via queue-depth check (SCALE-03).
//
// POST /v1/execute calls atCapacity() BEFORE building the spec or enqueuing.
// If the job queue is already at or beyond the configured depth ceiling, the
// request is rejected with a 429 carrying a clear "retry shortly" message.
//
// This gate is DISTINCT from the per-job stdin rate-limit in ratelimit.ts:
//   - Admission 429 → /execute, triggered by LLEN(jobs:queue) >= maxQueueDepth
//   - Stdin 429     → /stdin,   triggered by frame rate or pending-byte cap
//
// Why queue depth (not slot count) as the authoritative gate?
//   Queue depth is visible across API replicas with a single O(1) LLEN read.
//   It measures unbounded growth independently of whether workers are draining;
//   a full queue means new work will wait indefinitely — better to reject clearly.
//   If a capacityFree counter (e.g. from 05-01) is present in Redis, it MAY
//   supplement this check, but queue depth is the authoritative MVP gate here.

import { keys } from "@teovilla/code-runner-contract";
import { getRedis } from "./redis.ts";
import { config } from "./config.ts";
import { admissionRejections } from "./metrics.ts";

/**
 * Returns true when the job queue has reached or exceeded the configured
 * maximum depth and new jobs should be rejected.
 *
 * Uses a single LLEN read — safe to call on every POST /v1/execute; the
 * Redis round-trip is O(1) regardless of queue length.
 */
export async function atCapacity(): Promise<boolean> {
  const depth = await getRedis().llen(keys.jobQueue);
  return depth >= config.maxQueueDepth;
}

/**
 * 429 JSON response body for over-capacity admission rejections.
 * Mirrors the shape used by ratelimit.ts (error + retryAfterMs) for consistency.
 *
 * @param depth  Current queue depth (informational, non-sensitive).
 * @param cap    Configured maximum queue depth.
 */
export function admissionError(
  depth: number,
  cap: number,
): { error: string; retryAfterMs: number } {
  // Emit the rejection metric here — this helper is invoked ONLY on the 429
  // admission-rejection path, so the counter increments exactly once per
  // rejected request without touching execute.ts (08-03's file). No attributes:
  // admission rejection is a single dimension (no job_id — cardinality contract).
  admissionRejections.add(1);
  return {
    error: `Executor at capacity (queue depth ${depth} ≥ ${cap}). Retry shortly.`,
    retryAfterMs: 1000,
  };
}
