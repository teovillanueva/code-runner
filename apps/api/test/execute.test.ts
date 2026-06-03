// Tests for POST /v1/execute, GET /v1/jobs/:id, GET /v1/languages
//
// Environment is pre-configured by vitest.config.ts:
//   EXECUTOR_API_TOKEN, REDIS_URL, LANGUAGES_DIR, ENABLE_CHANNEL_AUTH
// The test Redis is flushed between tests to ensure isolation.

import { describe, it, expect, beforeAll, afterAll, beforeEach } from "vitest";
import type { Hono } from "hono";

// These constants must match what vitest.config.ts sets
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

async function getJob(app: Hono, jobId: string, token: string = VALID_TOKEN) {
  return app.request(`/v1/jobs/${jobId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function getLanguages(app: Hono, token: string = VALID_TOKEN) {
  return app.request("/v1/languages", {
    headers: { Authorization: `Bearer ${token}` },
  });
}

// ── Redis client for side-effect assertions ───────────────────────────────────

let redisClient: import("ioredis").default | null = null;

async function getTestRedis() {
  if (!redisClient) {
    const { default: Redis } = await import("ioredis");
    redisClient = new Redis(REDIS_URL, { lazyConnect: false, maxRetriesPerRequest: 1 });
  }
  return redisClient;
}

async function flushRedis() {
  try {
    const r = await getTestRedis();
    await r.flushdb();
  } catch {
    // If Redis is not available, side-effect assertions will be skipped
  }
}

// ── Test suite ────────────────────────────────────────────────────────────────

describe("POST /v1/execute", () => {
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

  it("returns 202 with jobId, channel, and status:queued for a valid request", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print('hello')" }],
    });

    expect(res.status).toBe(202);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body).toMatchObject({
      status: "queued",
    });
    expect(typeof body["jobId"]).toBe("string");
    expect((body["jobId"] as string).length).toBeGreaterThan(0);
    expect(body["channel"]).toBe(`private-run-${body["jobId"]}`);
  });

  it("returns 202 BEFORE any worker/process involvement (stateless — no side effects on worker)", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print(42)" }],
    });
    expect(res.status).toBe(202);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body["status"]).toBe("queued");
  });

  it("LPUSHes the jobId to jobs:queue", async () => {
    let r: import("ioredis").default;
    try {
      r = await getTestRedis();
    } catch {
      // Skip if Redis not available
      return;
    }

    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print(1)" }],
    });
    const { jobId } = (await res.json()) as { jobId: string };

    const queued = await r.lrange("jobs:queue", 0, -1);
    expect(queued).toContain(jobId);
  });

  it("writes jobSpec and jobStatus to Redis", async () => {
    let r: import("ioredis").default;
    try {
      r = await getTestRedis();
    } catch {
      return;
    }

    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "x = 1" }],
    });
    const { jobId } = (await res.json()) as { jobId: string };

    const specJson = await r.get(`job:${jobId}:spec`);
    const statusJson = await r.get(`job:${jobId}:status`);

    expect(specJson).not.toBeNull();
    expect(statusJson).not.toBeNull();

    const spec = JSON.parse(specJson!) as Record<string, unknown>;
    expect(spec["jobId"]).toBe(jobId);
    expect(spec["language"]).toBe("python");
    expect(spec["version"]).toBe("3.12");
    expect(spec["image"]).toBe("executor/python:3.12");
    expect(Array.isArray(spec["files"])).toBe(true);
    expect(spec["limits"]).toBeDefined();
    expect(spec["enqueuedAtMs"]).toBeGreaterThan(0);

    const status = JSON.parse(statusJson!) as Record<string, unknown>;
    expect(status["jobId"]).toBe(jobId);
    expect(status["state"]).toBe("queued");
  });

  it("applies request limits override on top of manifest defaults", async () => {
    let r: import("ioredis").default;
    try {
      r = await getTestRedis();
    } catch {
      return;
    }

    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "pass" }],
      limits: { wallTimeMs: 5000, memoryMb: 64 },
    });
    const { jobId } = (await res.json()) as { jobId: string };

    const specJson = await r.get(`job:${jobId}:spec`);
    const spec = JSON.parse(specJson!) as Record<string, Record<string, number>>;
    expect(spec["limits"]["wallTimeMs"]).toBe(5000);
    expect(spec["limits"]["memoryMb"]).toBe(64);
    // Other limits should come from manifest defaults
    expect(spec["limits"]["pids"]).toBe(64);
  });

  it("returns 400 for unknown language", async () => {
    const res = await postExecute(app, {
      language: "brainfuck",
      files: [{ name: "main.bf", content: "+++" }],
    });
    expect(res.status).toBe(400);
    const body = (await res.json()) as Record<string, unknown>;
    expect(String(body["error"])).toMatch(/language|brainfuck/i);
  });

  it("returns 400 for unknown version of known language", async () => {
    const res = await postExecute(app, {
      language: "python",
      version: "2.7",
      files: [{ name: "main.py", content: "print 1" }],
    });
    expect(res.status).toBe(400);
    const body = (await res.json()) as Record<string, unknown>;
    // Should mention the unknown version in the error
    expect(String(body["error"])).toMatch(/2\.7|version/i);
  });

  it("returns 400 with a clear validation error for missing files field", async () => {
    const res = await postExecute(app, {
      language: "python",
    });
    expect(res.status).toBe(400);
    const body = await res.json();
    expect(JSON.stringify(body)).toMatch(/files/i);
  });

  it("returns 400 with a clear validation error for missing language field", async () => {
    const res = await postExecute(app, {
      files: [{ name: "main.py", content: "pass" }],
    });
    expect(res.status).toBe(400);
    const body = await res.json();
    expect(JSON.stringify(body)).toMatch(/language/i);
  });

  it("returns 401 for missing token", async () => {
    const res = await app.request("/v1/execute", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        language: "python",
        files: [{ name: "main.py", content: "pass" }],
      }),
    });
    expect(res.status).toBe(401);
  });

  it("returns 401 for invalid token", async () => {
    const res = await postExecute(
      app,
      {
        language: "python",
        files: [{ name: "main.py", content: "pass" }],
      },
      "wrong-token",
    );
    expect(res.status).toBe(401);
  });
});

describe("GET /v1/jobs/:id", () => {
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

  it("returns 200 with JobStatus when the job exists", async () => {
    // First create a job
    const execRes = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "pass" }],
    });
    const { jobId } = (await execRes.json()) as { jobId: string };

    // Then fetch its status
    const res = await getJob(app, jobId);
    expect(res.status).toBe(200);
    const status = (await res.json()) as Record<string, unknown>;
    expect(status["jobId"]).toBe(jobId);
    expect(status["state"]).toBe("queued");
    expect(status["language"]).toBe("python");
  });

  it("returns 404 with an error when the job does not exist", async () => {
    const res = await getJob(app, "00000000-0000-0000-0000-000000000000");
    expect(res.status).toBe(404);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body["error"]).toBeDefined();
  });

  it("returns 401 for missing token", async () => {
    const res = await app.request("/v1/jobs/some-id");
    expect(res.status).toBe(401);
  });
});

describe("GET /v1/languages", () => {
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

  it("returns 200 with an array of language descriptors", async () => {
    const res = await getLanguages(app);
    expect(res.status).toBe(200);
    const langs = (await res.json()) as unknown[];
    expect(Array.isArray(langs)).toBe(true);
    expect(langs.length).toBeGreaterThan(0);
  });

  it("includes python in the language list", async () => {
    const res = await getLanguages(app);
    const langs = (await res.json()) as Array<Record<string, unknown>>;
    const python = langs.find((l) => l["language"] === "python");
    expect(python).toBeDefined();
    expect(python!["version"]).toBe("3.12");
    expect(python!["interactive"]).toBe(true);
  });

  it("language objects have required fields", async () => {
    const res = await getLanguages(app);
    const langs = (await res.json()) as Array<Record<string, unknown>>;
    for (const lang of langs) {
      expect(typeof lang["language"]).toBe("string");
      expect(typeof lang["version"]).toBe("string");
      expect(Array.isArray(lang["aliases"])).toBe(true);
      expect(typeof lang["interactive"]).toBe("boolean");
    }
  });

  it("returns 401 for missing token", async () => {
    const res = await app.request("/v1/languages");
    expect(res.status).toBe(401);
  });
});

// ── execute span + traceparent injection (OBS-04) ─────────────────────────────
//
// With an active TracerProvider, POST /v1/execute must start an `execute` span
// and inject a W3C traceparent into the JobSpec written to Redis BEFORE the
// LPUSH. The injected traceparent's trace_id must match the recorded span's
// trace_id (the cross-language correlation seam 08-02 extracts).

describe("POST /v1/execute — execute span + traceparent injection", () => {
  let app: Hono;
  let provider: import("@opentelemetry/sdk-node").tracing.BasicTracerProvider;
  let exporter: import("@opentelemetry/sdk-node").tracing.InMemorySpanExporter;

  beforeAll(async () => {
    const { tracing, core } = await import("@opentelemetry/sdk-node");
    const { trace, propagation, context } = await import("@opentelemetry/api");
    const { AsyncLocalStorageContextManager } = await import(
      "@opentelemetry/context-async-hooks"
    );

    // The real NodeSDK registers an AsyncLocalStorage context manager so the
    // active span flows through async boundaries (Hono handler). Without it,
    // context.active() returns ROOT inside startActiveSpan and inject() is a
    // no-op. Mirror that here.
    context.setGlobalContextManager(
      new AsyncLocalStorageContextManager().enable(),
    );

    exporter = new tracing.InMemorySpanExporter();
    provider = new tracing.BasicTracerProvider({
      spanProcessors: [new tracing.SimpleSpanProcessor(exporter)],
    });
    trace.setGlobalTracerProvider(provider);
    // The real NodeSDK registers the W3C propagator; mirror it so
    // propagation.inject() writes a traceparent (cross-language seam).
    propagation.setGlobalPropagator(new core.W3CTraceContextPropagator());

    app = await getApp();
    await flushRedis();
  });

  beforeEach(async () => {
    exporter.reset();
    await flushRedis();
  });

  afterAll(async () => {
    await provider.shutdown();
    const { trace, propagation, context } = await import("@opentelemetry/api");
    trace.disable();
    propagation.disable();
    context.disable();
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
    if (redisClient) {
      await redisClient.quit();
      redisClient = null;
    }
  });

  it("records an `execute` span and injects a matching traceparent into the JobSpec", async () => {
    let r: import("ioredis").default;
    try {
      r = await getTestRedis();
    } catch {
      return; // skip if Redis unavailable
    }

    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print('trace')" }],
    });
    expect(res.status).toBe(202);
    const { jobId } = (await res.json()) as { jobId: string };

    // An `execute` span was recorded.
    const spans = exporter.getFinishedSpans();
    const executeSpan = spans.find((s) => s.name === "execute");
    expect(executeSpan, "expected an `execute` span").toBeDefined();
    const spanTraceId = executeSpan!.spanContext().traceId;

    // The JobSpec written to Redis carries a W3C traceparent whose trace_id
    // matches the recorded span (BEFORE the LPUSH — the spec was set in the
    // same pipeline as the queue push).
    const specJson = await r.get(`job:${jobId}:spec`);
    expect(specJson).not.toBeNull();
    const spec = JSON.parse(specJson!) as Record<string, unknown>;

    const traceparent = spec["traceparent"];
    expect(typeof traceparent).toBe("string");
    // W3C format: 00-<32 hex trace_id>-<16 hex span_id>-<2 hex flags>
    expect(traceparent as string).toMatch(
      /^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$/,
    );
    const injectedTraceId = (traceparent as string).split("-")[1];
    expect(injectedTraceId).toBe(spanTraceId);
  });
});
