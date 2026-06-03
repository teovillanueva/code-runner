// Terminal-state counter recorder test (D-07 / OBS-06). Drives a job to a
// terminal state through the production run loop and asserts the
// code_runner.jobs.terminal counter increments with the correct low-cardinality
// terminal_state attribute, via an in-memory ManualReader installed as the
// GLOBAL MeterProvider.
//
// The global otel.Meter()/Int64Counter the worker captured at package init are
// DELEGATING instruments: when a real MeterProvider is registered via
// otel.SetMeterProvider, subsequent Add calls route to it. So installing the
// reader here is sufficient to observe the worker's counter.
package worker_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/teovillanueva/code-runner/internal/runner"
)

// installManualReader sets a global MeterProvider backed by a ManualReader and
// returns a collect func plus restore via t.Cleanup.
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

// terminalCount returns the summed value of code_runner.jobs.terminal data
// points whose terminal_state attribute equals want.
func terminalCount(t *testing.T, rm metricdata.ResourceMetrics, want string) int64 {
	t.Helper()
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "code_runner.jobs.terminal" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("expected Sum[int64] for code_runner.jobs.terminal, got %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("terminal_state"); ok && v.AsString() == want {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// TestTerminalCounter_IdleTimedOut asserts an idle-killed terminal path
// increments code_runner.jobs.terminal{terminal_state=idle_timed_out}.
//
// The idle outcome is produced by the REAL idle clock (the session overwrites
// the result flags by termination reason — see session/lifecycle.go), so we make
// the sandbox silent with a long Wait and rely on testSpec's short IdleMs (300ms)
// to fire the idle clock.
func TestTerminalCounter_IdleTimedOut(t *testing.T) {
	collect := installManualReader(t)

	sb := newScriptedSandbox()
	sb.waitDelay = 10 * time.Second // never returns normally before the idle clock
	// no stdout/stderr → no activity → idle clock fires at IdleMs

	spec := testSpec("metrics-idle-001")
	runJobToCompletion(t, spec, sb)

	rm := collect()
	if got := terminalCount(t, rm, "idle_timed_out"); got < 1 {
		t.Errorf("expected code_runner.jobs.terminal{terminal_state=idle_timed_out} >= 1, got %d", got)
	}
}

// TestTerminalCounter_Done asserts a normal completion increments
// code_runner.jobs.terminal{terminal_state=done}.
func TestTerminalCounter_Done(t *testing.T) {
	collect := installManualReader(t)

	sb := newScriptedSandbox()
	sb.waitDelay = 5 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	spec := testSpec("metrics-done-001")
	runJobToCompletion(t, spec, sb)

	rm := collect()
	if got := terminalCount(t, rm, "done"); got < 1 {
		t.Errorf("expected code_runner.jobs.terminal{terminal_state=done} >= 1, got %d", got)
	}
}

// TestQueueTime_Recorded asserts the time-in-queue histogram records a sample
// when the spec carries enqueuedAtMs.
func TestQueueTime_Recorded(t *testing.T) {
	collect := installManualReader(t)

	sb := newScriptedSandbox()
	sb.waitDelay = 5 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	spec := testSpec("metrics-queue-001")
	spec.EnqueuedAtMs = int(time.Now().Add(-200 * time.Millisecond).UnixMilli())
	runJobToCompletion(t, spec, sb)

	rm := collect()
	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "code_runner.queue.time" {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("expected Histogram[float64] for code_runner.queue.time, got %T", m.Data)
			}
			for _, dp := range h.DataPoints {
				if dp.Count >= 1 {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected at least one code_runner.queue.time sample")
	}

	// Ensure no high-cardinality job_id leaked onto the terminal counter (RESEARCH
	// anti-pattern). terminal_state is the only attribute allowed.
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "code_runner.jobs.terminal" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			for _, dp := range sum.DataPoints {
				if _, ok := dp.Attributes.Value("job_id"); ok {
					t.Errorf("job_id must NOT be a metric attribute on code_runner.jobs.terminal")
				}
			}
		}
	}
}
