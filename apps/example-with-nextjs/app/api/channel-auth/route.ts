import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";
import { getChannelAuthorizer } from "@/app/lib/code-runner";
import { errorResponse } from "@/app/lib/api-helpers";

export const dynamic = "force-dynamic";

// POST /api/channel-auth — soketi private-channel authorizer endpoint.
//
// pusher-js calls this with `socket_id` + `channel_name` (form-urlencoded by
// default). We sign with the server-side APP_SECRET via sdk-node's
// createChannelAuthorizer, which refuses anything but private-run-* channels.
//
// In a real app you'd FIRST check the user's session here and confirm they own
// the job before signing. This example signs unconditionally.
export async function POST(req: NextRequest) {
  try {
    const { socketId, channelName } = await readAuthParams(req);
    if (!socketId || !channelName) {
      return NextResponse.json(
        { error: "socket_id and channel_name are required" },
        { status: 400 },
      );
    }
    const auth = getChannelAuthorizer()(socketId, channelName);
    return NextResponse.json(auth);
  } catch (err) {
    return errorResponse(err);
  }
}

async function readAuthParams(
  req: NextRequest,
): Promise<{ socketId: string; channelName: string }> {
  const contentType = req.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) {
    const body = (await req.json()) as {
      socket_id?: string;
      channel_name?: string;
    };
    return {
      socketId: body.socket_id ?? "",
      channelName: body.channel_name ?? "",
    };
  }
  // Default pusher-js encoding: application/x-www-form-urlencoded.
  const form = await req.formData();
  return {
    socketId: String(form.get("socket_id") ?? ""),
    channelName: String(form.get("channel_name") ?? ""),
  };
}
