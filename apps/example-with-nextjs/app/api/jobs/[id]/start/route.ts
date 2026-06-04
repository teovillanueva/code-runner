import type { NextRequest } from "next/server";
import { getClient } from "@/app/lib/code-runner";
import { handle } from "@/app/lib/api-helpers";

export const dynamic = "force-dynamic";

// POST /api/jobs/:id/start — start a queued job. Called by the browser AFTER its
// soketi subscription is confirmed (the start-handshake), so output is not
// emitted before a subscriber is listening.
export function POST(
  _req: NextRequest,
  { params }: { params: Promise<{ id: string }> },
) {
  return handle(async () => {
    const { id } = await params;
    await getClient().start(id);
    return { ok: true };
  });
}
