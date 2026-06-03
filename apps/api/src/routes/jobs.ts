// GET /v1/jobs/:id — returns the JobStatus for a job or 404 if not found.
// Keys come from @teovilla/code-runner-contract (API-11: only Redis GET, never calls worker).

import { Hono } from "hono";
import { keys } from "@teovilla/code-runner-contract";
import { getRedis } from "../redis.ts";

export function registerJobsRoutes(app: Hono): void {
  app.get("/v1/jobs/:id", async (c) => {
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
  });
}
