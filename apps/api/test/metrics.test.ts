// Tests for the API rejection counters (metrics.ts) — OBS-06 / D-05 / D-06.
//
// D-05: admission-429 and ratelimit-429 rejections increment OTel counters.
// D-06: the counters use the `code_runner.*` dotted namespace + a `{request}`
//       unit (`code_runner.admission.rejected`, `code_runner.ratelimit.rejected`).
//
// Anti-pattern guard (T-08-10b): metric attributes stay low-cardinality. The
// ratelimit counter carries only a `reason` attribute (`frame_rate`/`byte_cap`);
// NO `job_id` (or any unbounded string) is ever attached to a metric point.
//
// Test seam: we install an InMemoryMetricExporter-backed MeterProvider as the
// global API MeterProvider (the same metrics namespace re-exported from
// @opentelemetry/sdk-node that 08-03 verified — A2). The module-under-test
// reads counters from `metrics.getMeter(...)` lazily, so it binds to whichever
// provider is global when the counter is first used.

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { metrics as apiMetrics } from "@opentelemetry/api";
import { metrics as sdkMetrics } from "@opentelemetry/sdk-node";

type Reader = InstanceType<typeof sdkMetrics.PeriodicExportingMetricReader>;
type Exporter = InstanceType<typeof sdkMetrics.InMemoryMetricExporter>;
type Provider = InstanceType<typeof sdkMetrics.MeterProvider>;

let exporter: Exporter;
let reader: Reader;
let provider: Provider;

async function collectPoints(metricName: string) {
  await reader.forceFlush();
  const collected = exporter.getMetrics();
  const points: { value: number; attributes: Record<string, unknown> }[] = [];
  let unit: string | undefined;
  for (const rm of collected) {
    for (const sm of rm.scopeMetrics) {
      for (const m of sm.metrics) {
        if (m.descriptor.name === metricName) {
          unit = m.descriptor.unit;
          for (const dp of m.dataPoints) {
            points.push({
              value: dp.value as number,
              attributes: dp.attributes as Record<string, unknown>,
            });
          }
        }
      }
    }
  }
  return { points, unit };
}

describe("metrics.ts — API rejection counters (OBS-06 / D-05 / D-06)", () => {
  beforeAll(() => {
    exporter = new sdkMetrics.InMemoryMetricExporter(
      sdkMetrics.AggregationTemporality.CUMULATIVE,
    );
    reader = new sdkMetrics.PeriodicExportingMetricReader({
      exporter,
      // Large interval — we drive collection manually via forceFlush().
      exportIntervalMillis: 60_000,
    });
    provider = new sdkMetrics.MeterProvider({ readers: [reader] });
    apiMetrics.setGlobalMeterProvider(provider);
  });

  afterAll(async () => {
    await provider.shutdown();
    apiMetrics.disable();
  });

  it("increments code_runner.admission.rejected on an admission rejection", async () => {
    const { admissionRejections } = await import("../src/metrics.ts");
    admissionRejections.add(1);
    admissionRejections.add(1);

    const { points, unit } = await collectPoints(
      "code_runner.admission.rejected",
    );
    expect(points.length).toBeGreaterThan(0);
    expect(unit).toBe("{request}");
    const total = points.reduce((s, p) => s + p.value, 0);
    expect(total).toBe(2);
    // Low-cardinality: admission rejection carries no attributes (no job_id).
    for (const p of points) {
      expect(Object.keys(p.attributes)).not.toContain("job_id");
    }
  });

  it("increments code_runner.ratelimit.rejected with a low-cardinality reason", async () => {
    const { ratelimitRejections } = await import("../src/metrics.ts");
    ratelimitRejections.add(1, { reason: "frame_rate" });
    ratelimitRejections.add(1, { reason: "byte_cap" });
    ratelimitRejections.add(1, { reason: "frame_rate" });

    const { points } = await collectPoints("code_runner.ratelimit.rejected");
    expect(points.length).toBeGreaterThan(0);

    const byReason = new Map<string, number>();
    for (const p of points) {
      const reason = p.attributes["reason"] as string;
      byReason.set(reason, (byReason.get(reason) ?? 0) + p.value);
      // Never job_id (or any other high-cardinality key) on the metric point.
      expect(Object.keys(p.attributes)).not.toContain("job_id");
      // Only the `reason` attribute is permitted on the rejection counter.
      expect(Object.keys(p.attributes)).toEqual(["reason"]);
    }
    expect(byReason.get("frame_rate")).toBe(2);
    expect(byReason.get("byte_cap")).toBe(1);
  });
});
