package otelinit

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// TestInit_NoOpWhenUnset asserts the OBS-01 no-op gate (RESEARCH Pitfall 2):
// with neither OTEL_EXPORTER_OTLP_ENDPOINT nor OTEL_TRACES_EXPORTER set, Init
// installs nothing — it returns a non-nil shutdown that returns nil, and does
// NOT install a non-default propagator (the global stays the no-op default).
func TestInit_NoOpWhenUnset(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "")

	// Reset the global propagator to the SDK default no-op so we can assert
	// Init did not touch it.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

	if !IsNone() {
		t.Fatalf("expected IsNone() to be true with OTEL env unset")
	}

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init returned error in no-op mode: %v", err)
	}
	if shutdown == nil {
		t.Fatalf("Init returned a nil shutdown func; must be a non-nil no-op")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("no-op shutdown returned error: %v", err)
	}

	// The no-op composite propagator carries no fields; Init must not have
	// installed the W3C propagator.
	if fields := otel.GetTextMapPropagator().Fields(); len(fields) != 0 {
		t.Errorf("expected no propagator fields in no-op mode, got %v", fields)
	}
}

// TestInit_SetsW3CPropagatorWhenConfigured asserts that when an exporter is
// configured (here OTEL_TRACES_EXPORTER=none — a valid, side-effect-free
// exporter selection that still exercises the provider-construction path), Init
// installs the W3C TraceContext propagator. The "traceparent" field is the
// distinguishing marker of the W3C propagator (RESEARCH Pitfall 3 — Go's
// default is a no-op that MUST be replaced for cross-language correlation).
func TestInit_SetsW3CPropagatorWhenConfigured(t *testing.T) {
	// OTEL_*_EXPORTER=none selects the autoexport no-op exporters/readers, so no
	// network connection is made, but Init still walks the full configured path
	// (it is NOT the IsNone early-return, because OTEL_TRACES_EXPORTER is set).
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")

	// Start from a known no-op propagator so the assertion proves Init set it.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

	if IsNone() {
		t.Fatalf("expected IsNone() to be false when OTEL_TRACES_EXPORTER is set")
	}

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init returned error in configured mode: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	// W3C TraceContext propagator advertises the "traceparent" field.
	fields := otel.GetTextMapPropagator().Fields()
	if !containsField(fields, "traceparent") {
		t.Errorf("expected W3C propagator (traceparent field) after Init, got fields=%v", fields)
	}
}

func containsField(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}
