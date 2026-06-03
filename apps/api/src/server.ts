// Entry point — starts the Hono API on @hono/node-server.
// All config is read from environment variables (CFG-01).

import { serve } from "@hono/node-server";
import { makeApp } from "./app.ts";
import { config } from "./config.ts";
import { getManifests } from "./manifests.ts";
import { getLogger } from "./logger.ts";

// NOTE: the OTel SDK is NOT imported here. It is `--import`ed ahead of this
// file (see telemetry.ts + package.json/Dockerfile) so the ioredis ESM hook
// registers before ioredis loads.

async function main() {
  const log = getLogger();

  // Fail fast if manifests cannot be loaded (misconfigured LANGUAGES_DIR)
  const manifests = await getManifests();
  log.info({ count: manifests.length }, "loaded language manifests");

  const app = makeApp();

  serve(
    {
      fetch: app.fetch,
      port: config.apiPort,
    },
    (info) => {
      log.info({ port: info.port }, "api listening");
    },
  );
}

main().catch((err) => {
  getLogger().error(
    { err: err instanceof Error ? err.message : String(err) },
    "fatal startup error",
  );
  process.exit(1);
});
