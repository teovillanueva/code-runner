// Auth middleware unit tests — vitest
//
// Tests: valid token → next; missing token → 401; wrong token → 401;
// same-length wrong token → 401; different-length token → 401.

import { describe, it, expect } from "vitest";
import { safeEqual } from "../src/auth.ts";
import { createMiddleware } from "hono/factory";
import { createHash, timingSafeEqual } from "node:crypto";
import { Hono } from "hono";

// ── safeEqual unit tests ─────────────────────────────────────────────────────

describe("safeEqual", () => {
  it("returns true for identical strings", () => {
    expect(safeEqual("my-secret-token", "my-secret-token")).toBe(true);
  });

  it("returns false for different strings of the same length", () => {
    // Same length, different content — must NOT short-circuit
    expect(safeEqual("aaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbb")).toBe(false);
  });

  it("returns false for different strings of different length", () => {
    // Different length — naive timingSafeEqual would throw; sha256 normalises this
    expect(safeEqual("short", "a-much-longer-string-that-differs")).toBe(false);
  });

  it("returns false for empty string vs non-empty string", () => {
    expect(safeEqual("", "token")).toBe(false);
  });

  it("returns true for empty strings on both sides", () => {
    expect(safeEqual("", "")).toBe(true);
  });
});

// ── HTTP middleware integration tests ────────────────────────────────────────
// We construct a minimal bearer-auth middleware directly with a known token
// so we can test the HTTP layer without depending on the config singleton.

function makeBearerAuth(expectedToken: string) {
  return createMiddleware(async (c, next) => {
    const header = c.req.header("authorization") ?? "";
    const token = header.startsWith("Bearer ") ? header.slice(7) : "";
    if (!token || !safeEqual(token, expectedToken)) {
      return c.json({ error: "unauthorized" }, 401);
    }
    await next();
  });
}

async function makeRequest(
  expectedToken: string,
  authHeader: string | null,
): Promise<{ status: number; body: unknown }> {
  const app = new Hono();
  app.use("/protected/*", makeBearerAuth(expectedToken));
  app.get("/protected/resource", (c) => c.json({ ok: true }));

  const headers: Record<string, string> = {};
  if (authHeader !== null) {
    headers["authorization"] = authHeader;
  }

  const res = await app.request("/protected/resource", { headers });
  let body: unknown;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status, body };
}

const VALID_TOKEN = "test-bearer-token-for-auth-tests";

describe("bearerAuth middleware", () => {
  it("accepts a valid bearer token and calls next", async () => {
    const { status, body } = await makeRequest(
      VALID_TOKEN,
      `Bearer ${VALID_TOKEN}`,
    );
    expect(status).toBe(200);
    expect(body).toEqual({ ok: true });
  });

  it("rejects a missing Authorization header with 401", async () => {
    const { status, body } = await makeRequest(VALID_TOKEN, null);
    expect(status).toBe(401);
    expect(body).toMatchObject({ error: "unauthorized" });
  });

  it("rejects an Authorization header without Bearer prefix with 401", async () => {
    const { status, body } = await makeRequest(VALID_TOKEN, VALID_TOKEN);
    expect(status).toBe(401);
    expect(body).toMatchObject({ error: "unauthorized" });
  });

  it("rejects a wrong token (same length) with 401", async () => {
    // Construct a token that differs by one character but has the same byte length
    const wrongToken = VALID_TOKEN.slice(0, -1) + "X";
    expect(wrongToken.length).toBe(VALID_TOKEN.length);
    const { status, body } = await makeRequest(
      VALID_TOKEN,
      `Bearer ${wrongToken}`,
    );
    expect(status).toBe(401);
    expect(body).toMatchObject({ error: "unauthorized" });
  });

  it("rejects a wrong token (different length) with 401", async () => {
    const { status, body } = await makeRequest(
      VALID_TOKEN,
      "Bearer definitely-not-the-right-token-at-all",
    );
    expect(status).toBe(401);
    expect(body).toMatchObject({ error: "unauthorized" });
  });

  it("rejects an empty Bearer token with 401", async () => {
    const { status, body } = await makeRequest(VALID_TOKEN, "Bearer ");
    expect(status).toBe(401);
    expect(body).toMatchObject({ error: "unauthorized" });
  });
});
