// Tests for the generated FileInput zod validator — the encoding field
// (FILES-02) and subdir-aware name. Run via:
//   pnpm --filter @teovilla/code-runner-contract test
//
// Validators are generated from packages/contract/schema/wire.schema.json.
// Import the generated zod module directly (see artifact.test.ts for why).

import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { FileInputSchema } from "../gen/ts/schemas.ts";

describe("FileInput.encoding", () => {
  it("defaults encoding to utf8 when absent (back-compat)", () => {
    const parsed = FileInputSchema.parse({ name: "main.py", content: "print(1)" });
    assert.equal(parsed.encoding, "utf8");
  });

  it("accepts explicit utf8", () => {
    const parsed = FileInputSchema.parse({
      name: "a.txt",
      content: "hi",
      encoding: "utf8",
    });
    assert.equal(parsed.encoding, "utf8");
  });

  it("accepts base64", () => {
    const parsed = FileInputSchema.parse({
      name: "blob.bin",
      content: "AAEC/w==",
      encoding: "base64",
    });
    assert.equal(parsed.encoding, "base64");
  });

  it("rejects an unknown encoding", () => {
    assert.throws(() =>
      FileInputSchema.parse({ name: "a.txt", content: "x", encoding: "rot13" }),
    );
  });

  it("accepts a subdir path in name", () => {
    const parsed = FileInputSchema.parse({ name: "data/input.csv", content: "a,b" });
    assert.equal(parsed.name, "data/input.csv");
  });

  it("still requires name (content is now optional — XOR with ref)", () => {
    // name remains mandatory.
    assert.throws(() => FileInputSchema.parse({ content: "x" }));
    // content is OPTIONAL at the schema level: a FileInput carries EXACTLY ONE
    // of content/ref, but that XOR is NOT expressible in the generated zod
    // schema (both become optional). It is enforced at runtime in the API
    // (zod .refine on the request) and the worker. So a bare { name } parses
    // here — the XOR rejection lives one layer up.
    const bare = FileInputSchema.parse({ name: "x" });
    assert.equal(bare.name, "x");
  });

  it("accepts a content-addressed ref (sha256:<64hex>)", () => {
    const h = "sha256:" + "a".repeat(64);
    const parsed = FileInputSchema.parse({ name: "big.csv", ref: h });
    assert.equal(parsed.ref, h);
  });

  it("rejects a malformed ref", () => {
    assert.throws(() =>
      FileInputSchema.parse({ name: "big.csv", ref: "sha256:zzz" }),
    );
    assert.throws(() =>
      // bare hex without the sha256: prefix is rejected
      FileInputSchema.parse({ name: "big.csv", ref: "a".repeat(64) }),
    );
  });
});
