// Soketi-publish metrics test (OBS-06 / D-05 / D-06). Drives Trigger calls
// through the instrumented publisher and asserts, via an in-memory ManualReader
// installed as the GLOBAL MeterProvider, that:
//
//   - code_runner.publish.duration records a histogram sample per Trigger, and
//   - code_runner.publish.errors increments on a forced Trigger error,
//
// with NO job_id (or any high-cardinality) attribute on either instrument.
package publisher

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// installManualReader installs a global MeterProvider backed by a ManualReader
// and returns a collect func; restored via t.Cleanup.
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

// errTriggerer always returns the supplied error from Trigger (and still records
// the call) — used to force the publish-error path.
type errTriggerer struct {
	calls int
	err   error
}

func (e *errTriggerer) Trigger(channel, event string, data interface{}) error {
	e.calls++
	return e.err
}

// histogramCount returns the total Count across all data points of the named
// Float64 histogram, and asserts no job_id attribute is present.
func histogramCount(t *testing.T, rm metricdata.ResourceMetrics, name string) uint64 {
	t.Helper()
	var total uint64
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
				total += dp.Count
			}
		}
	}
	return total
}

// counterValue returns the summed value of the named Int64 counter and asserts
// no job_id attribute is present.
func counterValue(t *testing.T, rm metricdata.ResourceMetrics, name string) int64 {
	t.Helper()
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("expected Sum[int64] for %s, got %T", name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				if _, bad := dp.Attributes.Value("job_id"); bad {
					t.Errorf("%s must NOT carry a job_id attribute", name)
				}
				total += dp.Value
			}
		}
	}
	return total
}

// TestPublishDuration_Recorded asserts a successful Stage Trigger records a
// publish-duration histogram sample and does NOT increment publish.errors.
func TestPublishDuration_Recorded(t *testing.T) {
	collect := installManualReader(t)

	p, fake := newTestPublisher(t)
	require.NoError(t, p.Stage("job-pub-ok", wire.StagePhaseQueued))
	require.Len(t, fake.snapshot(), 1)

	rm := collect()
	if got := histogramCount(t, rm, "code_runner.publish.duration"); got < 1 {
		t.Errorf("expected >= 1 code_runner.publish.duration sample, got %d", got)
	}
	assert.Equal(t, int64(0), counterValue(t, rm, "code_runner.publish.errors"),
		"publish.errors must stay 0 on a successful Trigger")
}

// TestPublishErrors_IncrementOnForcedError asserts a forced Trigger error
// increments code_runner.publish.errors AND still records a publish.duration
// sample (the call was timed regardless of outcome).
func TestPublishErrors_IncrementOnForcedError(t *testing.T) {
	collect := installManualReader(t)

	fake := &errTriggerer{err: errors.New("soketi boom")}
	p, err := newWithTriggerer(config.Config{}, fake)
	require.NoError(t, err)

	// Trigger an error via Result (single Trigger, no chunk splitting).
	gotErr := p.Result("job-pub-err", wire.ResultEvent{})
	require.Error(t, gotErr, "the forced Trigger error must propagate")
	require.Equal(t, 1, fake.calls)

	rm := collect()
	if got := counterValue(t, rm, "code_runner.publish.errors"); got < 1 {
		t.Errorf("expected code_runner.publish.errors >= 1 on a forced error, got %d", got)
	}
	if got := histogramCount(t, rm, "code_runner.publish.duration"); got < 1 {
		t.Errorf("expected the failed Trigger to still record a publish.duration sample, got %d", got)
	}
}
