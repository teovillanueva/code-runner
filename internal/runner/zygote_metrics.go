// Package runner — zygote pool observability (ZOBS-01..02).
//
// These instruments are emitted through the SAME OTel meter scope
// (instrumentationName == "code-runner-worker") and the SAME lazy-resolution
// pattern as the Docker tier's sandbox.create/kill latency histograms in
// docker.go: each accessor resolves its instrument from the CURRENT global
// MeterProvider on every call, so a MeterProvider installed after package init
// (otelinit.Init at boot, or a ManualReader in tests) routes measurements
// correctly and the no-op provider costs nothing.
//
// Taxonomy (ZOBS-01):
//   - code_runner.zygote.pool.warm_parents (Int64ObservableGauge, "{parent}") —
//     how many warm parents are alive, per language+version. Registered as a
//     callback against the poolManager's live parent map.
//   - code_runner.zygote.fork.duration (Float64Histogram, "s") — Create→STARTED
//     wall time (the fork+harden+cgroup-place handshake), per language.
//   - code_runner.zygote.parent.reap.count (Int64Counter, "{parent}") — warm
//     parents torn down by the idle reaper (POOL-03), per language.
//   - code_runner.zygote.parent.respawn.count (Int64Counter, "{parent}") — warm
//     parents dropped+respawned after dead-parent detection (POOL-04), per
//     language.
//
// Runner-agnostic domain parity (ZOBS-02): the zygote terminal/kill paths feed
// the SAME instruments the Docker tier uses — sandboxKillDuration() (kill
// latency) and a shared sandboxTerminal() counter (terminal outcomes) — so
// dashboards stay uniform regardless of tier. We REUSE the existing names; we do
// NOT invent a parallel taxonomy.
//
// job_id is NEVER a metric attribute (high-cardinality anti-pattern). Only the
// low-cardinality language / version attributes are attached.
package runner

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// zygoteForkDuration resolves the fork/spawn (Create→STARTED) latency histogram.
func zygoteForkDuration() metric.Float64Histogram {
	h, _ := otel.Meter(instrumentationName).Float64Histogram(
		"code_runner.zygote.fork.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Wall time of the zygote fork+harden handshake (Create→STARTED), in seconds."),
	)
	return h
}

// zygoteParentReapCount resolves the idle-reap counter (POOL-03).
func zygoteParentReapCount() metric.Int64Counter {
	c, _ := otel.Meter(instrumentationName).Int64Counter(
		"code_runner.zygote.parent.reap.count",
		metric.WithUnit("{parent}"),
		metric.WithDescription("Warm zygote pool parents torn down by the idle reaper (POOL-03)."),
	)
	return c
}

// zygoteParentRespawnCount resolves the dead-parent respawn counter (POOL-04).
func zygoteParentRespawnCount() metric.Int64Counter {
	c, _ := otel.Meter(instrumentationName).Int64Counter(
		"code_runner.zygote.parent.respawn.count",
		metric.WithUnit("{parent}"),
		metric.WithDescription("Warm zygote pool parents dropped+respawned after dead-parent detection (POOL-04)."),
	)
	return c
}

// zygoteFallbackCount resolves the resilience counter: jobs that were routed to
// the zygote tier but fell back to the Docker tier because zygote Create failed
// (pool wouldn't start, dial failed, agent missing, etc.). A non-zero rate means
// the zygote tier is degraded and silently serving via Docker — surface it on a
// dashboard/alert so "zygote is on" never hides "zygote is actually all Docker".
func zygoteFallbackCount() metric.Int64Counter {
	c, _ := otel.Meter(instrumentationName).Int64Counter(
		"code_runner.zygote.fallback.count",
		metric.WithUnit("{job}"),
		metric.WithDescription("Zygote-eligible jobs that fell back to the Docker tier after a zygote Create error."),
	)
	return c
}

// sandboxTerminal resolves the runner-agnostic terminal-outcome counter shared
// by BOTH tiers. It carries `language` and a low-cardinality `outcome`
// (exited|killed|timed_out) so dashboards plot terminal states uniformly across
// the Docker and zygote tiers (ZOBS-02). It lives here (rather than docker.go)
// because the zygote work introduced it; the Docker path may adopt it without a
// taxonomy change.
func sandboxTerminal() metric.Int64Counter {
	c, _ := otel.Meter(instrumentationName).Int64Counter(
		"code_runner.sandbox.terminal.count",
		metric.WithUnit("{sandbox}"),
		metric.WithDescription("Terminal sandbox outcomes by language and outcome, runner-agnostic."),
	)
	return c
}

// langVersionAttr returns the low-cardinality (language, version) attribute set
// used on zygote-pool instruments. Both come from the manifest (a bounded set),
// never from user input — safe as metric dimensions.
func langVersionAttr(language, version string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("language", language),
		attribute.String("version", version),
	)
}

// terminalAttr returns the (language, outcome) attribute set for the
// runner-agnostic terminal counter.
func terminalAttr(language, outcome string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("language", language),
		attribute.String("outcome", outcome),
	)
}

// registerWarmParentGauge registers the per-language warm-parent observable
// gauge against the CURRENT global MeterProvider, observing the poolManager's
// live parent map on each export cycle. It returns an Unregister func (or a
// no-op on error) so callers/tests can detach the callback. Mirrors the worker's
// RegisterMetrics observable-gauge pattern (no job_id; bounded attributes).
func registerWarmParentGauge(pm *poolManager) (func() error, error) {
	meter := otel.Meter(instrumentationName)
	warm, err := meter.Int64ObservableGauge(
		"code_runner.zygote.pool.warm_parents",
		metric.WithUnit("{parent}"),
		metric.WithDescription("Live warm zygote pool parents, per language+version (POOL-01)."),
	)
	if err != nil {
		return func() error { return nil }, err
	}
	reg, err := meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			for key, count := range pm.warmParentCounts() {
				o.ObserveInt64(warm, count, langVersionAttr(key.language, key.version))
			}
			return nil
		},
		warm,
	)
	if err != nil {
		return func() error { return nil }, err
	}
	return reg.Unregister, nil
}
