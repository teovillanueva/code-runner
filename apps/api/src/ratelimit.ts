// Per-job stdin rate-limit + pending-byte cap → 429 (API-10, STACK §1.6).
//
// Two independent guards on the /stdin route:
//
// 1. Frame rate limit — N stdin frames/sec per jobId, Redis-backed (holds
//    across API replicas). Implemented via INCR+EXPIRE on a per-job, per-window
//    counter key.
//
// 2. Pending-byte cap — total un-consumed stdin bytes per job. The API INCRBYs
//    on every publish; the worker DECRBYs as it drains (or the counter expires).
//    Over the cap → 429.
//
// Both limits are Redis-backed so they're correct under horizontal scale.

import type { MiddlewareHandler, Context } from "hono";
import { getRedis } from "./redis.ts";

// Configurable constants — tune empirically per STACK §1.6 guidance.
export const STDIN_RATE_LIMIT_FRAMES = 30; // max stdin frames per window
export const STDIN_RATE_WINDOW_SECS = 1; // window size in seconds
export const STDIN_PENDING_BYTE_CAP = 65_536; // 64 KiB max pending bytes

/**
 * Redis key for the rate-limit counter for a given job within a time window.
 * Window is floored to the nearest STDIN_RATE_WINDOW_SECS interval.
 */
function rateLimitKey(jobId: string): string {
  const window = Math.floor(Date.now() / (STDIN_RATE_WINDOW_SECS * 1000));
  return `job:${jobId}:stdin_rate:${window}`;
}

/**
 * Redis key for the pending-byte counter for a given job.
 */
export function pendingBytesKey(jobId: string): string {
  return `job:${jobId}:stdin_pending`;
}

/**
 * Middleware: enforce per-job stdin rate limit.
 * Reads jobId from the route param `:id`.
 * Returns 429 if the frame rate is exceeded.
 */
export const stdinRateLimit: MiddlewareHandler = async (c: Context, next) => {
  const jobId = c.req.param("id");
  if (!jobId) {
    await next();
    return;
  }

  const redis = getRedis();
  const key = rateLimitKey(jobId);

  const pipeline = redis.pipeline();
  pipeline.incr(key);
  pipeline.expire(key, STDIN_RATE_WINDOW_SECS + 1); // +1s grace
  const results = await pipeline.exec();

  const count = results?.[0]?.[1] as number;
  if (count > STDIN_RATE_LIMIT_FRAMES) {
    return c.json(
      {
        error: "Too many stdin frames",
        retryAfterMs: STDIN_RATE_WINDOW_SECS * 1000,
      },
      429,
    );
  }

  await next();
};

/**
 * Middleware: enforce pending-byte cap on stdin payloads.
 * Reads the chunk from the JSON body and checks/increments the Redis counter.
 * Returns 429 if the cap is exceeded.
 *
 * Must run AFTER body parsing (zValidator).
 */
export const stdinByteCapCheck: MiddlewareHandler = async (
  c: Context,
  next,
) => {
  const jobId = c.req.param("id");
  if (!jobId) {
    await next();
    return;
  }

  // Peek at the raw body size without consuming the parsed body
  const body = await c.req.json().catch(() => null);
  if (!body || typeof body.chunk !== "string") {
    await next();
    return;
  }

  const chunkBytes = Buffer.byteLength(body.chunk, "utf8");
  const redis = getRedis();
  const key = pendingBytesKey(jobId);

  // INCRBY the pending counter; set a TTL the first time (so stale keys expire)
  const pipeline = redis.pipeline();
  pipeline.incrby(key, chunkBytes);
  pipeline.expire(key, 300); // 5 min TTL — cleared by worker or expiry
  const results = await pipeline.exec();

  const total = results?.[0]?.[1] as number;
  if (total > STDIN_PENDING_BYTE_CAP) {
    // Undo the increment so the cap is not permanently inflated on rejected frames
    await redis.decrby(key, chunkBytes);
    return c.json(
      {
        error: "Stdin pending-byte cap exceeded. Wait for the worker to drain.",
        capBytes: STDIN_PENDING_BYTE_CAP,
      },
      429,
    );
  }

  // Stash chunk on context so the route handler doesn't have to re-parse body
  c.set("stdinChunk" as never, body.chunk);
  await next();
};
