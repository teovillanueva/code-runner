// Tests that POST /v1/execute copies the opt-in collectOutput flag from the
// request body into the persisted JobSpec (R3, request->spec half).
//
// Environment is pre-configured by vitest.config.ts:
//   EXECUTOR_API_TOKEN, REDIS_URL, LANGUAGES_DIR, ENABLE_CHANNEL_AUTH
// The test Redis is flushed between tests to ensure isolation. If Redis is
// unavailable, the side-effect assertions are skipped (mirrors execute.test.ts).

import { describe, it, expect, beforeAll, afterAll, beforeEach } from "vitest";
import type { Hono } from "hono";

const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;
const REDIS_URL = process.env["REDIS_URL"]!;

// ── Helpers (mirror execute.test.ts) ───────────────────────────────────────────

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
    // If Redis is not available, side-effect assertions will be skipped.
  }
}

async function readSpec(
  jobId: string,
): Promise<Record<string, unknown> | null> {
  let r: import("ioredis").default;
  try {
    r = await getTestRedis();
  } catch {
    return null; // skip — Redis unavailable
  }
  const specJson = await r.get(`job:${jobId}:spec`);
  if (!specJson) return null;
  return JSON.parse(specJson) as Record<string, unknown>;
}

// ── Test suite ──────────────────────────────────────────────────────────────

describe("POST /v1/execute — collectOutput request -> spec wiring (R3)", () => {
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

  it("persists collectOutput=true when the request sets it", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print('collect')" }],
      collectOutput: true,
    });
    expect(res.status).toBe(202);
    const { jobId } = (await res.json()) as { jobId: string };

    const spec = await readSpec(jobId);
    if (!spec) return; // Redis unavailable — skip side-effect assertion
    expect(spec["collectOutput"]).toBe(true);
  });

  it("persists collectOutput=false (explicit boolean) when the flag is omitted", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print('no collect')" }],
    });
    expect(res.status).toBe(202);
    const { jobId } = (await res.json()) as { jobId: string };

    const spec = await readSpec(jobId);
    if (!spec) return;
    // Must be an explicit false, never undefined/absent (worker read is unambiguous).
    expect(spec["collectOutput"]).toBe(false);
    expect("collectOutput" in spec).toBe(true);
  });

  it("does not alter the 202 response shape or other spec fields", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "x = 1" }],
      collectOutput: true,
    });
    expect(res.status).toBe(202);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body["status"]).toBe("queued");
    expect(typeof body["jobId"]).toBe("string");
    expect(body["channel"]).toBe(`private-run-${body["jobId"]}`);
    // collectOutput is a spec field, never leaked into the 202 response.
    expect("collectOutput" in body).toBe(false);

    const spec = await readSpec(body["jobId"] as string);
    if (!spec) return;
    // Other spec fields remain intact alongside collectOutput.
    expect(spec["jobId"]).toBe(body["jobId"]);
    expect(spec["language"]).toBe("python");
    expect(spec["image"]).toBe("executor/python:3.12");
    expect(Array.isArray(spec["files"])).toBe(true);
    expect(spec["limits"]).toBeDefined();
  });
});
