import { strict as assert } from "node:assert";
import { test } from "node:test";

import type { RunResult } from "@teovilla/code-runner-contract";
import { CodeRunnerClient } from "../src/client.ts";
import { NotFoundError } from "../src/errors.ts";

const SAMPLE_RESULT: RunResult = {
  exitCode: 0,
  signal: null,
  timedOut: false,
  idleTimedOut: false,
  truncated: false,
  durationMs: 42,
  stdout: "hello\n",
  stderr: "",
  artifacts: [
    {
      name: "plot.png",
      mimeType: "image/png",
      bytes: 1234,
      url: "https://storage.example/presigned/plot.png",
    },
  ],
  artifactsTruncated: false,
};

test("getOutput returns the typed RunResult and sends the bearer on a 200", async () => {
  let captured: { url?: string; auth?: string; method?: string } = {};
  const fetchImpl = (async (url: string, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    captured = {
      url,
      method: init?.method,
      auth: headers.get("Authorization") ?? undefined,
    };
    return new Response(JSON.stringify(SAMPLE_RESULT), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as unknown as typeof globalThis.fetch;

  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "secret-token",
    fetch: fetchImpl,
  });

  const res = await client.getOutput("job 1");

  // Path is GET /v1/jobs/:id/output with the id URL-encoded.
  assert.equal(captured.method, "GET");
  assert.equal(captured.url, "http://x/v1/jobs/job%201/output");
  // Bearer is auto-attached by request().
  assert.equal(captured.auth, "Bearer secret-token");

  // Typed RunResult round-trips, including the typed Artifact[].
  assert.equal(res.exitCode, 0);
  assert.equal(res.stdout, "hello\n");
  assert.equal(res.artifacts.length, 1);
  assert.equal(res.artifacts[0]?.name, "plot.png");
  assert.equal(res.artifacts[0]?.url, "https://storage.example/presigned/plot.png");
  assert.equal(res.artifactsTruncated, false);
});

test("getOutput rejects with NotFoundError on a 404", async () => {
  const fetchImpl = (async () =>
    new Response(JSON.stringify({ error: "no job" }), {
      status: 404,
      headers: { "Content-Type": "application/json" },
    })) as unknown as typeof globalThis.fetch;

  const client = new CodeRunnerClient({
    baseUrl: "http://x",
    token: "t",
    fetch: fetchImpl,
  });

  await assert.rejects(() => client.getOutput("nope"), NotFoundError);
});
