// Pump output-bytes counter test (OBS-06 / D-05). Asserts the pump increments
// code_runner.output.bytes by the number of bytes FORWARDED to the sink (within
// budget) — and only forwarded bytes, so truncated/discarded bytes are excluded.
// The counter carries NO attributes (job_id must never be a metric dimension).
package session_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/teovillanueva/code-runner/internal/session"
)

func installPumpReader(t *testing.T) func() metricdata.ResourceMetrics {
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
			t.Fatalf("collect: %v", err)
		}
		return rm
	}
}

func outputBytes(t *testing.T, rm metricdata.ResourceMetrics) int64 {
	t.Helper()
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "code_runner.output.bytes" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("expected Sum[int64] for code_runner.output.bytes, got %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if dp.Attributes.Len() != 0 {
					t.Errorf("code_runner.output.bytes must carry NO attributes, got %d", dp.Attributes.Len())
				}
				total += dp.Value
			}
		}
	}
	return total
}

// TestPumpOutputBytes_CountsForwardedBytes asserts the under-cap forward path
// increments the counter by exactly the forwarded byte count.
func TestPumpOutputBytes_CountsForwardedBytes(t *testing.T) {
	collect := installPumpReader(t)

	data := []byte("the quick brown fox\n")
	budget := &atomic.Int64{}
	budget.Store(1024)
	truncated := &atomic.Bool{}

	p := session.NewPump(bytes.NewReader(data), budget, truncated, func([]byte) {}, nil)
	if err := p.Run(); err != nil {
		t.Fatalf("pump.Run: %v", err)
	}

	if got := outputBytes(t, collect()); got != int64(len(data)) {
		t.Errorf("code_runner.output.bytes = %d; want %d (forwarded bytes)", got, len(data))
	}
}

// TestPumpOutputBytes_ExcludesTruncated asserts that when the budget is exceeded,
// only the bytes actually forwarded (== budget) are counted, not the discarded
// overflow.
func TestPumpOutputBytes_ExcludesTruncated(t *testing.T) {
	collect := installPumpReader(t)

	const cap = 8
	data := []byte("0123456789ABCDEF") // 16 bytes; only `cap` are forwarded
	budget := &atomic.Int64{}
	budget.Store(cap)
	truncated := &atomic.Bool{}

	p := session.NewPump(bytes.NewReader(data), budget, truncated, func([]byte) {}, nil)
	if err := p.Run(); err != nil {
		t.Fatalf("pump.Run: %v", err)
	}

	if !truncated.Load() {
		t.Error("expected truncated=true when input exceeds budget")
	}
	if got := outputBytes(t, collect()); got != cap {
		t.Errorf("code_runner.output.bytes = %d; want %d (only forwarded/within-budget bytes)", got, cap)
	}
}
