// Tests for multi-file input validation on POST /v1/execute (FILES-06/07):
//   - 413 when total decoded bytes exceed MAX_FILES_BYTES
//   - 400 on invalid base64
//   - 400 on absolute / traversal path
//   - happy path: base64 + subdir files pass through to the JobSpec
//
// Plus pure unit tests for the validateFiles / sanitizeWorkspacePath helpers.

import { describe, it, expect, beforeAll, afterAll, beforeEach } from "vitest";
import type { Hono } from "hono";
import { validateFiles, sanitizeWorkspacePath } from "../src/files.ts";

const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;
const REDIS_URL = process.env["REDIS_URL"]!;

async function getApp(): Promise<Hono> {
  const { resetManifests } = await import("../src/manifests.ts");
  resetManifests();
  const { makeApp } = await import("../src/app.ts");
  return makeApp();
}

async function postExecute(app: Hono, body: unknown) {
  return app.request("/v1/execute", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${VALID_TOKEN}`,
    },
    body: JSON.stringify(body),
  });
}

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
    /* skip */
  }
}

// ── Pure unit tests (no Redis / no app) ───────────────────────────────────────

describe("sanitizeWorkspacePath", () => {
  it("preserves flat and subdir paths", () => {
    expect(sanitizeWorkspacePath("a.txt")).toEqual({ ok: true, rel: "a.txt" });
    expect(sanitizeWorkspacePath("data/x.csv")).toEqual({ ok: true, rel: "data/x.csv" });
    expect(sanitizeWorkspacePath("./data/x.csv")).toEqual({ ok: true, rel: "data/x.csv" });
  });
  it("rejects absolute, traversal, and empty", () => {
    expect(sanitizeWorkspacePath("/etc/passwd").ok).toBe(false);
    expect(sanitizeWorkspacePath("../escape").ok).toBe(false);
    expect(sanitizeWorkspacePath("a/../../escape").ok).toBe(false);
    expect(sanitizeWorkspacePath("").ok).toBe(false);
  });
});

describe("validateFiles", () => {
  it("sums utf8 byte length", () => {
    const r = validateFiles([{ name: "a.txt", content: "héllo" }]);
    expect(r.error).toBeUndefined();
    expect(r.totalBytes).toBe(Buffer.byteLength("héllo", "utf8"));
  });
  it("sums decoded base64 byte length", () => {
    // "AAEC/w==" decodes to 4 bytes.
    const r = validateFiles([{ name: "b.bin", content: "AAEC/w==", encoding: "base64" }]);
    expect(r.error).toBeUndefined();
    expect(r.totalBytes).toBe(4);
  });
  it("flags invalid base64", () => {
    const r = validateFiles([{ name: "b.bin", content: "not!!base64", encoding: "base64" }]);
    expect(r.error?.kind).toBe("base64");
  });
  it("flags bad path", () => {
    const r = validateFiles([{ name: "../escape", content: "x" }]);
    expect(r.error?.kind).toBe("path");
  });

  // ── content/ref XOR (Phase 16, BLOB) ───────────────────────────────────────
  it("accepts a valid ref file and does NOT count its bytes", () => {
    const r = validateFiles([
      { name: "data.csv", ref: "sha256:" + "a".repeat(64) },
    ] as never);
    expect(r.error).toBeUndefined();
    expect(r.totalBytes).toBe(0);
  });
  it("flags a file with BOTH content and ref", () => {
    const r = validateFiles([
      { name: "x", content: "hi", ref: "sha256:" + "a".repeat(64) },
    ] as never);
    expect(r.error?.kind).toBe("ref");
  });
  it("flags a file with NEITHER content nor ref", () => {
    const r = validateFiles([{ name: "x" }] as never);
    expect(r.error?.kind).toBe("ref");
  });
  it("flags a malformed ref", () => {
    const r = validateFiles([{ name: "x", ref: "sha256:zzz" }] as never);
    expect(r.error?.kind).toBe("ref");
  });
});

// ── HTTP integration (needs the app; some assertions need Redis) ───────────────

describe("POST /v1/execute — file validation", () => {
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

  it("returns 413 when total decoded bytes exceed MAX_FILES_BYTES", async () => {
    // Default cap is 8 MiB; send ~9 MiB of utf8 content.
    const big = "x".repeat(9 * 1024 * 1024);
    const res = await postExecute(app, {
      language: "python",
      files: [
        { name: "main.py", content: "pass" },
        { name: "big.txt", content: big },
      ],
    });
    expect(res.status).toBe(413);
    const body = (await res.json()) as Record<string, unknown>;
    expect(String(body["error"])).toMatch(/too large|MAX_FILES_BYTES/i);
  });

  it("returns 400 for invalid base64 content", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "blob.bin", content: "not!!base64", encoding: "base64" }],
    });
    expect(res.status).toBe(400);
    const body = (await res.json()) as Record<string, unknown>;
    expect(String(body["error"])).toMatch(/base64/i);
  });

  it("returns 400 for an absolute path", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "/etc/passwd", content: "x" }],
    });
    expect(res.status).toBe(400);
    const body = (await res.json()) as Record<string, unknown>;
    expect(String(body["error"])).toMatch(/path|absolute/i);
  });

  it("returns 400 for a traversal path", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "../escape.py", content: "x" }],
    });
    expect(res.status).toBe(400);
    const body = (await res.json()) as Record<string, unknown>;
    expect(String(body["error"])).toMatch(/path|traversal/i);
  });

  it("accepts base64 + subdir files and passes them through to the JobSpec", async () => {
    let r: import("ioredis").default;
    try {
      r = await getTestRedis();
    } catch {
      return; // skip side-effect assertions without Redis
    }
    const res = await postExecute(app, {
      language: "python",
      files: [
        { name: "main.py", content: "print(1)" },
        { name: "data/in.csv", content: "a,b\n1,2\n", encoding: "utf8" },
        { name: "assets/blob.bin", content: "AAEC/w==", encoding: "base64" },
      ],
    });
    expect(res.status).toBe(202);
    const { jobId } = (await res.json()) as { jobId: string };
    const specJson = await r.get(`job:${jobId}:spec`);
    expect(specJson).not.toBeNull();
    const spec = JSON.parse(specJson!) as { files: Array<Record<string, unknown>> };
    expect(spec.files).toHaveLength(3);
    const blob = spec.files.find((f) => f["name"] === "assets/blob.bin");
    expect(blob).toBeDefined();
    expect(blob!["encoding"]).toBe("base64");
    expect(blob!["content"]).toBe("AAEC/w==");
    const csv = spec.files.find((f) => f["name"] === "data/in.csv");
    expect(csv).toBeDefined();
  });

  it("backward-compat: a flat, no-encoding request behaves exactly as before (202)", async () => {
    const res = await postExecute(app, {
      language: "python",
      files: [{ name: "main.py", content: "print('hello')" }],
    });
    expect(res.status).toBe(202);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body["status"]).toBe("queued");
  });
});
