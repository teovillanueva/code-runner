// /v1/blobs/* returns 501 when the blob store is NOT configured (Phase 16).
//
// In a fresh module graph (own test file) with NO blob env set, the config
// singleton resolves blobStoreConfigured=false, so the routes return 501
// "blob store not configured" — keeping `docker compose up` a no-op when MinIO
// isn't wired (mirrors telemetry-off-by-default). vitest.config.ts does NOT set
// any BLOB_*/AWS_* env, so this file sees an unconfigured store.

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import type { Hono } from "hono";

const VALID_TOKEN = process.env["EXECUTOR_API_TOKEN"]!;
const HASH = "sha256:" + "c".repeat(64);

async function getApp(): Promise<Hono> {
  // Defensive: ensure no blob env leaked from a sibling (vitest singleFork runs
  // files in separate module graphs, but be explicit).
  delete process.env["BLOB_S3_PUBLIC_ENDPOINT"];
  delete process.env["BLOB_S3_BUCKET"];
  delete process.env["ARTIFACT_S3_PUBLIC_ENDPOINT"];
  delete process.env["AWS_ENDPOINT_URL_S3"];
  delete process.env["ARTIFACT_S3_ENDPOINT"];
  delete process.env["BUCKET_NAME"];
  delete process.env["AWS_ACCESS_KEY_ID"];
  delete process.env["AWS_SECRET_ACCESS_KEY"];
  const { resetManifests } = await import("../src/manifests.ts");
  resetManifests();
  const { makeApp } = await import("../src/app.ts");
  return makeApp();
}

function post(app: Hono, path: string, body: unknown) {
  return app.request(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${VALID_TOKEN}`,
    },
    body: JSON.stringify(body),
  });
}

describe("/v1/blobs/* — unconfigured store returns 501", () => {
  let app: Hono;

  beforeAll(async () => {
    app = await getApp();
  });

  afterAll(async () => {
    const { disconnectRedis } = await import("../src/redis.ts");
    await disconnectRedis();
  });

  it("check returns 501", async () => {
    const res = await post(app, "/v1/blobs/check", { hashes: [HASH] });
    expect(res.status).toBe(501);
    const body = (await res.json()) as { error: string };
    expect(body.error).toContain("blob store not configured");
  });

  it("finalize returns 501", async () => {
    const res = await post(app, "/v1/blobs/finalize", { hashes: [HASH] });
    expect(res.status).toBe(501);
  });
});
