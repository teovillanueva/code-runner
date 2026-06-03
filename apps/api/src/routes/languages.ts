// GET /v1/languages — returns the list of LanguageInfo from the manifests.
// Zero hardcoded language identifiers (API-07).

import { Hono } from "hono";
import { toLanguageInfo } from "@teovilla/code-runner-contract";
import { getManifests } from "../manifests.ts";

export function registerLanguagesRoutes(app: Hono): void {
  app.get("/v1/languages", async (c) => {
    const manifests = await getManifests();
    return c.json(toLanguageInfo(manifests), 200);
  });
}
