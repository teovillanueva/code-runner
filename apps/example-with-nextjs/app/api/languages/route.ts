import { getClient } from "@/app/lib/code-runner";
import { handle } from "@/app/lib/api-helpers";

export const dynamic = "force-dynamic";

// GET /api/languages — proxy to GET /v1/languages on the gateway.
export function GET() {
  return handle(() => getClient().listLanguages());
}
