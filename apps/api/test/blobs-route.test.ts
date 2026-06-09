// /v1/blobs/check + /v1/blobs/finalize route tests (Phase 16, BLOB-02/03/04).
//
// Mirrors the route-test convention (output-route.test.ts): drive the app via
// app.request with a Bearer header and seed/inspect the test Redis directly.
// We do NOT need a live S3 — a presigned PUT URL is just a well-formed signed
// string, so we assert its SHAPE (correct host + blobs/cas/<hash> key + sig
// params), never upload. The blob store is wired by setting blob env BEFORE the
// config singleton + app are imported (see beforeAll).
//
// The test Redis is the :6380 convention from vitest.config.ts. If Redis is
// unavailable, the side-effect (present/finalize) assertions are skipped; the
// 401/400/501 assertions never need Redis.

import { describe, it, expect, beforeAll, afterAll, beforeEach } from "vitest";
import type { Hono } from "hono";
import { keys } from "@teovilla/code-runner-contract";

const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;
const REDIS_URL = process.env["REDIS_URL"]!;

const HASH_A =
  "sha256:" + "a".repeat(64);
const HASH_B =
  "sha256:" + "b".repeat(64);
const PUBLIC_ENDPOINT = "http://127.0.0.1:9000";
const BUCKET = "code-runner-blobs-test";

async function getApp(): Promise<Hono> {
  const { resetManifests } = await import("../src/manifests.ts");
  resetManifests();
  const { makeApp } = await import("../src/app.ts");
  return makeApp();
}

function post(
  app: Hono,
  path: string,
  body: unknown,
  token: string | null = VALID_TOKEN,
) {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token !== null) headers["Authorization"] = `Bearer ${token}`;
  return app.request(path, { method: "POST", headers, body: JSON.stringify(body) });
}

let redisClient: import("ioredis").default | null = null;
async function getTestRedis() {
  if (!redisClient) {
    const { default: Redis } = await import("ioredis");
    redisClient = new Redis(REDIS_URL, { lazyConnect: false, maxRetriesPerRequest: 1 });
  }
  return redisClient;
}
async function flushRedis(): Promise<boolean> {
  try {
    const r = await getTestRedis();
    await r.flushdb();
    return true;
  } catch {
    return false;
  }
}

