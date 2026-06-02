// Constant-time bearer-token authentication middleware.
//
// Uses sha256 + timingSafeEqual (STACK §1.3):
// - timingSafeEqual requires equal-length buffers; hashing both to a fixed
//   32-byte digest removes the early-exit length leak while keeping the
//   comparison constant-time over the full digest.
//
// Rejects missing or invalid tokens with 401 {error:"unauthorized"} (API-08).

import { createHash, timingSafeEqual } from "node:crypto";
import { createMiddleware } from "hono/factory";
import { config } from "./config.ts";

/**
 * Length-safe, constant-time comparison of two strings.
 * Both sides are hashed to a fixed 32-byte SHA-256 digest before comparing.
 */
export function safeEqual(a: string, b: string): boolean {
  const ha = createHash("sha256").update(a).digest();
  const hb = createHash("sha256").update(b).digest();
  return timingSafeEqual(ha, hb);
}

/**
 * Hono middleware: validates the `Authorization: Bearer <token>` header
 * against EXECUTOR_API_TOKEN using constant-time comparison.
 *
 * Returns 401 {error:"unauthorized"} for missing or invalid tokens.
 */
export const bearerAuth = createMiddleware(async (c, next) => {
  const header = c.req.header("authorization") ?? "";
  const token = header.startsWith("Bearer ") ? header.slice(7) : "";

  if (!token || !safeEqual(token, config.executorApiToken)) {
    return c.json({ error: "unauthorized" }, 401);
  }

  await next();
});
