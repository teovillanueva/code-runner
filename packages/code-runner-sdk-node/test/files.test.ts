import { strict as assert } from "node:assert";
import { test } from "node:test";

import type { FileInput } from "@teovilla/code-runner-contract";
import { CodeRunnerClient } from "../src/client.ts";
import { toFileInput, toFileInputs } from "../src/files.ts";

/** A fetch fake that records the last request body and returns 202. */
function recorder(): { fetch: typeof globalThis.fetch; bodies: string[] } {
  const bodies: string[] = [];
  const fetch = (async (_url: string, init?: RequestInit) => {
    bodies.push(init?.body as string);
    return new Response(
      JSON.stringify({ jobId: "j", channel: "private-run-j", status: "queued" }),
      { status: 202, headers: { "Content-Type": "application/json" } },
    );
  }) as unknown as typeof globalThis.fetch;
  return { fetch, bodies };
}

test("toFileInput: a Buffer becomes base64 with encoding:base64", () => {
  const out = toFileInput({ name: "blob.bin", data: Buffer.from([0, 1, 2, 255]) });
  assert.equal(out.name, "blob.bin");
  assert.equal(out.encoding, "base64");
  assert.equal(out.content, Buffer.from([0, 1, 2, 255]).toString("base64"));
  assert.equal(out.content, "AAEC/w==");
});

test("toFileInput: a Uint8Array becomes base64", () => {
  const out = toFileInput({ name: "u.bin", data: new Uint8Array([104, 105]) });
  assert.equal(out.encoding, "base64");
  assert.equal(Buffer.from(out.content, "base64").toString(), "hi");
});

test("toFileInput: a text file passes through as utf8 (no encoding)", () => {
  const out = toFileInput({ name: "main.py", content: "print(1)" });
  assert.equal(out.content, "print(1)");
  assert.equal(out.encoding, undefined);
});

test("toFileInput: a raw wire FileInput is preserved as-is", () => {
  const raw: FileInput = { name: "x", content: "QQ==", encoding: "base64" };
  assert.deepEqual(toFileInput(raw), raw);
});

test("toFileInputs: mixed text + binary + raw", () => {
  const out = toFileInputs([
    { name: "main.py", content: "x" },
    { name: "b.bin", data: Buffer.from("hi") },
    { name: "raw.txt", content: "y", encoding: "utf8" },
  ]);
  assert.equal(out.length, 3);
  assert.equal(out[1]?.encoding, "base64");
  assert.equal(Buffer.from(out[1]!.content, "base64").toString(), "hi");
});

test("executeFiles: Buffer input serializes to encoding:base64 over the wire", async () => {
  const { fetch, bodies } = recorder();
  const client = new CodeRunnerClient({ baseUrl: "http://x", token: "t", fetch });

  await client.executeFiles({
    language: "python",
    files: [
      { name: "main.py", content: "open('data/blob.bin','rb').read()" },
      { name: "data/blob.bin", data: Buffer.from([0, 1, 2, 255]) },
    ],
  });

  const sent = JSON.parse(bodies[0]!) as {
    files: Array<{ name: string; content: string; encoding?: string }>;
  };
  const blob = sent.files.find((f) => f.name === "data/blob.bin")!;
  assert.equal(blob.encoding, "base64");
  assert.equal(blob.content, "AAEC/w==");
  const main = sent.files.find((f) => f.name === "main.py")!;
  assert.equal(main.encoding, undefined);
});
