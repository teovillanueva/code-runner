// Reaper orphan-counter instrument test (OBS-06 / D-05). The production reap
// path (reapContainer) increments this counter after a real Docker
// ContainerRemove and is exercised by the //go:build reaper_integration suite.
// This Docker-FREE unit test asserts the INSTRUMENT it increments is correct and
// deterministic via an in-memory ManualReader: code_runner.reaper.orphans is an
// Int64 counter carrying NO attributes (low-cardinality by design; never a
// job_id or container_id).
package reaper

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestReaperOrphansCounter(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})

	// Increment into the same instrument reapContainer() uses.
	reaperOrphans().Add(context.Background(), 1)
	reaperOrphans().Add(context.Background(), 1)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var total int64
	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "code_runner.reaper.orphans" {
				continue
			}
			found = true
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("expected Sum[int64] for code_runner.reaper.orphans, got %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if dp.Attributes.Len() != 0 {
					t.Errorf("code_runner.reaper.orphans must carry NO attributes, got %d", dp.Attributes.Len())
				}
				total += dp.Value
			}
		}
	}
	if !found {
		t.Fatal("code_runner.reaper.orphans counter not recorded")
	}
	if total != 2 {
		t.Errorf("code_runner.reaper.orphans = %d; want 2", total)
	}
}
