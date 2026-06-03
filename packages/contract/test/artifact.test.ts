// Tests for the generated Artifact + RunResult zod validators (R1, R2).
// Run via: pnpm --filter @teovilla/code-runner-contract test
// Uses Node.js built-in test runner (node:test) — no external test framework.
//
// These validators are generated from packages/contract/schema/wire.schema.json.
// Import the generated zod module directly (gen/ts/schemas.ts) — it only imports
// zod, avoiding the index.ts -> gen/ts/types.js (.js, build-time) resolution that
// the experimental-strip-types test loader cannot resolve.

import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { ArtifactSchema, RunResultSchema } from "../gen/ts/schemas.ts";

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const validArtifact = {
  name: "plot.png",
  mimeType: "image/png",
  bytes: 1234,
  url: "https://example.invalid/artifacts/job-1/plot.png?sig=abc",
};

const validRunResult = {
  exitCode: 0,
  signal: null,
  timedOut: false,
  idleTimedOut: false,
  truncated: false,
  durationMs: 42,
  stdout: "done\n",
  stderr: "",
  artifacts: [validArtifact],
  artifactsTruncated: false,
};

// ---------------------------------------------------------------------------
// Artifact — valid parse (R1)
// ---------------------------------------------------------------------------

describe("ArtifactSchema — valid artifact", () => {
  it("parses a complete URL-only Artifact without error", () => {
    const parsed = ArtifactSchema.parse(validArtifact);
    assert.deepStrictEqual(parsed, validArtifact);
  });
});

// ---------------------------------------------------------------------------
// Artifact — url required (R1, D-01)
// ---------------------------------------------------------------------------

describe("ArtifactSchema — url is required", () => {
  it("rejects an Artifact missing the url field", () => {
    const { url, ...withoutUrl } = validArtifact;
    void url;
    const result = ArtifactSchema.safeParse(withoutUrl);
    assert.strictEqual(
      result.success,
      false,
      "expected an Artifact without `url` to fail zod validation",
    );
  });
});

// ---------------------------------------------------------------------------
// RunResult — round-trips with >= 1 artifact (R2)
// ---------------------------------------------------------------------------

describe("RunResultSchema — round-trip with an artifact", () => {
  it("parses a RunResult carrying one artifact and re-serializes equal", () => {
    const parsed = RunResultSchema.parse(validRunResult);
    assert.ok(
      Array.isArray(parsed.artifacts) && parsed.artifacts.length >= 1,
      "expected the parsed RunResult to carry >= 1 artifact",
    );
    // re-serialize equal (JSON round-trip through the validator)
    assert.deepStrictEqual(
      JSON.parse(JSON.stringify(parsed)),
      validRunResult,
    );
  });
});
