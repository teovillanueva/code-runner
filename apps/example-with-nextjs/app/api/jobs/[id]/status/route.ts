import type { NextRequest } from "next/server";
import { getClient } from "@/app/lib/code-runner";
import { handle } from "@/app/lib/api-helpers";

export const dynamic = "force-dynamic";

// GET /api/jobs/:id/status — the live JobStatus. The browser calls this once its
// soketi subscription is confirmed to reconcile state: a job that already moved
// past "queued" before we subscribed would otherwise miss those events and sit
// stale until the next one. Pulls through the bearer-holding backend (the token
// never reaches the browser).
export function GET(
  _req: NextRequest,
  { params }: { params: Promise<{ id: string }> },
) {
  return handle(async () => {
    const { id } = await params;
    return getClient().getStatus(id);
  });
}
