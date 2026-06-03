// Output-bytes counter test (OBS-06 / D-05). Drives a job whose sandbox emits
// stdout through the production run loop and asserts, via an in-memory
// ManualReader, that code_runner.output.bytes records the forwarded byte volume
// — and that NO per-chunk spans are produced (RESEARCH anti-pattern: per-chunk
// spans are forbidden; output volume is a counter, not a span-per-chunk).
package worker_test

import (
	"bytes"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/teovillanueva/code-runner/internal/runner"
)

// outputBytesValue returns the summed value of code_runner.output.bytes and
// asserts no job_id attribute leaked onto it (low-cardinality invariant).
func outputBytesValue(t *testing.T, rm metricdata.ResourceMetrics) int64 {
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
				if _, bad := dp.Attributes.Value("job_id"); bad {
					t.Errorf("code_runner.output.bytes must NOT carry a job_id attribute")
				}
				total += dp.Value
			}
		}
	}
	return total
}

// TestOutputBytesCounter_NonzeroAndNoPerChunkSpans asserts that an interactive
// job emitting stdout produces a nonzero code_runner.output.bytes value equal to
// the forwarded byte count, and that the recorded trace contains ZERO per-chunk
// spans (only the named worker phase spans).
func TestOutputBytesCounter_NonzeroAndNoPerChunkSpans(t *testing.T) {
	collect := installManualReader(t)
	rec := installSpanRecorder(t)

	const payload = "line-1\nline-2\nline-3\nhello-from-sandbox\n"
	sb := newScriptedSandbox()
	sb.stdout = bytes.NewReader([]byte(payload))
	sb.waitDelay = 20 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	spec := testSpec("metrics-output-bytes-001")
	// Ensure a generous output budget so all bytes are forwarded (not truncated).
	spec.Limits.OutputKb = 256
	runJobToCompletion(t, spec, sb)

	// ── Counter assertion: forwarded bytes == payload length, and nonzero. ──
	rm := collect()
	got := outputBytesValue(t, rm)
	if got != int64(len(payload)) {
		t.Errorf("code_runner.output.bytes = %d; want %d (forwarded payload length)", got, len(payload))
	}
	if got == 0 {
		t.Errorf("expected a nonzero code_runner.output.bytes for a job that emitted stdout")
	}

	// ── No per-chunk spans: every recorded span must be a named phase span. ──
	allowed := map[string]bool{
		"claim":          true,
		"sandbox.create": true,
		"handshake.wait": true,
		"compile":        true,
		"run":            true,
		"publish.result": true,
	}
	for name := range spanNames(rec.Ended()) {
		if !allowed[name] {
			t.Errorf("unexpected span %q — output must be a counter, never per-chunk spans", name)
		}
	}
}