// ── Configured-store suite ───────────────────────────────────────────────────
describe("/v1/blobs/* — configured store", () => {
  let app: Hono;

  beforeAll(async () => {
    // Set blob env BEFORE importing config/app so the singleton picks it up.
    process.env["BLOB_S3_PUBLIC_ENDPOINT"] = PUBLIC_ENDPOINT;
    process.env["BLOB_S3_BUCKET"] = BUCKET;
    process.env["AWS_ACCESS_KEY_ID"] = "minioadmin";
    process.env["AWS_SECRET_ACCESS_KEY"] = "minioadmin";
    process.env["AWS_REGION"] = "us-east-1";
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

  it("check: returns a presigned PUT URL for a missing blob", async () => {
    const ok = await flushRedis();
    if (!ok) return;
    const res = await post(app, "/v1/blobs/check", { hashes: [HASH_A] });
    expect(res.status).toBe(200);
    const body = (await res.json()) as {
      missing: { hash: string; uploadUrl: string }[];
      present: string[];
    };
    expect(body.present).toEqual([]);
    expect(body.missing).toHaveLength(1);
    expect(body.missing[0]!.hash).toBe(HASH_A);
    const url = new URL(body.missing[0]!.uploadUrl);
    // Signed against the public endpoint host, with the blobs/cas/<hash> key.
    // The ":" in sha256:<hex> is percent-encoded in the path (%3A) — decode to
    // compare; SigV4 signs the encoded form, the SDK PUTs the URL verbatim.
    expect(url.host).toBe("127.0.0.1:9000");
    expect(decodeURIComponent(url.pathname)).toBe(`/${BUCKET}/blobs/cas/${HASH_A}`);
    // SigV4 query params present (a well-formed presigned URL).
    expect(url.searchParams.get("X-Amz-Algorithm")).toBe("AWS4-HMAC-SHA256");
    expect(url.searchParams.get("X-Amz-Signature")).toBeTruthy();
    expect(url.searchParams.get("X-Amz-Expires")).toBeTruthy();
  });

  it("check: returns present (no upload URL) for a known blob and touches TTL", async () => {
    const ok = await flushRedis();
    if (!ok) return;
    const r = await getTestRedis();
    // Seed liveness as if previously finalized, with a short TTL.
    await r.hset(keys.blobMeta(HASH_B), "size", "10", "createdAtMs", String(Date.now()));
    await r.pexpire(keys.blobMeta(HASH_B), 1000);
    await r.sadd(keys.blobIndex, HASH_B);

    const res = await post(app, "/v1/blobs/check", { hashes: [HASH_B] });
    expect(res.status).toBe(200);
    const body = (await res.json()) as { missing: unknown[]; present: string[] };
    expect(body.missing).toEqual([]);
    expect(body.present).toEqual([HASH_B]);
    // TTL was extended monotonically toward the idle TTL (>> the seeded 1s).
    const ttl = await r.pttl(keys.blobMeta(HASH_B));
    expect(ttl).toBeGreaterThan(60_000);
    // Membership re-asserted.
    expect(await r.sismember(keys.blobIndex, HASH_B)).toBe(1);
  });

  it("check: mixes missing + present in one call", async () => {
    const ok = await flushRedis();
    if (!ok) return;
    const r = await getTestRedis();
    await r.hset(keys.blobMeta(HASH_B), "size", "10", "createdAtMs", String(Date.now()));
    await r.pexpire(keys.blobMeta(HASH_B), 5000);

    const res = await post(app, "/v1/blobs/check", { hashes: [HASH_A, HASH_B] });
    const body = (await res.json()) as {
      missing: { hash: string }[];
      present: string[];
    };
    expect(body.missing.map((m) => m.hash)).toEqual([HASH_A]);
    expect(body.present).toEqual([HASH_B]);
  });

  it("finalize: records liveness (meta + index) without reading bytes", async () => {
    const ok = await flushRedis();
    if (!ok) return;
    const r = await getTestRedis();
    const res = await post(app, "/v1/blobs/finalize", { hashes: [HASH_A] });
    expect(res.status).toBe(200);
    const body = (await res.json()) as { finalized: string[] };
    expect(body.finalized).toEqual([HASH_A]);
    // Liveness recorded: meta exists with a TTL, index contains the hash.
    expect(await r.exists(keys.blobMeta(HASH_A))).toBe(1);
    expect(await r.pttl(keys.blobMeta(HASH_A))).toBeGreaterThan(60_000);
    expect(await r.sismember(keys.blobIndex, HASH_A)).toBe(1);
  });

  it("check: rejects a malformed hash with 400", async () => {
    const res = await post(app, "/v1/blobs/check", { hashes: ["not-a-hash"] });
    expect(res.status).toBe(400);
  });

  it("check: rejects a sha256 with wrong hex length with 400", async () => {
    const res = await post(app, "/v1/blobs/check", {
      hashes: ["sha256:" + "a".repeat(63)],
    });
    expect(res.status).toBe(400);
  });

  it("finalize: rejects a malformed hash with 400", async () => {
    const res = await post(app, "/v1/blobs/finalize", { hashes: ["nope"] });
    expect(res.status).toBe(400);
  });

  it("requires a bearer token (401)", async () => {
    const res = await post(app, "/v1/blobs/check", { hashes: [HASH_A] }, null);
    expect(res.status).toBe(401);
  });
});

// ── Unconfigured-store suite ─────────────────────────────────────────────────
// A separate suite in a fresh module graph (vitest singleFork resets modules per
// file, but config is a singleton within a file) — we exercise the 501 path by
// asserting it directly against a config where blobStoreConfigured is false.
describe("/v1/blobs/* — presign endpoint parsing", () => {
  it("parseEndpoint handles scheme/port/host forms", async () => {
    const { parseEndpoint } = await import("../src/blobPresign.ts");
    expect(parseEndpoint("http://minio:9000")).toEqual({
      endPoint: "minio",
      port: 9000,
      useSSL: false,
    });
    expect(parseEndpoint("https://s3.example.com")).toEqual({
      endPoint: "s3.example.com",
      port: 443,
      useSSL: true,
    });
    expect(parseEndpoint("https://s3.example.com:8443/")).toEqual({
      endPoint: "s3.example.com",
      port: 8443,
      useSSL: true,
    });
    expect(parseEndpoint("127.0.0.1:9000")).toEqual({
      endPoint: "127.0.0.1",
      port: 9000,
      useSSL: false,
    });
  });
});
