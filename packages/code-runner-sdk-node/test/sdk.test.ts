import { strict as assert } from "node:assert";
import { createHmac } from "node:crypto";
import { test } from "node:test";

import { CodeRunnerClient } from "../src/client.ts";
import {
  CapacityError,
  NotFoundError,
  RateLimitError,
  UnauthorizedError,
  ValidationError,
} from "../src/errors.ts";
import {
  createChannelAuthorizer,
  signChannelAuth,
} from "../src/channelAuth.ts";

test("signChannelAuth is byte-identical to the pusher HMAC formula", () => {
  const res = signChannelAuth({
    socketId: "123.456",
    channelName: "private-run-abc",
    appKey: "k",
    appSecret: "s",
  });
  const expected =
    "k:" +
    createHmac("sha256", "s").update("123.456:private-run-abc").digest("hex");
  assert.equal(res.auth, expected);
});

test("createChannelAuthorizer signs private-run-* channels", () => {
  const authorizer = createChannelAuthorizer({ appKey: "k", appSecret: "s" });
  const res = authorizer("123.456", "private-run-abc");
  const expected =
    "k:" +
    createHmac("sha256", "s").update("123.456:private-run-abc").digest("hex");
  assert.equal(res.auth, expected);
});

test("createChannelAuthorizer throws for non private-run channels", () => {
  const authorizer = createChannelAuthorizer({ appKey: "k", appSecret: "s" });
  assert.throws(() => authorizer("123.456", "public-foo"));
});

function fakeFetch(status: number, body: unknown): typeof globalThis.fetch {
  return (async () =>
    new Response(body === undefined ? "" : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    })) as unknown as typeof globalThis.fetch;
}

test("execute returns parsed body on 202", async () => {
  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "t",
    fetch: fakeFetch(202, {
      jobId: "j1",
      channel: "private-run-j1",
      status: "queued",
    }),
  });
  const res = await client.execute({
    language: "python",
    files: [{ name: "main.py", content: "print(1)" }],
  });
  assert.equal(res.jobId, "j1");
  assert.equal(res.channel, "private-run-j1");
  assert.equal(res.status, "queued");
});

test("execute throws CapacityError with retryAfterMs on 429", async () => {
  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "t",
    fetch: fakeFetch(429, { error: "at capacity", retryAfterMs: 1000 }),
  });
  await assert.rejects(
    () =>
      client.execute({
        language: "python",
        files: [{ name: "main.py", content: "x" }],
      }),
    (err: unknown) => {
      assert.ok(err instanceof CapacityError);
      assert.equal((err as CapacityError).retryAfterMs, 1000);
      return true;
    },
  );
});

test("execute throws ValidationError on 400", async () => {
  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "t",
    fetch: fakeFetch(400, { error: "bad" }),
  });
  await assert.rejects(
    () =>
      client.execute({
        language: "python",
        files: [{ name: "main.py", content: "x" }],
      }),
    ValidationError,
  );
});

test("getJob throws NotFoundError on 404", async () => {
  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "t",
    fetch: fakeFetch(404, { error: "no job" }),
  });
  await assert.rejects(() => client.getJob("nope"), NotFoundError);
});

test("any 401 throws UnauthorizedError", async () => {
  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "bad",
    fetch: fakeFetch(401, { error: "unauthorized" }),
  });
  await assert.rejects(() => client.listLanguages(), UnauthorizedError);
});

test("sendStdin throws RateLimitError carrying retryAfterMs/capBytes on 429", async () => {
  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "t",
    fetch: fakeFetch(429, { error: "stdin cap", capBytes: 65536 }),
  });
  await assert.rejects(
    () => client.sendStdin("j1", "data"),
    (err: unknown) => {
      assert.ok(err instanceof RateLimitError);
      assert.equal((err as RateLimitError).capBytes, 65536);
      return true;
    },
  );
});

test("sendStdin posts {chunk} to the stdin endpoint", async () => {
  let captured: { url?: string; body?: string } = {};
  const fetchImpl = (async (url: string, init?: RequestInit) => {
    captured = { url, body: init?.body as string };
    return new Response(JSON.stringify({ ok: true }), { status: 200 });
  }) as unknown as typeof globalThis.fetch;
  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "t",
    fetch: fetchImpl,
  });
  await client.sendStdin("j1", "hello");
  assert.equal(captured.url, "http://x/v1/jobs/j1/stdin");
  assert.deepEqual(JSON.parse(captured.body ?? "{}"), { chunk: "hello" });
});
