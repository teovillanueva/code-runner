// Entry point — starts the Hono API on @hono/node-server.
// All config is read from environment variables (CFG-01).

import { serve } from "@hono/node-server";
import { makeApp } from "./app.ts";
import { config } from "./config.ts";
import { getManifests } from "./manifests.ts";

async function main() {
  // Fail fast if manifests cannot be loaded (misconfigured LANGUAGES_DIR)
  const manifests = await getManifests();
  console.log(`[api] loaded ${manifests.length} language manifest(s)`);

  const app = makeApp();

  serve(
    {
      fetch: app.fetch,
      port: config.apiPort,
    },
    (info) => {
      console.log(`[api] listening on http://localhost:${info.port}`);
    },
  );
}

main().catch((err) => {
  console.error("[api] fatal startup error:", err);
  process.exit(1);
});
