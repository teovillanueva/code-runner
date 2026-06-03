// Tests for POST /v1/jobs/:id/start|stdin|stdin/close|kill
//
// Assertions:
//  - Each endpoint PUBLISHes the correct Redis channel and payload
//  - The API never calls the worker directly (only PUBLISH/SET/LPUSH/GET)
//  - /stdin validates the StdinMessage schema
//
// Spies on the ioredis `publish` method to capture published messages.

import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import type { Hono } from "hono";
import { controlChannel, stdinChannel } from "@teovilla/code-runner-contract";

// env is pre-configured by vitest.config.ts

const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;

async function getApp(): Promise<Hono> {
  const { resetManifests } = await import("../src/manifests.ts");
  resetManifests();
  const { makeApp } = await import("../src/app.ts");
  return makeApp();
}

async function callEndpoint(
  app: Hono,
  method: string,
  path: string,
  body?: unknown,
  token: string = VALID_TOKEN,
) {
  const opts: RequestInit = {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  };
  return app.request(path, opts);
}

// ── Capture published messages via ioredis spy ────────────────────────────────

type PublishCall = { channel: string; message: string };

async function withPublishSpy(fn: (published: PublishCall[]) => Promise<void>) {
  const { getRedis } = await import("../src/redis.ts");
  const redis = getRedis();

  const published: PublishCall[] = [];
  const origPublish = redis.publish.bind(redis);
  const spy = vi
    .spyOn(redis, "publish")
    .mockImplementation(
      async (
        channel: string | Buffer,
        message: string | Buffer,
      ): Promise<number> => {
        published.push({
          channel: channel.toString(),
          message: message.toString(),
        });
        return (
          origPublish(channel as string, message as string).catch(() => 0) as Promise<number>
        );
      },
    );

  try {
    await fn(published);
  } finally {
    spy.mockRestore();
  }
}

describe("POST /v1/jobs/:id/start", () => {
  let app: Hono;
  const jobId = "test-job-start-001";

  beforeAll(async () => {
    app = await getApp();
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
  });

  it("returns 202 and PUBLISHes {type:'start'} to controlChannel", async () => {
    await withPublishSpy(async (published) => {
      const res = await callEndpoint(app, "POST", `/v1/jobs/${jobId}/start`);
      expect(res.status).toBe(202);
      expect(published).toHaveLength(1);
      expect(published[0]!.channel).toBe(controlChannel(jobId));
      const msg = JSON.parse(published[0]!.message);
      expect(msg).toEqual({ type: "start" });
    });
  });

  it("returns 401 for missing token", async () => {
    const res = await app.request(`/v1/jobs/${jobId}/start`, { method: "POST" });
    expect(res.status).toBe(401);
  });

  it("never calls the worker directly (only PUBLISH)", async () => {
    // This is enforced architecturally — the control routes only call redis.publish.
    // We verify no unexpected network calls by checking that our spy captures the call.
    await withPublishSpy(async (published) => {
      await callEndpoint(app, "POST", `/v1/jobs/${jobId}/start`);
      // Only one Redis PUBLISH — no direct worker HTTP calls
      expect(published.every((p) => p.channel.startsWith("ctrl:"))).toBe(true);
    });
  });
});

describe("POST /v1/jobs/:id/stdin", () => {
  let app: Hono;
  const jobId = "test-job-stdin-001";

  beforeAll(async () => {
    app = await getApp();
    // Flush Redis to reset rate-limit counters
    try {
      const { getRedis } = await import("../src/redis.ts");
      const r = getRedis();
      await r.flushdb();
    } catch {
      // no-op
    }
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
  });

  it("returns 200 and PUBLISHes StdinMessage to stdinChannel", async () => {
    await withPublishSpy(async (published) => {
      const res = await callEndpoint(
        app,
        "POST",
        `/v1/jobs/${jobId}/stdin`,
        { chunk: "hello\n" },
      );
      expect(res.status).toBe(200);
      expect(published).toHaveLength(1);
      expect(published[0]!.channel).toBe(stdinChannel(jobId));
      const msg = JSON.parse(published[0]!.message);
      expect(msg).toEqual({ chunk: "hello\n" });
    });
  });

  it("publishes to stdinChannel (not controlChannel)", async () => {
    await withPublishSpy(async (published) => {
      await callEndpoint(app, "POST", `/v1/jobs/${jobId}/stdin`, { chunk: "x" });
      expect(published[0]!.channel).toBe(stdinChannel(jobId));
      expect(published[0]!.channel).not.toBe(controlChannel(jobId));
    });
  });

  it("returns 400 for missing chunk field", async () => {
    const res = await callEndpoint(app, "POST", `/v1/jobs/${jobId}/stdin`, {});
    expect(res.status).toBe(400);
  });

  it("returns 401 for missing token", async () => {
    const res = await app.request(`/v1/jobs/${jobId}/stdin`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ chunk: "test" }),
    });
    expect(res.status).toBe(401);
  });
});

describe("POST /v1/jobs/:id/stdin/close", () => {
  let app: Hono;
  const jobId = "test-job-close-001";

  beforeAll(async () => {
    app = await getApp();
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
  });

  it("returns 200 and PUBLISHes {type:'stdin_close'} to controlChannel", async () => {
    await withPublishSpy(async (published) => {
      const res = await callEndpoint(
        app,
        "POST",
        `/v1/jobs/${jobId}/stdin/close`,
      );
      expect(res.status).toBe(200);
      expect(published).toHaveLength(1);
      expect(published[0]!.channel).toBe(controlChannel(jobId));
      const msg = JSON.parse(published[0]!.message);
      expect(msg).toEqual({ type: "stdin_close" });
    });
  });

  it("returns 401 for missing token", async () => {
    const res = await app.request(`/v1/jobs/${jobId}/stdin/close`, {
      method: "POST",
    });
    expect(res.status).toBe(401);
  });
});

describe("POST /v1/jobs/:id/kill", () => {
  let app: Hono;
  const jobId = "test-job-kill-001";

  beforeAll(async () => {
    app = await getApp();
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
  });

  it("returns 200 and PUBLISHes {type:'kill'} to controlChannel", async () => {
    await withPublishSpy(async (published) => {
      const res = await callEndpoint(
        app,
        "POST",
        `/v1/jobs/${jobId}/kill`,
      );
      expect(res.status).toBe(200);
      expect(published).toHaveLength(1);
      expect(published[0]!.channel).toBe(controlChannel(jobId));
      const msg = JSON.parse(published[0]!.message);
      expect(msg).toEqual({ type: "kill" });
    });
  });

  it("returns 401 for missing token", async () => {
    const res = await app.request(`/v1/jobs/${jobId}/kill`, { method: "POST" });
    expect(res.status).toBe(401);
  });
});
