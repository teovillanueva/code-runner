import type { NextRequest } from "next/server";
import { getClient } from "@/app/lib/code-runner";
import { handle } from "@/app/lib/api-helpers";

export const dynamic = "force-dynamic";

// POST /api/jobs/:id/stdin — write a chunk to the running process' stdin.
export function POST(
  req: NextRequest,
  { params }: { params: Promise<{ id: string }> },
) {
  return handle(async () => {
    const { id } = await params;
    const { chunk } = (await req.json()) as { chunk: string };
    await getClient().sendStdin(id, chunk);
    return { ok: true };
  });
}
