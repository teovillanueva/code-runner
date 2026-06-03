// Package otelinit bootstraps the worker's OpenTelemetry SDK, env-gated so that
// it is a TRUE no-op when OTEL is unconfigured (OBS-01, RESEARCH Pattern 2 /
// Pitfall 2).
//
// Neither the Go SDK nor the autoexport factory is a no-op by default:
// autoexport defaults every OTEL_*_EXPORTER to "otlp", which would silently try
// to push to localhost:4318. So Init early-returns a no-op shutdown when no
// endpoint/exporter is configured — installing no exporters, opening no port,
// and leaving the global TracerProvider/MeterProvider at their default no-ops.
//
// When OTEL is configured, Init constructs TracerProvider + MeterProvider +
// LoggerProvider from the env-driven autoexport factory and ALWAYS sets the
// global propagator to W3C TraceContext (REQUIRED for cross-language trace
// correlation — Go's default propagator is a no-op, RESEARCH Pitfall 3).
//
// Security (RESEARCH V7/V8, threat T-08-05): this package only wires the OTLP
// PUSH pipeline. It opens no inbound HTTP listener and never logs or attributes
// any secret or user code/stdin. The endpoint is operator-configured env, not
// attacker-controllable.
package otelinit

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// loggerName is the instrumentation scope name for the worker's OTLP log bridge.
const loggerName = "code-runner-worker"

// OTLPLogHandler returns an slog.Handler that bridges slog records to the global
// OTel LoggerProvider for OTLP export (D-03: OTLP logs when configured). It is
// the OTLP-only path — NOT a stdout writer (RESEARCH Pitfall 4); stdout JSON
// correlation is the separate custom handler in internal/logging.
//
// When OTEL is unconfigured (IsNone), the global LoggerProvider is the SDK no-op,
// so the returned bridge silently discards — callers may install it
// unconditionally with zero export cost in the off state.
func OTLPLogHandler() slog.Handler {
	return otelslog.NewHandler(loggerName)
}

// noopShutdown is the shutdown function returned when OTEL is not configured.
// It is non-nil (so callers can always defer it) and returns nil.
func noopShutdown(context.Context) error { return nil }

// IsNone reports whether OTEL is configured to be off — i.e. no OTLP endpoint
// and no traces exporter are set in the environment. When true, Init installs
// nothing and the worker behaves exactly as today (no exporter, no new port,
// no startup regression). Exported so callers/tests can assert the gate.
func IsNone() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" &&
		os.Getenv("OTEL_TRACES_EXPORTER") == ""
}

// Init bootstraps the OTel SDK from the environment.
//
// When OTEL is unconfigured (IsNone), it returns a non-nil no-op shutdown and a
// nil error WITHOUT installing any global provider or propagator — the no-op
// gate (OBS-01).
//
// When configured, it builds the trace/metric/log providers via the env-driven
// autoexport factory, sets the globals, ALWAYS sets the W3C TraceContext
// propagator, and returns a combined errors.Join shutdown that flushes/stops
// every provider.
func Init(ctx context.Context) (shutdown func(context.Context) error, err error) {
	// No-op gate: no endpoint AND no traces exporter configured → install
	// nothing. autoexport would otherwise default to OTLP and push to
	// localhost:4318 (RESEARCH Pitfall 2 / OBS-01).
	if IsNone() {
		return noopShutdown, nil
	}

	// Resource from env (OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES).
	res, err := resource.New(ctx, resource.WithFromEnv())
	if err != nil {
		// resource.New returns a usable (possibly partial) resource alongside a
		// non-fatal error for unrecognised attrs; fall back to the default.
		res = resource.Default()
	}

	// W3C TraceContext propagator — REQUIRED for cross-language correlation.
	// Go's global propagator is a no-op unless set (RESEARCH Pitfall 3).
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Traces.
	spanExp, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return noopShutdown, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(spanExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// Metrics.
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return func(c context.Context) error { return tp.Shutdown(c) }, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// Logs (OTLP path — the stdout-always path is the custom slog handler in
	// internal/logging; otelslog/this provider feed OTLP only, RESEARCH Pitfall 4).
	logExp, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return func(c context.Context) error {
			return errors.Join(tp.Shutdown(c), mp.Shutdown(c))
		}, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	global.SetLoggerProvider(lp)

	return func(c context.Context) error {
		return errors.Join(tp.Shutdown(c), mp.Shutdown(c), lp.Shutdown(c))
	}, nil
}
