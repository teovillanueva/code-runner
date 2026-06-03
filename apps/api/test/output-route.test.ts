// GET /v1/jobs/:id/output route tests — R9 pullable run output.
//
// Mirrors the existing route-test convention (execute-collect-output.test.ts):
// seed the test Redis directly via ioredis, then drive the route through
// app.request with a Bearer header. The route is Redis-only (API-11): it reads
// job:<id>:output and never calls the worker.
//
// Environment is pre-configured by vitest.config.ts:
//   EXECUTOR_API_TOKEN, REDIS_URL, LANGUAGES_DIR, ENABLE_CHANNEL_AUTH
// The test Redis is flushed between tests for isolation. If Redis is
// unavailable, the body-shape side-effect assertions are skipped (mirrors
// execute-collect-output.test.ts); the 401 assertion never needs Redis.

import { describe, it, expect, beforeAll, afterAll, beforeEach } from "vitest";
import type { Hono } from "hono";
import { keys, type RunResult } from "@teovilla/code-runner-contract";

const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;
const REDIS_URL = process.env["REDIS_URL"]!;

// ── Helpers (mirror execute-collect-output.test.ts) ──────────────────────────

async function getApp(): Promise<Hono> {
  const { resetManifests } = await import("../src/manifests.ts");
  resetManifests();
  const { makeApp } = await import("../src/app.ts");
  return makeApp();
}

function getOutput(app: Hono, jobId: string, token: string | null = VALID_TOKEN) {
  const headers: Record<string, string> = {};
  if (token !== null) {
    headers["Authorization"] = `Bearer ${token}`;
  }
  return app.request(`/v1/jobs/${jobId}/output`, { method: "GET", headers });
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

async function flushRedis() {
  try {
    const r = await getTestRedis();
    await r.flushdb();
  } catch {
    // Redis unavailable — body-shape assertions will be skipped.
  }
}

/** Seed a value at job:<id>:output. Returns false if Redis is unavailable. */
async function seedOutput(jobId: string, value: string): Promise<boolean> {
  let r: import("ioredis").default;
  try {
    r = await getTestRedis();
  } catch {
    return false;
  }
  await r.set(keys.jobOutput(jobId), value);
  return true;
}

const sampleResult: RunResult = {
  exitCode: 0,
  signal: null,
  timedOut: false,
  idleTimedOut: false,
  truncated: false,
  durationMs: 42,
  stdout: "hello\n",
  stderr: "",
  artifacts: [],
  artifactsTruncated: false,
};

// ── Test suite ───────────────────────────────────────────────────────────────

describe("GET /v1/jobs/:id/output — pullable run output (R9)", () => {
  let app: Hono;

  beforeAll(async () => {
    app = await getApp();
    await flushRedis();
  });

  beforeEach(async () => {
    await flushRedis();
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
    if (redisClient) {
      await redisClient.quit();
      redisClient = null;
    }
  });

  it("returns 200 with the persisted RunResult for a collected job", async () => {
    const seeded = await seedOutput("job1", JSON.stringify(sampleResult));
    if (!seeded) return; // Redis unavailable — skip

    const res = await getOutput(app, "job1");
    expect(res.status).toBe(200);
    const body = (await res.json()) as RunResult;
    expect(body).toEqual(sampleResult);
  });

  it("returns 404 when the output key is absent (unknown / not-collected / expired)", async () => {
    // No seeding — the key is absent. Covers unknown id, non-collected job,
    // and a job past its result TTL: all collapse to "absent".
    const res = await getOutput(app, "does-not-exist");
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error: string };
    expect(body.error).toContain("does-not-exist");
  });

  it("returns 401 without a bearer token (central /v1/* middleware)", async () => {
    const res = await getOutput(app, "job1", null);
    expect(res.status).toBe(401);
  });

  it("returns 401 with an invalid bearer token", async () => {
    const res = await getOutput(app, "job1", "definitely-wrong-token");
    expect(res.status).toBe(401);
  });

  it("returns 500 for a malformed (non-JSON) stored value", async () => {
    const seeded = await seedOutput("job-bad", "{not valid json");
    if (!seeded) return; // Redis unavailable — skip

    const res = await getOutput(app, "job-bad");
    expect(res.status).toBe(500);
  });
});
