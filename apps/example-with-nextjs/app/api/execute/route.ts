import type { NextRequest } from "next/server";
import type { ExecuteRequest } from "@teovilla/code-runner-sdk-node";
import { getClient } from "@/app/lib/code-runner";
import { handle } from "@/app/lib/api-helpers";

export const dynamic = "force-dynamic";

// POST /api/execute — enqueue a job (queued, NOT started).
//
// Body: { language, version?, files: [{ name, content }], collectOutput? }
// Returns the ExecuteResponse ({ jobId, channel, status }) the client uses to
// subscribe to the private-run-<jobId> soketi channel.
//
// We deliberately do NOT call start() here. The browser subscribes first and
// then hits /api/jobs/:id/start once soketi confirms the subscription (the
// start-handshake) — otherwise the worker can emit (and we'd miss) output before
// any subscriber is listening.
export function POST(req: NextRequest) {
  return handle(async () => {
    const body = (await req.json()) as ExecuteRequest;
    return getClient().execute(body);
  });
}
