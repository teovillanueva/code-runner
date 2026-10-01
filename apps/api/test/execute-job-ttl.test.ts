// Tests that POST /v1/execute gives the per-job spec and status keys an expiry
// (JOB_TTL). Before this, every job left two keys in Redis forever; the store
// filled up in production (2026-09-05, ~339k keys) and, being `noeviction`,
// rejected every write.
//
// Environment is pre-configured by vitest.config.ts:
//   EXECUTOR_API_TOKEN, REDIS_URL, LANGUAGES_DIR, ENABLE_CHANNEL_AUTH
// If Redis is unavailable, the side-effect assertions are skipped (mirrors
// execute-collect-output.test.ts).

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import type { Hono } from "hono";

const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;
const REDIS_URL = process.env["REDIS_URL"]!;

async function getApp(): Promise<Hono> {
  const { resetManifests } = await import("../src/manifests.ts");
  resetManifests();
  const { makeApp } = await import("../src/app.ts");
  return makeApp();
}

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

async function ttlOf(key: string): Promise<number | null> {
  try {
    const r = await getTestRedis();
    return await r.ttl(key);
  } catch {
    return null; // Redis unavailable — skip side-effect assertion
  }
}

describe("POST /v1/execute — job:<id>:spec/status carry a TTL (JOB_TTL)", () => {
  let app: Hono;

  beforeAll(async () => {
    app = await getApp();
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
    if (redisClient) {
      await redisClient.quit();
      redisClient = null;
    }
  });

  it("expires both keys within the configured JOB_TTL (default 3600s)", async () => {
    const res = await app.request("/v1/execute", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${VALID_TOKEN}`,
      },
      body: JSON.stringify({
        language: "python",
        files: [{ name: "main.py", content: "print('ttl')" }],
      }),
    });
    expect(res.status).toBe(202);
    const { jobId } = (await res.json()) as { jobId: string };

    const { config } = await import("../src/config.ts");
    for (const key of [`job:${jobId}:spec`, `job:${jobId}:status`]) {
      const ttl = await ttlOf(key);
      if (ttl === null) return; // Redis unavailable
      // -1 = exists without expiry (the bug), -2 = missing.
      expect(ttl).toBeGreaterThan(0);
      expect(ttl).toBeLessThanOrEqual(config.jobTtlSeconds);
    }
  });
});
