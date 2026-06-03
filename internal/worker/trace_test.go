// Cross-language traceparent extract round-trip contract (Wave 0 foundation,
// now GREEN against production code).
//
// This test encodes the headline acceptance criterion of phase 08: a W3C
// traceparent written by the TS SDK and carried over Redis on the JobSpec must
// decode, in Go, to the SAME 16-byte trace_id. 08-01 introduced it against a
// test-only extract helper; 08-02 (this revision) re-points it at the PRODUCTION
// extractLinkedSpanContext so the same assertions now exercise the real code
// path used at runJobFromSpec.
//
// Security (RESEARCH Security V5 / threat T-08-03): traceparent crosses the
// Redis seam as untrusted input. Extraction must FAIL CLOSED — absent or
// malformed values yield an invalid SpanContext (fresh trace) and MUST NOT panic.
package worker

import (
	"testing"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// specWithTrace builds a JobSpec carrying the given (optional) traceparent /
// tracestate, mirroring how the API populates the carrier fields.
func specWithTrace(traceparent, tracestate *string) wire.JobSpec {
	return wire.JobSpec{
		JobId:       "trace-test",
		Traceparent: traceparent,
		Tracestate:  tracestate,
	}
}

func strPtr(s string) *string { return &s }

// TestTraceparentExtractRoundTrip is the cross-language correlation proof against
// PRODUCTION extractLinkedSpanContext: a known TS-format traceparent must extract
// to a SpanContext whose trace_id, in canonical lowercase hex, equals the
// trace_id embedded in the header.
func TestTraceparentExtractRoundTrip(t *testing.T) {
	const traceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	const wantTraceID = "0af7651916cd43dd8448eb211c80319c"
	const wantSpanID = "b7ad6b7169203331"

	sc := extractLinkedSpanContext(specWithTrace(strPtr(traceparent), nil))

	if !sc.IsValid() {
		t.Fatalf("expected a valid SpanContext from a well-formed traceparent, got invalid")
	}
	if got := sc.TraceID().String(); got != wantTraceID {
		t.Errorf("trace_id mismatch across the wire: got %q, want %q", got, wantTraceID)
	}
	if got := sc.SpanID().String(); got != wantSpanID {
		t.Errorf("span_id mismatch: got %q, want %q", got, wantSpanID)
	}
	if !sc.IsSampled() {
		t.Errorf("expected sampled flag (01) to be preserved")
	}
}

// TestTraceparentExtractPreservesTracestate asserts the optional tracestate
// companion field rides along and is recovered on extract.
func TestTraceparentExtractPreservesTracestate(t *testing.T) {
	const traceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	const tracestate = "vendor=opaque-value"

	sc := extractLinkedSpanContext(specWithTrace(strPtr(traceparent), strPtr(tracestate)))

	if !sc.IsValid() {
		t.Fatalf("expected a valid SpanContext, got invalid")
	}
	if got := sc.TraceState().String(); got != tracestate {
		t.Errorf("tracestate not preserved: got %q, want %q", got, tracestate)
	}
}

// TestTraceparentAbsentFailsClosed asserts the OTEL-off / backward-compatible
// no-op path: a nil traceparent (field absent on an old JobSpec) extracts to an
// invalid SpanContext (fresh trace) without panicking.
func TestTraceparentAbsentFailsClosed(t *testing.T) {
	sc := extractLinkedSpanContext(specWithTrace(nil, nil))
	if sc.IsValid() {
		t.Errorf("expected an invalid SpanContext when traceparent is absent, got valid")
	}
}

// TestTraceparentMalformedFailsClosed asserts the security fail-closed path
// (threat T-08-03): malformed/oversized traceparent values must yield an invalid
// SpanContext without panicking, so the worker falls back to a fresh trace.
func TestTraceparentMalformedFailsClosed(t *testing.T) {
	cases := map[string]string{
		"empty string":          "",
		"garbage":               "not-a-traceparent",
		"truncated":             "00-0af7651916cd43dd8448eb211c80319c",
		"all-zero trace_id":     "00-00000000000000000000000000000000-b7ad6b7169203331-01",
		"all-zero span_id":      "00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01",
		"non-hex trace_id":      "00-zzf7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"oversized (8KB+ junk)": "00-" + makeLongHex(8192),
	}
	for name, tp := range cases {
		t.Run(name, func(t *testing.T) {
			// Must not panic on any untrusted input.
			sc := extractLinkedSpanContext(specWithTrace(strPtr(tp), nil))
			if sc.IsValid() {
				t.Errorf("expected an invalid SpanContext for malformed traceparent %q, got valid", name)
			}
		})
	}
}

func makeLongHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
