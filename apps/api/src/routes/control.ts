// Control endpoints: /start, /stdin, /stdin/close, /kill
//
// Each endpoint PUBLISHes to the correct Redis channel from @teovilla/code-runner-contract.
// The API NEVER calls the worker directly (API-11) — only PUBLISH/LPUSH/SET/GET.
//
// /start  → PUBLISH controlChannel(id) {type:"start"}   (API-02)
// /stdin  → PUBLISH stdinChannel(id)   StdinMessage      (API-03)
// /stdin/close → PUBLISH controlChannel(id) {type:"stdin_close"} (API-04)
// /kill   → PUBLISH controlChannel(id) {type:"kill"}    (API-05)

import { Hono } from "hono";
import { zValidator } from "@hono/zod-validator";
import { controlChannel, stdinChannel, keys, StdinMessageSchema } from "@teovilla/code-runner-contract";
import { getRedis } from "../redis.ts";
import { stdinRateLimit, stdinByteCapCheck } from "../ratelimit.ts";

// TTL (seconds) for the durable start flag. Must comfortably outlive the worst
// case a job spends queued behind capacity before a worker claims it, while
// still self-cleaning for jobs that are started but never claimed (e.g. the tab
// closed). Admission caps queue depth, so an hour is far beyond any real wait.
const START_FLAG_TTL_SECONDS = 3600;

export function registerControlRoutes(app: Hono): void {
  // POST /v1/jobs/:id/start → durable start flag + PUBLISH controlChannel {type:"start"}
  //
  // The flag is the SOURCE OF TRUTH for the start-handshake: a job still queued
  // (no worker subscribed to ctrl:<id>) when /start is called would lose the
  // fire-and-forget publish, so the worker reads this flag when it claims the
  // job. The publish stays as the low-latency path for an already-parked worker.
  // SET first so the durable record exists before the ephemeral signal.
  app.post("/v1/jobs/:id/start", async (c) => {
    const jobId = c.req.param("id");
    const redis = getRedis();
    await redis.set(keys.startFlag(jobId), "1", "EX", START_FLAG_TTL_SECONDS);
    await redis.publish(controlChannel(jobId), JSON.stringify({ type: "start" }));
    return c.json({ ok: true }, 202);
  });

  // POST /v1/jobs/:id/stdin → PUBLISH stdinChannel {chunk}
  // Rate-limited (frame rate + pending-byte cap) → 429 on overflow (API-10)
  app.post(
    "/v1/jobs/:id/stdin",
    stdinRateLimit,
    zValidator("json", StdinMessageSchema, (result, c) => {
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
    stdinByteCapCheck,
    async (c) => {
      const jobId = c.req.param("id");
      const body = c.req.valid("json");
      const redis = getRedis();
      await redis.publish(stdinChannel(jobId), JSON.stringify({ chunk: body.chunk }));
      return c.json({ ok: true }, 200);
    },
  );

  // POST /v1/jobs/:id/stdin/close → PUBLISH controlChannel {type:"stdin_close"}
  app.post("/v1/jobs/:id/stdin/close", async (c) => {
    const jobId = c.req.param("id");
    const redis = getRedis();
    await redis.publish(controlChannel(jobId), JSON.stringify({ type: "stdin_close" }));
    return c.json({ ok: true }, 200);
  });

  // POST /v1/jobs/:id/kill → PUBLISH controlChannel {type:"kill"}
  app.post("/v1/jobs/:id/kill", async (c) => {
    const jobId = c.req.param("id");
    const redis = getRedis();
    await redis.publish(controlChannel(jobId), JSON.stringify({ type: "kill" }));
    return c.json({ ok: true }, 200);
  });
}
