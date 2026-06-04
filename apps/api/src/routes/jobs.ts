// GET /v1/jobs/:id         — returns the JobStatus for a job or 404 if not found.
// GET /v1/jobs/:id/status  — alias of the above; mirrors the /output sub-path so
//                            clients can pull the live status to reconcile state
//                            after a late soketi subscription.
// GET /v1/jobs/:id/output  — returns the persisted RunResult (collected run output) or 404.
// Keys come from @teovilla/code-runner-contract (API-11: only Redis GET, never calls worker).

import { Hono, type Context } from "hono";
import { keys } from "@teovilla/code-runner-contract";
import { getRedis } from "../redis.ts";

export function registerJobsRoutes(app: Hono): void {
  // Shared handler: read + parse the persisted JobStatus (job:<id>:status). A
  // job that already advanced past "queued" before the client subscribed to its
  // soketi channel would miss those events; pulling this on subscribe lets the
  // client reconcile to the real state (late-join).
  const respondStatus = async (c: Context) => {
    const jobId = c.req.param("id");
    const redis = getRedis();

    const statusJson = await redis.get(keys.jobStatus(jobId));
    if (!statusJson) {
      return c.json({ error: `Job not found: ${jobId}` }, 404);
    }

    let status: unknown;
    try {
      status = JSON.parse(statusJson);
    } catch {
      return c.json({ error: "Internal error: malformed job status" }, 500);
    }

    return c.json(status, 200);
  };

  app.get("/v1/jobs/:id", respondStatus);
  app.get("/v1/jobs/:id/status", respondStatus);

  // R9: server-side pull surface for collected run output. Redis-only (API-11):
  // reads job:<id>:output and never reaches the worker. A single absence check
  // (404) covers an unknown id, a non-collected job, and a job past its result
  // TTL — all leave the key absent, so a caller cannot probe which ids exist
  // (T-09-15). Bearer auth is enforced by the central /v1/* middleware (T-09-16,
  // R9 401), so no per-handler auth lives here.
  app.get("/v1/jobs/:id/output", async (c) => {
    const jobId = c.req.param("id");
    const redis = getRedis();

    const outputJson = await redis.get(keys.jobOutput(jobId));
    if (!outputJson) {
      return c.json({ error: `Job output not found: ${jobId}` }, 404);
    }

    let runResult: unknown;
    try {
      runResult = JSON.parse(outputJson);
    } catch {
      return c.json({ error: "Internal error: malformed job output" }, 500);
    }

    return c.json(runResult, 200);
  });
}
