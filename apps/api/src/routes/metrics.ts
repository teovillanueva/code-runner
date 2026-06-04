// GET /metrics — Prometheus scrape endpoint (UNAUTHENTICATED; it lives outside
// /v1/* so the bearer middleware doesn't apply). It's scraped by the platform's
// Prometheus (e.g. Fly's managed Prometheus via the [metrics] block in fly.toml)
// so an autoscaler — fly-autoscaler queries `max(code_runner_queue_depth)` — can
// scale the worker fleet on the job-queue backlog.
//
// Dependency-free on purpose: just LLEN jobs:queue formatted as Prometheus text.
// On a Redis error we return 503 (a failed scrape) rather than a misleading 0,
// so the autoscaler keeps the last good sample instead of scaling the fleet down
// during a Redis blip.

import { Hono } from "hono";
import { keys } from "@teovilla/code-runner-contract";
import { getRedis } from "../redis.ts";

export function registerMetricsRoutes(app: Hono): void {
  app.get("/metrics", async (c) => {
    let depth: number;
    try {
      depth = await getRedis().llen(keys.jobQueue);
    } catch {
      return c.text("# code_runner: redis unavailable\n", 503);
    }
    const body =
      "# HELP code_runner_queue_depth Jobs waiting in jobs:queue (LLEN).\n" +
      "# TYPE code_runner_queue_depth gauge\n" +
      `code_runner_queue_depth ${depth}\n`;
    return c.text(body, 200, {
      "Content-Type": "text/plain; version=0.0.4",
    });
  });
}
