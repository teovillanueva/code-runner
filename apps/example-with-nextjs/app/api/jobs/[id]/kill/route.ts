import type { NextRequest } from "next/server";
import { getClient } from "@/app/lib/code-runner";
import { handle } from "@/app/lib/api-helpers";

export const dynamic = "force-dynamic";

// POST /api/jobs/:id/kill — terminate the sandbox.
export function POST(
  _req: NextRequest,
  { params }: { params: Promise<{ id: string }> },
) {
  return handle(async () => {
    const { id } = await params;
    await getClient().kill(id);
    return { ok: true };
  });
}
