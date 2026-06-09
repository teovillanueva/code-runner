// Hono app factory — assembles the full /v1/* API.
//
// Exported as makeApp() so tests can create an isolated instance.
// Applies bearerAuth middleware to all /v1/* routes.

import { Hono } from "hono";
import { httpInstrumentationMiddleware } from "@hono/otel";
import { bearerAuth } from "./auth.ts";
import { registerExecuteRoutes } from "./routes/execute.ts";
import { registerJobsRoutes } from "./routes/jobs.ts";
import { registerLanguagesRoutes } from "./routes/languages.ts";
import { registerControlRoutes } from "./routes/control.ts";
import { registerBlobsRoutes } from "./routes/blobs.ts";
import { registerMetricsRoutes } from "./routes/metrics.ts";
import { registerChannelAuthRoutes } from "./channelAuth.ts";
import { config } from "./config.ts";
import { getLogger } from "./logger.ts";

export function makeApp(): Hono {
  const app = new Hono();

  // OTel request span for the inbound HTTP lifecycle (D-01). No-op when no SDK
  // is active; load-order-immune (it is middleware, not module-load patching).
  app.use("*", httpInstrumentationMiddleware());

  // Health check (no auth required)
  app.get("/health", (c) => c.json({ status: "ok" }));

  // Prometheus scrape endpoint (no auth — scraped by the platform). Exposes the
  // job-queue depth for autoscaling. Lives outside /v1/* so bearer auth skips it.
  registerMetricsRoutes(app);

  // Apply bearer auth to all /v1/* routes
  app.use("/v1/*", bearerAuth);

  // Register route groups
  registerExecuteRoutes(app);
  registerJobsRoutes(app);
  registerLanguagesRoutes(app);
  registerControlRoutes(app);
  registerBlobsRoutes(app);

  // Optional channel-auth helper (CHAN-02) — guarded by ENABLE_CHANNEL_AUTH
  if (config.enableChannelAuth) {
    registerChannelAuthRoutes(app);
  }

  // Global error handler
  app.onError((err, c) => {
    getLogger().error({ err: err.message }, "unhandled error");
    return c.json({ error: "Internal server error" }, 500);
  });

  return app;
}
