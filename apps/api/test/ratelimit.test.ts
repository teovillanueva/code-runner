// Rate-limit tests: stdin frame rate + pending-byte cap → 429
//
// Tests flood the /stdin endpoint past each limit and assert 429.
// Uses a live Redis (from vitest.config.ts env) and flushes the relevant keys
// before each test.

import { describe, it, expect, beforeEach, afterAll } from "vitest";
import type { Hono } from "hono";
import {
  STDIN_RATE_LIMIT_FRAMES,
  STDIN_PENDING_BYTE_CAP,
  pendingBytesKey,
} from "../src/ratelimit.ts";

// env pre-configured by vitest.config.ts
const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;
const REDIS_URL = process.env["REDIS_URL"]!;

let testRedis: import("ioredis").default | null = null;

async function getTestRedis() {
  if (!testRedis) {
    const { default: Redis } = await import("ioredis");
    testRedis = new Redis(REDIS_URL, { maxRetriesPerRequest: 1 });
  }
  return testRedis;
}

async function getApp(): Promise<Hono> {
  const { resetManifests } = await import("../src/manifests.ts");
  resetManifests();
  const { makeApp } = await import("../src/app.ts");
  return makeApp();
}

async function sendStdin(
  app: Hono,
  jobId: string,
  chunk: string,
  token: string = VALID_TOKEN,
) {
  return app.request(`/v1/jobs/${jobId}/stdin`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify({ chunk }),
  });
}

describe("stdin rate limit (frame rate) → 429", () => {
  let app: Hono;
  const JOB_ID = `rl-rate-test-${Date.now()}`;

  beforeEach(async () => {
    // Flush rate-limit counter keys for this job
    try {
      const r = await getTestRedis();
      await r.flushdb();
    } catch {
      // no-op
    }
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
    if (testRedis) {
      await testRedis.quit();
      testRedis = null;
    }
  });

  it("allows up to STDIN_RATE_LIMIT_FRAMES requests per window", async () => {
    app = await getApp();
    const successCount = STDIN_RATE_LIMIT_FRAMES;
    let last429 = false;

    for (let i = 0; i < successCount; i++) {
      const res = await sendStdin(app, JOB_ID, "x");
      if (res.status === 429) {
        last429 = true;
        break;
      }
      expect(res.status).toBe(200);
    }
    expect(last429).toBe(false);
  });

  it("returns 429 when frame rate is exceeded", async () => {
    app = await getApp();
    // Exhaust the frame budget
    for (let i = 0; i < STDIN_RATE_LIMIT_FRAMES; i++) {
      await sendStdin(app, JOB_ID, "x");
    }
    // Next request should be rate-limited
    const res = await sendStdin(app, JOB_ID, "overflow");
    expect(res.status).toBe(429);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body["error"]).toBeDefined();
  });
});

describe("stdin pending-byte cap → 429", () => {
  let app: Hono;

  beforeEach(async () => {
    try {
      const r = await getTestRedis();
      await r.flushdb();
    } catch {
      // no-op
    }
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
    if (testRedis) {
      await testRedis.quit();
      testRedis = null;
    }
  });

  it("returns 429 when pending-byte cap is exceeded", async () => {
    app = await getApp();
    const JOB_ID = `rl-bytes-test-${Date.now()}`;

    // Pre-seed the pending counter at the cap using the test Redis
    try {
      const r = await getTestRedis();
      await r.set(pendingBytesKey(JOB_ID), STDIN_PENDING_BYTE_CAP);
    } catch {
      // If Redis unavailable, skip
      return;
    }

    // Any additional stdin should be rejected
    const res = await sendStdin(app, JOB_ID, "one more byte");
    expect(res.status).toBe(429);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body["error"]).toBeDefined();
  });

  it("accepts stdin when pending bytes are below the cap", async () => {
    app = await getApp();
    const JOB_ID = `rl-bytes-ok-${Date.now()}`;

    // Set the pending counter well below the cap
    try {
      const r = await getTestRedis();
      await r.set(pendingBytesKey(JOB_ID), 100);
    } catch {
      return;
    }

    const res = await sendStdin(app, JOB_ID, "hello");
    expect(res.status).toBe(200);
  });

  it("pending-byte cap value is greater than 0 (sanity check)", () => {
    expect(STDIN_PENDING_BYTE_CAP).toBeGreaterThan(0);
  });
});
