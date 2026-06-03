// Tests for job-admission 429 gate (SCALE-03)
//
// Verifies that POST /v1/execute returns 429 with a clear "at capacity" message
// when LLEN(jobs:queue) >= MAX_QUEUE_DEPTH, and 202 when below capacity.
//
// This 429 is DISTINCT from the per-job stdin rate-limit 429 in ratelimit.ts:
//   - Admission 429: triggered by queue depth at /execute (queue-depth gate)
//   - Stdin 429: triggered by frame rate or pending-byte cap at /stdin
//
// Environment is pre-configured by vitest.config.ts:
//   EXECUTOR_API_TOKEN, REDIS_URL, LANGUAGES_DIR

import { describe, it, expect, beforeAll, afterAll, beforeEach } from "vitest";
import type { Hono } from "hono";
import { keys } from "@teovilla/code-runner-contract";

const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;
const REDIS_URL = process.env["REDIS_URL"]!;

// ── Helpers ──────────────────────────────────────────────────────────────────

async function getApp(): Promise<Hono> {
  const { resetManifests } = await import("../src/manifests.ts");
  resetManifests();
  const { makeApp } = await import("../src/app.ts");
  return makeApp();
}

async function postExecute(
  app: Hono,
  body: unknown,
  token: string = VALID_TOKEN,
) {
  return app.request("/v1/execute", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify(body),
  });
}

// ── Redis client for side-effect assertions ───────────────────────────────────

let redisClient: import("ioredis").default | null = null;

async function getTestRedis() {
  if (!redisClient) {
    const { default: Redis } = await import("ioredis");
    redisClient = new Redis(REDIS_URL, {
      lazyConnect: false,
      maxRetriesPerRequest: 1,
    });
  }
  return redisClient;
}

async function flushTestRedis() {
  try {
    const r = await getTestRedis();
    await r.flushdb();
  } catch {
    // If Redis not available, side-effect assertions will be skipped
  }
}

// ── Test suite ────────────────────────────────────────────────────────────────

describe("POST /v1/execute — admission gate (queue-depth 429)", () => {
  let app: Hono;

  beforeAll(async () => {
    app = await getApp();
    await flushTestRedis();
  });

  beforeEach(async () => {
    await flushTestRedis();
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
    if (redisClient) {
      await redisClient.quit();
      redisClient = null;
    }
  });

  it("returns 429 with a capacity error message when queue is at capacity", async () => {
    // Seed the queue with maxQueueDepth dummy job IDs so it is exactly at capacity
    const { config } = await import("../src/config.ts");
    const cap = config.maxQueueDepth;

    const r = await getTestRedis();

    // RPUSH dummy ids — LLEN will equal cap, triggering the gate.
    // Push in batches of 100 to avoid argument limits.
    for (let i = 0; i < cap; i += 100) {
      const batch = Array.from(
        { length: Math.min(100, cap - i) },
        (_, j) => `dummy-job-${i + j}`,
      );
      await r.rpush(keys.jobQueue, ...batch);
    }

    const lenBefore = await r.llen(keys.jobQueue);
    expect(lenBefore).toBe(cap);

    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print('hi')" }],
    });

    expect(res.status).toBe(429);
    const body = (await res.json()) as Record<string, unknown>;
    // Message must mention capacity and include retryAfterMs
    expect(String(body["error"])).toMatch(/capacity/i);
    expect(typeof body["retryAfterMs"]).toBe("number");

    // Queue MUST NOT have grown (no enqueue on rejected request)
    const lenAfter = await r.llen(keys.jobQueue);
    expect(lenAfter).toBe(cap);
  });

  it("does NOT write spec or status to Redis for a rejected (over-capacity) request", async () => {
    const { config } = await import("../src/config.ts");
    const cap = config.maxQueueDepth;

    const r = await getTestRedis();

    // Fill queue to capacity — push in batches to avoid argument limits
    for (let i = 0; i < cap; i += 100) {
      const batch = Array.from(
        { length: Math.min(100, cap - i) },
        (_, j) => `dummy-spec-${i + j}`,
      );
      await r.rpush(keys.jobQueue, ...batch);
    }

    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "x = 1" }],
    });

    expect(res.status).toBe(429);

    // No new spec or status keys should exist (beyond the seeded dummy ids)
    // Scan for job:*:spec keys that are NOT from our dummy set
    const specKeys = await r.keys("job:*:spec");
    expect(specKeys.length).toBe(0);

    const statusKeys = await r.keys("job:*:status");
    expect(statusKeys.length).toBe(0);
  });

  it("returns 202 and enqueues when queue is below capacity", async () => {
    // Queue is empty (flushed in beforeEach) — below any positive cap
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print('ok')" }],
    });

    expect(res.status).toBe(202);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body["status"]).toBe("queued");
    expect(typeof body["jobId"]).toBe("string");
    expect((body["jobId"] as string).length).toBeGreaterThan(0);

    // Queue should have grown by 1
    const r = await getTestRedis();
    const queueLen = await r.llen(keys.jobQueue);
    expect(queueLen).toBe(1);

    // Job should be in the queue
    const queued = await r.lrange(keys.jobQueue, 0, -1);
    expect(queued).toContain(body["jobId"]);
  });

  it("is distinct from the stdin rate-limit 429 (different route + trigger)", async () => {
    // Admission gate is on /execute (queue depth); stdin 429 is on /stdin (frame rate/bytes).
    // Verify that a normal empty-queue execute returns 202, not a stdin-related error.
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print(42)" }],
    });
    expect(res.status).toBe(202);

    const body = (await res.json()) as Record<string, unknown>;
    // The 202 response must have jobId, not an error about stdin
    expect(body["jobId"]).toBeDefined();
    expect(body["error"]).toBeUndefined();
  });
});
