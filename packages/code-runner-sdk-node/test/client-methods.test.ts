import { strict as assert } from "node:assert";
import { createHmac } from "node:crypto";
import { test } from "node:test";

import type { LanguageInfo } from "@teovilla/code-runner-contract";
import { CodeRunnerClient } from "../src/client.ts";
import { CodeRunnerError } from "../src/errors.ts";
import {
  createChannelAuthorizer,
  signChannelAuth,
} from "../src/channelAuth.ts";

interface Captured {
  url: string;
  method: string;
  auth: string | undefined;
  body: string | undefined;
}

/** A fetch fake that records the last call and returns a fixed response. */
function recorder(
  status: number,
  body: unknown,
): { fetch: typeof globalThis.fetch; calls: Captured[] } {
  const calls: Captured[] = [];
  const fetch = (async (url: string, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    calls.push({
      url,
      method: init?.method ?? "GET",
      auth: headers.get("Authorization") ?? undefined,
      body: init?.body as string | undefined,
    });
    return new Response(body === undefined ? "" : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  }) as unknown as typeof globalThis.fetch;
  return { fetch, calls };
}

test("listLanguages GETs /v1/languages with the bearer and returns the array", async () => {
  const langs: LanguageInfo[] = [
    { language: "python", version: "3.12", aliases: ["py"] },
  ] as unknown as LanguageInfo[];
  const { fetch, calls } = recorder(200, langs);
  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "secret",
    fetch,
  });

  const res = await client.listLanguages();

  assert.equal(calls[0]?.method, "GET");
  assert.equal(calls[0]?.url, "http://x/v1/languages");
  assert.equal(calls[0]?.auth, "Bearer secret");
  assert.equal(res[0]?.language, "python");
});

test("start POSTs to /v1/jobs/:id/start and resolves void", async () => {
  const { fetch, calls } = recorder(202, { ok: true });
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  const res = await client.start("j1");

  assert.equal(res, undefined);
  assert.equal(calls[0]?.method, "POST");
  assert.equal(calls[0]?.url, "http://x/v1/jobs/j1/start");
});

test("kill POSTs to /v1/jobs/:id/kill", async () => {
  const { fetch, calls } = recorder(200, { ok: true });
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  await client.kill("j1");

  assert.equal(calls[0]?.method, "POST");
  assert.equal(calls[0]?.url, "http://x/v1/jobs/j1/kill");
});

test("closeStdin POSTs to /v1/jobs/:id/stdin/close", async () => {
  const { fetch, calls } = recorder(200, { ok: true });
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  await client.closeStdin("j1");

  assert.equal(calls[0]?.method, "POST");
  assert.equal(calls[0]?.url, "http://x/v1/jobs/j1/stdin/close");
});

test("ids are URL-encoded in every path", async () => {
  const { fetch, calls } = recorder(200, { ok: true });
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  await client.kill("weird id/with spaces");

  assert.equal(calls[0]?.url, "http://x/v1/jobs/weird%20id%2Fwith%20spaces/kill");
});

test("baseUrl trailing slashes are normalized away", async () => {
  const { fetch, calls } = recorder(200, []);
  const client = new CodeRunnerClient({
    baseUrl: "http://x:8080///",
    token: "t",
    fetch,
  });

  await client.listLanguages();

  assert.equal(calls[0]?.url, "http://x:8080/v1/languages");
});

test("an empty 200 body resolves to undefined (no JSON parse error)", async () => {
  const fetch = (async () =>
    new Response("", { status: 200 })) as unknown as typeof globalThis.fetch;
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  const res = await client.start("j1");
  assert.equal(res, undefined);
});

test("an unmapped error status throws a generic CodeRunnerError carrying the status", async () => {
  const fetch = (async () =>
    new Response(JSON.stringify({ error: "boom" }), {
      status: 500,
    })) as unknown as typeof globalThis.fetch;
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  await assert.rejects(
    () => client.listLanguages(),
    (err: unknown) => {
      assert.ok(err instanceof CodeRunnerError);
      assert.equal((err as CodeRunnerError).status, 500);
      assert.equal((err as CodeRunnerError).message, "boom");
      return true;
    },
  );
});

test("constructor throws when no fetch is available", () => {
  const original = globalThis.fetch;
  // Simulate a runtime without a global fetch and no override passed.
  (globalThis as { fetch?: unknown }).fetch = undefined;
  try {
    assert.throws(
      () => new CodeRunnerClient({ baseUrl: "http://x", token: "t" }),
      /No fetch implementation/,
    );
  } finally {
    globalThis.fetch = original;
  }
});

test("signChannelAuth changes with the socketId (HMAC depends on the full payload)", () => {
  const a = signChannelAuth({
    socketId: "1.1",
    channelName: "private-run-x",
    appKey: "k",
    appSecret: "s",
  });
  const b = signChannelAuth({
    socketId: "2.2",
    channelName: "private-run-x",
    appKey: "k",
    appSecret: "s",
  });
  assert.notEqual(a.auth, b.auth);
  // And each still matches the canonical pusher formula.
  assert.equal(
    a.auth,
    "k:" + createHmac("sha256", "s").update("1.1:private-run-x").digest("hex"),
  );
});

test("createChannelAuthorizer rejects private-* channels that are not private-run-*", () => {
  const authorizer = createChannelAuthorizer({ appKey: "k", appSecret: "s" });
  assert.throws(() => authorizer("1.1", "private-foo"), /only private-run-/);
});
