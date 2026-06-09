// CAS blob upload + transparent inline-vs-CAS routing (Phase 16, BLOB-10/11).
//
// We drive the client with a recording fake fetch so we can assert the exact
// call sequence (check -> PUT -> finalize -> execute) without a live API/S3.

import { strict as assert } from "node:assert";
import { createHash } from "node:crypto";
import { test } from "node:test";

import { CodeRunnerClient } from "../src/client.ts";

interface Call {
  url: string;
  method: string;
  body?: unknown;
}

/**
 * A fake fetch that records every call and answers by URL:
 *   /v1/blobs/check    -> { missing: [...], present: [...] } driven by `present`
 *   <presigned PUT>    -> 200 (records the PUT)
 *   /v1/blobs/finalize -> { finalized: hashes }
 *   /v1/execute        -> 202 { jobId, channel, status }
 */
function makeFetch(opts: {
  present?: Set<string>;
  calls: Call[];
  uploadUrl?: string;
}) {
  const present = opts.present ?? new Set<string>();
  const uploadUrl =
    opts.uploadUrl ?? "http://store.example/code-runner/blobs/cas/x?sig=1";
  return (async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    let body: unknown;
    if (init?.body) {
      try {
        body = JSON.parse(String(init.body));
      } catch {
        body = init.body;
      }
    }
    opts.calls.push({ url, method, body });

    if (url.endsWith("/v1/blobs/check")) {
      const hashes = (body as { hashes: string[] }).hashes;
      const missing = hashes
        .filter((h) => !present.has(h))
        .map((h) => ({ hash: h, uploadUrl }));
      const presentList = hashes.filter((h) => present.has(h));
      return new Response(
        JSON.stringify({ missing, present: presentList }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    if (url.endsWith("/v1/blobs/finalize")) {
      const hashes = (body as { hashes: string[] }).hashes;
      return new Response(JSON.stringify({ finalized: hashes }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.endsWith("/v1/execute")) {
      return new Response(
        JSON.stringify({ jobId: "j1", channel: "private-run-j1", status: "queued" }),
        { status: 202, headers: { "Content-Type": "application/json" } },
      );
    }
    // Presigned PUT to the store.
    return new Response("", { status: 200 });
  }) as unknown as typeof globalThis.fetch;
}

function refOf(buf: Buffer): string {
  return "sha256:" + createHash("sha256").update(buf).digest("hex");
}

test("blobs.upload: missing blob -> check, PUT, finalize, returns ref", async () => {
  const calls: Call[] = [];
  const client = new CodeRunnerClient({
    baseUrl: "http://api",
    token: "t",
    fetch: makeFetch({ calls }),
  });
  const buf = Buffer.from("hello blob");
  const { ref } = await client.blobs.upload(buf);

  assert.equal(ref, refOf(buf));
  const seq = calls.map((c) => `${c.method} ${new URL(c.url, "http://api").pathname}`);
  assert.deepEqual(seq, [
    "POST /v1/blobs/check",
    "PUT /code-runner/blobs/cas/x",
    "POST /v1/blobs/finalize",
  ]);
});

test("blobs.upload: present blob -> skips the PUT and finalize", async () => {
  const calls: Call[] = [];
  const buf = Buffer.from("already there");
  const present = new Set([refOf(buf)]);
  const client = new CodeRunnerClient({
    baseUrl: "http://api",
    token: "t",
    fetch: makeFetch({ calls, present }),
  });
  const { ref } = await client.blobs.upload(buf);
  assert.equal(ref, refOf(buf));
  const methods = calls.map((c) => c.method);
  assert.deepEqual(methods, ["POST"]); // only the check call
  assert.ok(calls[0]!.url.endsWith("/v1/blobs/check"));
});

test("executeFiles: small binary goes inline (base64), no blob calls", async () => {
  const calls: Call[] = [];
  const client = new CodeRunnerClient({
    baseUrl: "http://api",
    token: "t",
    fetch: makeFetch({ calls }),
  });
  await client.executeFiles({
    language: "python",
    files: [{ name: "small.bin", data: Buffer.from("tiny") }],
  });
  // Only /v1/execute was called (no check/PUT/finalize).
  assert.equal(calls.length, 1);
  assert.ok(calls[0]!.url.endsWith("/v1/execute"));
  const sentFiles = (calls[0]!.body as { files: { name: string; content?: string; encoding?: string; ref?: string }[] }).files;
  assert.equal(sentFiles[0]!.encoding, "base64");
  assert.equal(sentFiles[0]!.ref, undefined);
  assert.equal(Buffer.from(sentFiles[0]!.content!, "base64").toString(), "tiny");
});

test("executeFiles: large binary is uploaded and sent as {name, ref}", async () => {
  const calls: Call[] = [];
  const client = new CodeRunnerClient({
    baseUrl: "http://api",
    token: "t",
    fetch: makeFetch({ calls }),
    inlineThresholdBytes: 8, // tiny threshold so a small buffer routes to CAS
  });
  const big = Buffer.from("this is bigger than eight bytes");
  await client.executeFiles({
    language: "python",
    files: [{ name: "data.bin", data: big }],
  });
  const seq = calls.map((c) => `${c.method} ${new URL(c.url, "http://api").pathname}`);
  assert.deepEqual(seq, [
    "POST /v1/blobs/check",
    "PUT /code-runner/blobs/cas/x",
    "POST /v1/blobs/finalize",
    "POST /v1/execute",
  ]);
  const execCall = calls.find((c) => c.url.endsWith("/v1/execute"))!;
  const sentFiles = (execCall.body as { files: { name: string; ref?: string; content?: string }[] }).files;
  assert.equal(sentFiles[0]!.name, "data.bin");
  assert.equal(sentFiles[0]!.ref, refOf(big));
  assert.equal(sentFiles[0]!.content, undefined);
});

test("executeFiles: raw {name, ref} passes through unchanged", async () => {
  const calls: Call[] = [];
  const client = new CodeRunnerClient({
    baseUrl: "http://api",
    token: "t",
    fetch: makeFetch({ calls }),
  });
  const ref = "sha256:" + "a".repeat(64);
  await client.executeFiles({
    language: "python",
    files: [{ name: "pre.bin", ref } as never],
  });
  assert.equal(calls.length, 1); // no blob calls; ref passed straight through
  const sentFiles = (calls[0]!.body as { files: { name: string; ref?: string }[] }).files;
  assert.equal(sentFiles[0]!.ref, ref);
});

test("executeFiles: text file stays inline utf8", async () => {
  const calls: Call[] = [];
  const client = new CodeRunnerClient({
    baseUrl: "http://api",
    token: "t",
    fetch: makeFetch({ calls }),
  });
  await client.executeFiles({
    language: "python",
    files: [{ name: "main.py", content: "print(1)" }],
  });
  assert.equal(calls.length, 1);
  const sentFiles = (calls[0]!.body as { files: { name: string; content?: string }[] }).files;
  assert.equal(sentFiles[0]!.content, "print(1)");
});
