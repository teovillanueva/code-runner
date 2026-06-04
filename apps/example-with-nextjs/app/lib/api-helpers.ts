import "server-only";
import { NextResponse } from "next/server";
import {
  CapacityError,
  CodeRunnerError,
  NotFoundError,
  RateLimitError,
  UnauthorizedError,
  ValidationError,
} from "@teovilla/code-runner-sdk-node";

/**
 * Run a route handler body and translate SDK errors into HTTP responses.
 * This is the boundary the browser sees — the bearer token never crosses it.
 */
export async function handle<T>(
  fn: () => Promise<T>,
): Promise<NextResponse> {
  try {
    const data = await fn();
    return NextResponse.json(data ?? { ok: true });
  } catch (err) {
    return errorResponse(err);
  }
}

export function errorResponse(err: unknown): NextResponse {
  if (err instanceof ValidationError) {
    return NextResponse.json({ error: err.message }, { status: 400 });
  }
  if (err instanceof UnauthorizedError) {
    // The browser never sees a real 401 (it would imply the app's own token is
    // bad); surface it as a 502 so it reads as an upstream/config problem.
    return NextResponse.json(
      { error: "code-runner gateway rejected the app token" },
      { status: 502 },
    );
  }
  if (err instanceof NotFoundError) {
    return NextResponse.json({ error: err.message }, { status: 404 });
  }
  if (err instanceof CapacityError) {
    return NextResponse.json(
      { error: err.message, retryAfterMs: err.retryAfterMs },
      { status: 429 },
    );
  }
  if (err instanceof RateLimitError) {
    return NextResponse.json(
      {
        error: err.message,
        retryAfterMs: err.retryAfterMs,
        capBytes: err.capBytes,
      },
      { status: 429 },
    );
  }
  if (err instanceof CodeRunnerError) {
    return NextResponse.json(
      { error: err.message },
      { status: err.status ?? 502 },
    );
  }
  const message = err instanceof Error ? err.message : "Unexpected error";
  return NextResponse.json({ error: message }, { status: 500 });
}
