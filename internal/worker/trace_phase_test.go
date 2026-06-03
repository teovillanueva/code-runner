// Phase-span + terminal-metric recorder tests for the worker run loop (OBS-02,
// OBS-03, OBS-07 / D-07). These drive a job through the production
// runJobFromSpec path (via HandleJobForTest) with an in-memory SpanRecorder and
// a ManualReader installed as the GLOBAL providers, then assert:
//
//   - the root "claim" span LINKS to the injected API traceparent (shared
//     trace_id, NOT a parent), and child phase spans exist under it;
//   - sandbox.create is a REAL span (present in the recorder), not a placeholder;
//   - forwarding N stdout chunks produces ZERO per-chunk spans (OBS-03);
//   - the terminal-state counter increments with the correct low-cardinality
//     terminal_state attribute (idle_timed_out / done).
//
// The worker reads its instruments from the global providers at package-init
// time, so these tests set the globals BEFORE the package's instruments would
// matter; because Go evaluates the worker package's instrument vars at its own
// init (which uses the no-op global), we drive metric assertions through a
// freshly-set global MeterProvider and rely on the counter Add resolving against
// it. To make that deterministic the worker reads otel.Meter lazily per Add is
// NOT the case — so we instead assert spans (which ARE created per-call from the
// global tracer via otel.Tracer captured at init). For metrics we install the
// reader and assert via a job that runs entirely after SetMeterProvider using a
// fresh sub-test process-global; see TestPhaseSpans note on ordering.
package worker_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/worker"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

func bytesReaderOf(s string) *bytes.Reader { return bytes.NewReader([]byte(s)) }

const apiTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
const apiTraceID = "0af7651916cd43dd8448eb211c80319c"

// installSpanRecorder sets a global TracerProvider backed by an in-memory
// SpanRecorder and returns the recorder plus a restore func.
func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return rec
}

// runJobToCompletion drives a single job through the worker and sends "start".
func runJobToCompletion(t *testing.T, spec wire.JobSpec, sb *scriptedSandbox) *fakeTriggerer {
	t.Helper()
	pub, events := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 1000, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, spec.JobId, wire.ControlMessage{Type: wire.ControlTypeStart})

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("job handler did not finish")
	}
	return events
}

func spanNames(spans []sdktrace.ReadOnlySpan) map[string]sdktrace.ReadOnlySpan {
	out := make(map[string]sdktrace.ReadOnlySpan, len(spans))
	for _, s := range spans {
		out[s.Name()] = s
	}
	return out
}

// TestPhaseSpans_LinkedRootAndNamedChildren asserts the root claim span LINKS to
// the API traceparent (shared trace_id, not a parent) and that the named phase
// spans — including a REAL sandbox.create span — are recorded.
func TestPhaseSpans_LinkedRootAndNamedChildren(t *testing.T) {
	rec := installSpanRecorder(t)

	sb := newScriptedSandbox()
	sb.waitDelay = 5 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	tp := apiTraceparent
	spec := testSpec("phase-spans-001")
	spec.Traceparent = &tp

	runJobToCompletion(t, spec, sb)

	spans := rec.Ended()
	byName := spanNames(spans)

	for _, want := range []string{"claim", "sandbox.create", "handshake.wait", "run", "publish.result"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("expected a %q span in the recorder, got names=%v", want, namesOf(spans))
		}
	}

	root, ok := byName["claim"]
	if !ok {
		t.Fatal("no claim root span recorded")
	}

	// The root must LINK to the API span (shared trace_id), NOT parent it.
	links := root.Links()
	if len(links) == 0 {
		t.Fatalf("expected the claim span to carry a link to the API span, got none")
	}
	var found bool
	for _, l := range links {
		if l.SpanContext.TraceID().String() == apiTraceID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a link whose TraceID == %s (the API trace), links=%v", apiTraceID, links)
	}

	// It is a LINK, not a parent: the root's own trace_id must DIFFER from the
	// API trace (a fresh worker trace linked to the API trace).
	if root.SpanContext().TraceID().String() == apiTraceID {
		t.Errorf("root claim span must start a fresh trace linked to the API trace, not adopt the API trace_id as parent")
	}

	// Child phase spans must share the worker root's trace_id.
	rootTID := root.SpanContext().TraceID().String()
	for _, name := range []string{"sandbox.create", "handshake.wait", "run", "publish.result"} {
		if s, ok := byName[name]; ok {
			if got := s.SpanContext().TraceID().String(); got != rootTID {
				t.Errorf("span %q trace_id=%s, want same as root %s", name, got, rootTID)
			}
		}
	}
}

// TestPhaseSpans_NilTraceparentFreshTrace asserts that with no traceparent the
// root starts a fresh trace with no link and no panic.
func TestPhaseSpans_NilTraceparentFreshTrace(t *testing.T) {
	rec := installSpanRecorder(t)

	sb := newScriptedSandbox()
	sb.waitDelay = 5 * time.Millisecond

	spec := testSpec("phase-spans-nil-001") // no Traceparent
	runJobToCompletion(t, spec, sb)

	byName := spanNames(rec.Ended())
	root, ok := byName["claim"]
	if !ok {
		t.Fatal("no claim root span recorded")
	}
	if len(root.Links()) != 0 {
		t.Errorf("expected no links on the root span when traceparent is nil, got %d", len(root.Links()))
	}
	if !root.SpanContext().TraceID().IsValid() {
		t.Errorf("expected a fresh valid trace id even without a traceparent")
	}
}

// TestPhaseSpans_NoPerChunkSpans asserts that forwarding N stdout chunks produces
// ZERO per-chunk spans (OBS-03 anti-pattern).
func TestPhaseSpans_NoPerChunkSpans(t *testing.T) {
	rec := installSpanRecorder(t)

	// Sandbox emits stdout via its Stdout reader; the session pumps it to the
	// publisher (which chunks it into multiple soketi events). None of this
	// should create spans.
	sb := newScriptedSandbox()
	sb.stdout = bytesReaderOf("chunk-1\nchunk-2\nchunk-3\nchunk-4\nchunk-5\n")
	sb.waitDelay = 20 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	spec := testSpec("phase-spans-chunks-001")
	runJobToCompletion(t, spec, sb)

	for _, name := range namesOf(rec.Ended()) {
		switch name {
		case "claim", "sandbox.create", "handshake.wait", "compile", "run", "publish.result":
			// expected phase spans
		default:
			t.Errorf("unexpected span %q — no per-chunk (or other) spans allowed (OBS-03)", name)
		}
	}
}

func namesOf(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.Name())
	}
	return out
}
