package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/teovillanueva/code-runner/internal/logging"
)

// validSpanCtx builds a context carrying a known-valid SpanContext so we can
// assert the handler echoes its trace_id/span_id verbatim.
func validSpanCtx(t *testing.T) (context.Context, trace.SpanContext) {
	t.Helper()
	tid, err := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	if err != nil {
		t.Fatalf("bad trace id: %v", err)
	}
	sid, err := trace.SpanIDFromHex("b7ad6b7169203331")
	if err != nil {
		t.Fatalf("bad span id: %v", err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(context.Background(), sc), sc
}

func logOne(t *testing.T, ctx context.Context) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	h := logging.NewCtxHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(h)
	logger.InfoContext(ctx, "hello", "k", "v")

	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("handler output is not valid JSON: %v\nraw: %q", err, buf.String())
	}
	return out
}

// TestHandler_InjectsTraceFieldsWithinSpan asserts that a record logged within a
// valid span context yields JSON whose trace_id/span_id equal that span context.
func TestHandler_InjectsTraceFieldsWithinSpan(t *testing.T) {
	ctx, sc := validSpanCtx(t)
	out := logOne(t, ctx)

	if got := out["trace_id"]; got != sc.TraceID().String() {
		t.Errorf("trace_id mismatch: got %v, want %s", got, sc.TraceID().String())
	}
	if got := out["span_id"]; got != sc.SpanID().String() {
		t.Errorf("span_id mismatch: got %v, want %s", got, sc.SpanID().String())
	}
}

// TestHandler_OmitsTraceFieldsWithoutSpan asserts that logging WITHOUT a span
// context omits the keys entirely — no empty/zero values are forced.
func TestHandler_OmitsTraceFieldsWithoutSpan(t *testing.T) {
	out := logOne(t, context.Background())

	if _, ok := out["trace_id"]; ok {
		t.Errorf("expected no trace_id key without a span context, got %v", out["trace_id"])
	}
	if _, ok := out["span_id"]; ok {
		t.Errorf("expected no span_id key without a span context, got %v", out["span_id"])
	}
	if _, ok := out["job_id"]; ok {
		t.Errorf("expected no job_id key without a job id, got %v", out["job_id"])
	}
}

// TestHandler_InjectsJobID asserts a job_id placed on the context via WithJobID
// appears as "job_id" in the JSON.
func TestHandler_InjectsJobID(t *testing.T) {
	ctx := logging.WithJobID(context.Background(), "job-123")
	out := logOne(t, ctx)

	if got := out["job_id"]; got != "job-123" {
		t.Errorf("job_id mismatch: got %v, want job-123", got)
	}
}

// TestHandler_OutputIsValidJSON asserts the wrapped JSON handler always produces
// valid JSON regardless of OTEL state (D-03 stdout-always). The logOne helper
// already json.Unmarshal-s the output, so reaching here with both ctx shapes
// proves validity; we also assert the message survives.
func TestHandler_OutputIsValidJSON(t *testing.T) {
	ctx, _ := validSpanCtx(t)
	out := logOne(t, ctx)
	if out["msg"] != "hello" {
		t.Errorf("expected msg=hello, got %v", out["msg"])
	}
	if out["k"] != "v" {
		t.Errorf("expected attribute k=v to survive, got %v", out["k"])
	}
}
