// Sandbox latency-histogram metrics test (OBS-06 / D-05 / D-06).
//
// The production Create/Kill paths talk to the Docker daemon and are exercised
// by the //go:build docker integration suite. This Docker-FREE unit test asserts
// the INSTRUMENTS those paths record into are correct and deterministic via an
// in-memory ManualReader installed as the global MeterProvider:
//
//   - code_runner.sandbox.create.duration is a Float64 HISTOGRAM (unit "s"),
//     distinct from the 08-02 sandbox.create SPAN,
//   - code_runner.sandbox.kill.duration   is a Float64 HISTOGRAM (unit "s"),
//   - both carry the low-cardinality `language` attribute and NEVER a job_id.
//
// It is an internal (package runner) test so it can drive the unexported
// instrument helpers that Create/Kill use, recording the same measurements the
// production code records.
package runner

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func installManualReader(t *testing.T) func() metricdata.ResourceMetrics {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})
	return func() metricdata.ResourceMetrics {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect metrics: %v", err)
		}
		return rm
	}
}

// findHistogram returns the named Float64 histogram and asserts it carries a
// `language` attribute equal to wantLang and NO job_id attribute.
func findHistogram(t *testing.T, rm metricdata.ResourceMetrics, name, wantLang string) (metricdata.Histogram[float64], bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("expected Histogram[float64] for %s, got %T", name, m.Data)
			}
			for _, dp := range h.DataPoints {
				if _, bad := dp.Attributes.Value("job_id"); bad {
					t.Errorf("%s must NOT carry a job_id attribute", name)
				}
				if v, ok := dp.Attributes.Value("language"); !ok || v.AsString() != wantLang {
					t.Errorf("%s: expected language=%q attribute, got %v (present=%v)", name, wantLang, v.AsString(), ok)
				}
			}
			return h, true
		}
	}
	return metricdata.Histogram[float64]{}, false
}

// TestSandboxCreateDurationHistogram asserts the create-latency instrument is a
// recorded Float64 histogram with the low-cardinality language attribute.
func TestSandboxCreateDurationHistogram(t *testing.T) {
	collect := installManualReader(t)

	// Record into the same instrument Create() uses, with the same language attr.
	sandboxCreateDuration().Record(context.Background(), 0.0123, langAttr("python"))

	rm := collect()
	h, found := findHistogram(t, rm, "code_runner.sandbox.create.duration", "python")
	if !found {
		t.Fatal("code_runner.sandbox.create.duration histogram not recorded")
	}
	var count uint64
	for _, dp := range h.DataPoints {
		count += dp.Count
	}
	if count < 1 {
		t.Errorf("expected >= 1 create-duration sample, got %d", count)
	}
}

// TestSandboxKillDurationHistogram asserts the kill-latency instrument is a
// recorded Float64 histogram with the low-cardinality language attribute.
func TestSandboxKillDurationHistogram(t *testing.T) {
	collect := installManualReader(t)

	sandboxKillDuration().Record(context.Background(), 0.0042, langAttr("go"))

	rm := collect()
	h, found := findHistogram(t, rm, "code_runner.sandbox.kill.duration", "go")
	if !found {
		t.Fatal("code_runner.sandbox.kill.duration histogram not recorded")
	}
	var count uint64
	for _, dp := range h.DataPoints {
		count += dp.Count
	}
	if count < 1 {
		t.Errorf("expected >= 1 kill-duration sample, got %d", count)
	}
}
