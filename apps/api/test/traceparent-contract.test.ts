// Contract test: the generated zod JobSpec schema treats traceparent/tracestate
// as OPTIONAL W3C trace-context strings (phase 08, decisions D-11 + D-12).
//
// This is the TS half of the Wave 0 cross-language trace-carrier contract. The
// Go half (internal/worker/trace_test.go) proves byte-format equivalence on
// extract; this half proves the carrier is wire-optional so:
//   1. old JobSpec messages without traceparent still validate (no-op when OTEL
//      is off — backward compatible), and
//   2. when present, the W3C string value is preserved through validation.
//
// No secret/code/stdin appears in any fixture (threat T-08-02): only the
// non-secret W3C identifiers are exercised.

import { describe, it, expect } from "vitest";
import { JobSpecSchema } from "@teovilla/code-runner-contract";

// A minimal, valid JobSpec WITHOUT the optional trace fields. Mirrors what the
// API enqueued before phase 08 existed.
const baseSpec = {
  jobId: "job-abc",
  channel: "private-run-job-abc",
  language: "python",
  version: "3.12",
  image: "code-runner/python:3.12",
  entrypoint: "main.py",
  compile: null,
  run: ["python", "main.py"],
  interactive: true,
  files: [{ name: "main.py", content: "print('hi')" }],
  limits: {
    wallTimeMs: 10000,
    idleMs: 5000,
    cpuMs: 5000,
    memoryMb: 256,
    pids: 64,
    outputKb: 1024,
  },
  enqueuedAtMs: 1_700_000_000_000,
} as const;

describe("JobSpec traceparent/tracestate contract", () => {
  it("parses a spec WITHOUT traceparent (backward-compatible no-op)", () => {
    const parsed = JobSpecSchema.parse(baseSpec);
    expect(parsed.traceparent).toBeUndefined();
    expect(parsed.tracestate).toBeUndefined();
  });

  it("parses a spec WITH traceparent and preserves the W3C string value", () => {
    const traceparent =
      "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01";
    const tracestate = "vendor=opaque-value";
    const parsed = JobSpecSchema.parse({ ...baseSpec, traceparent, tracestate });
    expect(parsed.traceparent).toBe(traceparent);
    expect(parsed.tracestate).toBe(tracestate);
  });

  it("treats traceparent as optional in the schema shape", () => {
    // The field key being absent from the object must NOT fail validation:
    // a present-but-undefined value and a missing key both succeed.
    expect(() => JobSpecSchema.parse(baseSpec)).not.toThrow();
    expect(() =>
      JobSpecSchema.parse({ ...baseSpec, traceparent: undefined }),
    ).not.toThrow();
  });

  it("rejects a non-string traceparent (still a typed W3C string field)", () => {
    expect(() =>
      JobSpecSchema.parse({ ...baseSpec, traceparent: 12345 }),
    ).toThrow();
  });
});
