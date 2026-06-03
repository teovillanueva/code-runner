import { strict as assert } from "node:assert";
import { afterEach, test } from "node:test";

import { CodeRunnerClient } from "../src/client.ts";

// ── Test seam ────────────────────────────────────────────────────────────────
// The client injects W3C trace headers from `globalThis.__OTEL_API__` (an
// optional `@opentelemetry/api` override) when present. We drive that seam with
// a tiny fake implementing the two members the client uses — `propagation.inject`
// and `context.active()` — so the suite asserts the injection contract WITHOUT
// depending on `@opentelemetry/api` being installed/resolvable from this package
// (it is an optional peer; non-OTel consumers never have it). One test also uses
// the real package when available to prove the byte-format is genuine W3C.

type OtelGlobal = typeof globalThis & {
  __OTEL_API__?: {
    propagation: {
      inject(ctx: unknown, carrier: Record<string, string>): void;
    };
    context: { active(): unknown };
  };
};

afterEach(() => {
  delete (globalThis as OtelGlobal).__OTEL_API__;
});

/** A fetch fake that records the URL + headers of the request it sees. */
function recordingFetch(): {
  fetch: typeof globalThis.fetch;
  calls: { url: string; headers: Record<string, string> }[];
} {
  const calls: { url: string; headers: Record<string, string> }[] = [];
  const fetch = (async (url: string, init?: RequestInit) => {
    calls.push({
      url,
      headers: { ...(init?.headers as Record<string, string>) },
    });
    return new Response(
      JSON.stringify({ jobId: "j1", channel: "private-run-j1", status: "queued" }),
      { status: 202, headers: { "Content-Type": "application/json" } },
    );
  }) as unknown as typeof globalThis.fetch;
  return { fetch, calls };
}

/** Install a fake OTel api that injects a fixed traceparent (simulating an active span). */
function installFakeOtel(traceparent: string): void {
  (globalThis as OtelGlobal).__OTEL_API__ = {
    propagation: {
      inject(_ctx: unknown, carrier: Record<string, string>) {
        carrier["traceparent"] = traceparent;
      },
    },
    context: { active: () => ({}) },
  };
}

test("with an active caller context, /v1/execute carries the injected traceparent", async () => {
  const tp = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01";
  installFakeOtel(tp);
  const { fetch, calls } = recordingFetch();
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  await client.execute({
    language: "python",
    files: [{ name: "main.py", content: "print(1)" }],
  });

  assert.equal(calls.length, 1);
  assert.equal(calls[0]?.url, "http://x/v1/execute");
  assert.equal(calls[0]?.headers["traceparent"], tp);
});

test("with NO active caller context, /v1/execute still succeeds and carries no traceparent", async () => {
  // No __OTEL_API__ installed and (by default) no real active span → no header.
  const { fetch, calls } = recordingFetch();
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  const res = await client.execute({
    language: "python",
    files: [{ name: "main.py", content: "print(1)" }],
  });

  assert.equal(res.jobId, "j1");
  assert.equal(calls.length, 1);
  assert.equal(calls[0]?.headers["traceparent"], undefined);
});

test("injection is no-op AND no-error when @opentelemetry/api throws (optional peer absent)", async () => {
  // Simulate the optional peer being absent by making the override throw on use.
  (globalThis as OtelGlobal).__OTEL_API__ = {
    propagation: {
      inject() {
        throw new Error("module not found: @opentelemetry/api");
      },
    },
    context: {
      active() {
        throw new Error("module not found: @opentelemetry/api");
      },
    },
  };
  const { fetch, calls } = recordingFetch();
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  // Must not throw; request proceeds exactly as before, with no traceparent.
  const res = await client.execute({
    language: "python",
    files: [{ name: "main.py", content: "x" }],
  });

  assert.equal(res.jobId, "j1");
  assert.equal(calls[0]?.headers["traceparent"], undefined);
});

test("traceparent is injected ONLY on /v1/execute, never on stdin/kill/status paths", async () => {
  installFakeOtel("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01");
  const { fetch, calls } = recordingFetch();
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  await client.sendStdin("j1", "data");
  await client.kill("j1");
  await client.getJob("j1");
  await client.start("j1");
  await client.listLanguages();

  assert.ok(calls.length >= 5);
  for (const call of calls) {
    assert.ok(
      !call.url.endsWith("/v1/execute"),
      "no execute call expected in this test",
    );
    assert.equal(
      call.headers["traceparent"],
      undefined,
      `traceparent must not be injected on ${call.url}`,
    );
  }
});

test("injection uses a genuine W3C traceparent when the real @opentelemetry/api is present", async (t) => {
  // Best-effort: if the optional peer is resolvable, prove the real
  // propagation.inject produces a spec-shaped traceparent under an active span.
  let api: typeof import("@opentelemetry/api");
  try {
    api = await import("@opentelemetry/api");
  } catch {
    t.skip("@opentelemetry/api not installed (optional peer) — covered by fake-OTel tests");
    return;
  }

  // The global `propagation` defaults to a no-op propagator until one is
  // registered; install the W3C trace-context propagator so inject() emits a
  // real `traceparent` (mirrors how the API/worker SDKs set TraceContext).
  const w3c = await import("@opentelemetry/core").catch(() => null);
  if (!w3c) {
    t.skip("@opentelemetry/core not installed — covered by fake-OTel tests");
    return;
  }
  api.propagation.setGlobalPropagator(new w3c.W3CTraceContextPropagator());

  // Build an active span context manually and make context.active() return it,
  // via the test seam, so we exercise the REAL inject() byte-format.
  const traceId = "0af7651916cd43dd8448eb211c80319c";
  const spanId = "b7ad6b7169203331";
  const activeCtx = api.trace.setSpanContext(api.context.active(), {
    traceId,
    spanId,
    traceFlags: api.TraceFlags.SAMPLED,
    isRemote: false,
  });
  (globalThis as OtelGlobal).__OTEL_API__ = {
    propagation: api.propagation,
    context: { active: () => activeCtx },
  };

  const { fetch, calls } = recordingFetch();
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });
  await client.execute({
    language: "python",
    files: [{ name: "main.py", content: "print(1)" }],
  });

  const tp = calls[0]?.headers["traceparent"];
  assert.ok(tp, "expected a traceparent header");
  // W3C: version-traceid-spanid-flags; traceid must match the active span.
  assert.match(tp, /^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$/);
  assert.equal(tp.split("-")[1], traceId);
});
