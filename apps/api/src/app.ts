// Hono app factory — assembles the full /v1/* API.
//
// Exported as makeApp() so tests can create an isolated instance.
// Applies bearerAuth middleware to all /v1/* routes.

import { Hono } from "hono";
import { bearerAuth } from "./auth.ts";
import { registerExecuteRoutes } from "./routes/execute.ts";
import { registerJobsRoutes } from "./routes/jobs.ts";
import { registerLanguagesRoutes } from "./routes/languages.ts";
import { registerControlRoutes } from "./routes/control.ts";
import { registerChannelAuthRoutes } from "./channelAuth.ts";
import { config } from "./config.ts";

export function makeApp(): Hono {
  const app = new Hono();

  // Health check (no auth required)
  app.get("/health", (c) => c.json({ status: "ok" }));

  // Apply bearer auth to all /v1/* routes
  app.use("/v1/*", bearerAuth);

  // Register route groups
  registerExecuteRoutes(app);
  registerJobsRoutes(app);
  registerLanguagesRoutes(app);
  registerControlRoutes(app);

  // Optional channel-auth helper (CHAN-02) — guarded by ENABLE_CHANNEL_AUTH
  if (config.enableChannelAuth) {
    registerChannelAuthRoutes(app);
  }

  // Global error handler
  app.onError((err, c) => {
    console.error("[api] unhandled error:", err);
    return c.json({ error: "Internal server error" }, 500);
  });

  return app;
}
