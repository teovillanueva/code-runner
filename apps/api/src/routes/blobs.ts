// POST /v1/blobs/check    — existence handshake: which blobs are missing (with a
//                           presigned PUT URL) vs already present (TTL refreshed).
// POST /v1/blobs/finalize — record liveness for just-uploaded blobs (no byte read;
//                           integrity is the worker's pull-time job).
//
// The API PRESIGNS PUT URLs (pure local crypto, no S3 network call, no byte
// proxying through the gateway). Existence is checked against REDIS
// (EXISTS blob:meta:<hash>), never S3. Finalize records Redis liveness only.
// The worker is the authoritative sha256 verifier on pull (Phase 16, plan 01).
//
// Bearer auth is automatic via the central /v1/* middleware (registered in app.ts).

import { Hono } from "hono";
import { zValidator } from "@hono/zod-validator";
import {
  BlobCheckRequestSchema,
  BlobFinalizeRequestSchema,
  keys,
  type BlobCheckResponse,
  type BlobFinalizeResponse,
  type BlobUpload,
} from "@teovilla/code-runner-contract";
import { getRedis } from "../redis.ts";
import { config } from "../config.ts";
import { presignBlobPut } from "../blobPresign.ts";

// Monotonic touch-on-use: a verbatim port of the Go worker's
// internal/blobindex.touchScript (16-01). It SADDs the hash to blobs:index,
// records size/createdAtMs ONCE via HSETNX, and extends the idle TTL ONLY when
// the requested TTL is longer than the current remaining one (or no TTL/no key).
// A shorter touch never shrinks the TTL — the monotonic guarantee. Kept in
// lockstep with the worker so a "present" touch (API) and a pull touch (worker)
// behave identically.
//
//   KEYS[1] = blob:meta:<hash>     KEYS[2] = blobs:index
//   ARGV[1] = hash  ARGV[2] = size  ARGV[3] = createdAtMs  ARGV[4] = requestedTTLms
const TOUCH_SCRIPT = `
local meta  = KEYS[1]
local index = KEYS[2]
local hash  = ARGV[1]
local size  = ARGV[2]
local created = ARGV[3]
local reqTtl = tonumber(ARGV[4])

redis.call('SADD', index, hash)
redis.call('HSETNX', meta, 'size', size)
redis.call('HSETNX', meta, 'createdAtMs', created)

local cur = redis.call('PTTL', meta)
if cur < 0 or reqTtl > cur then
  redis.call('PEXPIRE', meta, reqTtl)
  return reqTtl
end
return cur
`;

/** 501 body returned when the blob store is not configured (mirrors no-op default). */
function notConfigured(): { error: string } {
  return {
    error:
      "blob store not configured: set BLOB_S3_PUBLIC_ENDPOINT/AWS_ENDPOINT_URL_S3, " +
      "BLOB_S3_BUCKET/BUCKET_NAME, and AWS_* credentials to enable content-addressed blobs",
  };
}

export function registerBlobsRoutes(app: Hono): void {
  // POST /v1/blobs/check — existence + presign handshake.
  app.post(
    "/v1/blobs/check",
    zValidator("json", BlobCheckRequestSchema, (result, c) => {
      if (!result.success) {
        return c.json(
          {
            error: "Validation error",
            details: result.error.issues.map((i) => ({
              path: i.path.join("."),
              message: i.message,
            })),
          },
          400,
        );
      }
    }),
    async (c) => {
      if (!config.blobStoreConfigured) {
        return c.json(notConfigured(), 501);
      }
      const { hashes } = c.req.valid("json");
      const redis = getRedis();
      const ttlMs = config.blobIdleTtlSeconds * 1000;
      const createdAtMs = Date.now();

      const missing: BlobUpload[] = [];
      const present: string[] = [];

      // De-dup while preserving first-seen order (a caller may repeat a hash).
      const seen = new Set<string>();
      for (const hash of hashes) {
        if (seen.has(hash)) continue;
        seen.add(hash);

        const exists = await redis.exists(keys.blobMeta(hash));
        if (exists > 0) {
          // Present: monotonically touch the TTL (extend, never shrink) and
          // re-assert index membership. Size is unknown here — HSETNX is a no-op
          // since createdAt/size were recorded on first finalize; pass 0.
          await redis.eval(
            TOUCH_SCRIPT,
            2,
            keys.blobMeta(hash),
            keys.blobIndex,
            hash,
            "0",
            String(createdAtMs),
            String(ttlMs),
          );
          present.push(hash);
        } else {
          // Missing: presign a PUT to blobs/cas/<hash> against the PUBLIC
          // endpoint (local crypto, no S3 call). Short upload window.
          const uploadUrl = await presignBlobPut(
            hash,
            config.blobUploadUrlTtlSeconds,
          );
          missing.push({ hash, uploadUrl });
        }
      }

      const body: BlobCheckResponse = { missing, present };
      return c.json(body, 200);
    },
  );

  // POST /v1/blobs/finalize — record liveness for just-uploaded blobs. No byte
  // read: integrity is verified authoritatively by the worker on pull (16-01).
  app.post(
    "/v1/blobs/finalize",
    zValidator("json", BlobFinalizeRequestSchema, (result, c) => {
      if (!result.success) {
        return c.json(
          {
            error: "Validation error",
            details: result.error.issues.map((i) => ({
              path: i.path.join("."),
              message: i.message,
            })),
          },
          400,
        );
      }
    }),
    async (c) => {
      if (!config.blobStoreConfigured) {
        return c.json(notConfigured(), 501);
      }
      const { hashes } = c.req.valid("json");
      const redis = getRedis();
      const ttlMs = config.blobIdleTtlSeconds * 1000;
      const createdAtMs = Date.now();

      const finalized: string[] = [];
      const seen = new Set<string>();
      for (const hash of hashes) {
        if (seen.has(hash)) continue;
        seen.add(hash);
        // Record/refresh liveness: SADD blobs:index + HSETNX meta + monotonic
        // idle TTL. Identical semantics to the worker's Touch (same Lua).
        await redis.eval(
          TOUCH_SCRIPT,
          2,
          keys.blobMeta(hash),
          keys.blobIndex,
          hash,
          "0",
          String(createdAtMs),
          String(ttlMs),
        );
        finalized.push(hash);
      }

      const body: BlobFinalizeResponse = { finalized };
      return c.json(body, 200);
    },
  );
}
