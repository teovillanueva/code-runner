// Package logging provides a stdout slog.Handler that injects trace correlation
// fields (trace_id/span_id/job_id) from the context into every JSON log line.
//
// Why a custom handler (RESEARCH Pitfall 4 / D-03): the official otelslog bridge
// converts slog records to OTel LogRecords for the OTLP pipeline ONLY — it does
// not write stdout. OBS-07 requires stdout JSON to always carry trace_id, so we
// wrap slog.JSONHandler here. This handler is installed unconditionally (D-03
// "stdout always"), independent of whether OTLP export is configured; when there
// is no active span the correlation keys are simply omitted (no empty/zero
// values are forced).
//
// Security (RESEARCH V7/V8, threat T-08-04): only the non-secret identifiers
// trace_id/span_id/job_id are injected. No secret (EXECUTOR_API_TOKEN,
// SOKETI_APP_SECRET) and no user code/stdin is ever read or attributed here.
package logging

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// jobIDKeyType is an unexported context-key type so the key cannot collide with
// keys from other packages.
type jobIDKeyType struct{}

// jobIDKey is the context key under which a job ID is stored for log correlation.
var jobIDKey = jobIDKeyType{}

// WithJobID returns a child context carrying jobID, so log lines emitted with
// that context include a "job_id" field. job_id rides on logs/spans only — never
// on metrics (RESEARCH anti-pattern: job_id is high-cardinality).
func WithJobID(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, jobIDKey, jobID)
}

// JobIDFromContext returns the job ID stored in ctx (if any).
func JobIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(jobIDKey).(string)
	return v, ok
}

// SpanContextFromContext is re-exported for callers/tests that need the same
// span-context extraction the handler uses.
func SpanContextFromContext(ctx context.Context) trace.SpanContext {
	return trace.SpanContextFromContext(ctx)
}

// ctxHandler wraps a base slog.Handler (a JSON handler in practice) and injects
// trace_id/span_id/job_id pulled from the record's context.
type ctxHandler struct {
	slog.Handler
}

// NewCtxHandler wraps base so that every record gains trace_id/span_id from a
// valid span context and job_id from the context (when present). base is
// expected to be a slog.NewJSONHandler so output stays valid JSON in all OTEL
// states (D-03 stdout-always).
func NewCtxHandler(base slog.Handler) slog.Handler {
	return ctxHandler{Handler: base}
}

// Handle injects the correlation attributes (only when present) and delegates to
// the wrapped handler. The record is cloned via AddAttrs on the local copy so
// the caller's record is unaffected.
func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	if jid, ok := JobIDFromContext(ctx); ok {
		r.AddAttrs(slog.String("job_id", jid))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs preserves the wrapping when slog derives a child handler.
func (h ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup preserves the wrapping when slog derives a grouped handler.
func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithGroup(name)}
}

// fanoutHandler dispatches each record to every wrapped handler. It is used to
// emit a log line to BOTH the stdout JSON handler (D-03 stdout-always) and the
// OTLP-logs bridge (D-03 OTLP-when-configured) in one slog.SetDefault.
type fanoutHandler struct {
	handlers []slog.Handler
}

// NewFanout returns a handler that forwards every record to each of handlers.
// A handler whose Enabled returns false for a given level is skipped. Errors
// from individual handlers are joined so one failing sink does not silence the
// others.
func NewFanout(handlers ...slog.Handler) slog.Handler {
	return fanoutHandler{handlers: handlers}
}

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return fanoutHandler{handlers: next}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return fanoutHandler{handlers: next}
}
