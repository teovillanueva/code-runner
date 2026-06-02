// Optional channel-auth helper endpoint (CHAN-02).
//
// This is a NON-CORE, opt-in route that authorizes soketi private-channel
// subscriptions. It is:
//   - Disabled by default (requires ENABLE_CHANNEL_AUTH=true)
//   - Guarded by the same bearer-token auth as all /v1/* routes
//   - NOT a dependency of the core execute/stdin/control flow
//
// The soketi APP_SECRET is read from env only and is NEVER written to Redis
// or returned by any endpoint (CFG-02/CFG-03).
//
// Production guidance: in a real deployment, the upstream application should
// authorize its own channel subscriptions. This helper exists for quickstart
// demos so a single service can handle both execution and channel auth.

import { Hono } from "hono";
import Pusher from "pusher";
import { config } from "./config.ts";

let _pusher: Pusher | null = null;

function getPusher(): Pusher {
  if (!_pusher) {
    _pusher = new Pusher({
      appId: config.soketiAppId,
      key: config.soketiAppKey,
      // APP_SECRET is read from env only; never returned in any response (CFG-02/03)
      secret: config.soketiAppSecret,
      host: config.soketiHost,
      port: String(config.soketiPort),
      useTLS: config.soketiUseTls,
    });
  }
  return _pusher;
}

/**
 * Register the optional channel-auth helper.
 * Called from app.ts only when ENABLE_CHANNEL_AUTH=true.
 *
 * NOT part of the core execution flow — see module-level comment.
 */
export function registerChannelAuthRoutes(app: Hono): void {
  // POST /v1/channel-auth — authorize a soketi private-channel subscription
  // Body: { socket_id: string, channel_name: string }
  app.post("/v1/channel-auth", async (c) => {
    let body: { socket_id?: string; channel_name?: string };
    try {
      body = await c.req.json();
    } catch {
      return c.json({ error: "Invalid JSON body" }, 400);
    }

    const { socket_id, channel_name } = body;
    if (!socket_id || !channel_name) {
      return c.json(
        { error: "Missing required fields: socket_id, channel_name" },
        400,
      );
    }

    // The channel must be a private-run-<jobId> channel
    if (!channel_name.startsWith("private-run-")) {
      return c.json(
        { error: "Only private-run-<jobId> channels can be authorized here" },
        403,
      );
    }

    try {
      const pusher = getPusher();
      const authResponse = pusher.authorizeChannel(socket_id, channel_name);
      return c.json(authResponse, 200);
    } catch (err) {
      console.error("[channel-auth] pusher error:", err);
      return c.json({ error: "Channel authorization failed" }, 500);
    }
  });
}
