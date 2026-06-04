import type { NextRequest } from "next/server";
import { getClient } from "@/app/lib/code-runner";
import { handle } from "@/app/lib/api-helpers";

export const dynamic = "force-dynamic";

// GET /api/jobs/:id/output — the authoritative persisted RunResult (stdout,
// stderr, exit code, artifacts). The live soketi stream is best-effort; this is
// the source of truth once the job is done.
export function GET(
  _req: NextRequest,
  { params }: { params: Promise<{ id: string }> },
) {
  return handle(async () => {
    const { id } = await params;
    return getClient().getOutput(id);
  });
}
