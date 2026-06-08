// Zygote pool observability unit test (ZOBS-01..02).
//
// Docker-FREE: it asserts the INSTRUMENTS the zygote Create/Kill/pool paths
// record into are correct, deterministic, and runner-agnostic, via an in-memory
// ManualReader installed as the global MeterProvider (the same harness
// metrics_test.go uses — installManualReader/findHistogram are shared in-package
// helpers).
//
// It is an internal (package runner) test so it can drive the unexported
// instrument accessors that the production code records into, recording the same
// measurements the hot path records. No real agent / Docker is needed: the
// metrics wiring is verified independently of the privileged integration suite.
package runner

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/teovillanueva/code-runner/internal/config"
)

// findSumInt64 returns the named Int64 sum (counter) and asserts no job_id, and
// that a data point with the wanted attributes exists with value >= want.
func findSumInt64(t *testing.T, rm metricdata.ResourceMetrics, name string) (metricdata.Sum[int64], bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			s, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("expected Sum[int64] for %s, got %T", name, m.Data)
			}
			for _, dp := range s.DataPoints {
				if _, bad := dp.Attributes.Value("job_id"); bad {
					t.Errorf("%s must NOT carry a job_id attribute", name)
				}
			}
			return s, true
		}
	}
	return metricdata.Sum[int64]{}, false
}

// TestZygoteForkDurationHistogram asserts the fork/spawn latency instrument is a
// Float64 histogram carrying (language) and no job_id.
func TestZygoteForkDurationHistogram(t *testing.T) {
	collect := installManualReader(t)

	zygoteForkDuration().Record(context.Background(), 0.0031, langVersionAttr("python", "3.12"))

	rm := collect()
	h, found := findHistogram(t, rm, "code_runner.zygote.fork.duration", "python")
	if !found {
		t.Fatal("code_runner.zygote.fork.duration histogram not recorded")
	}
	var count uint64
	for _, dp := range h.DataPoints {
		count += dp.Count
	}
	if count < 1 {
		t.Errorf("expected >= 1 fork-duration sample, got %d", count)
	}
}

// TestZygoteParentReapCounter asserts the idle-reap counter increments with the
// (language, version) attribute set.
func TestZygoteParentReapCounter(t *testing.T) {
	collect := installManualReader(t)

	zygoteParentReapCount().Add(context.Background(), 1, langVersionAttr("python", "3.12"))
	zygoteParentReapCount().Add(context.Background(), 1, langVersionAttr("python", "3.12"))

	rm := collect()
	s, found := findSumInt64(t, rm, "code_runner.zygote.parent.reap.count")
	if !found {
		t.Fatal("code_runner.zygote.parent.reap.count counter not recorded")
	}
	var total int64
	for _, dp := range s.DataPoints {
		total += dp.Value
		if v, ok := dp.Attributes.Value("language"); !ok || v.AsString() != "python" {
			t.Errorf("reap counter: expected language=python, got %v", v.AsString())
		}
	}
	if total != 2 {
		t.Errorf("expected reap count total 2, got %d", total)
	}
}

// TestZygoteParentRespawnCounter asserts the respawn counter increments.
func TestZygoteParentRespawnCounter(t *testing.T) {
	collect := installManualReader(t)

	zygoteParentRespawnCount().Add(context.Background(), 1, langVersionAttr("r", "4.4"))

	rm := collect()
	s, found := findSumInt64(t, rm, "code_runner.zygote.parent.respawn.count")
	if !found {
		t.Fatal("code_runner.zygote.parent.respawn.count counter not recorded")
	}
	var total int64
	for _, dp := range s.DataPoints {
		total += dp.Value
	}
	if total != 1 {
		t.Errorf("expected respawn count total 1, got %d", total)
	}
}

// TestSandboxTerminalCounter asserts the runner-agnostic terminal-outcome
// counter carries (language, outcome) and no job_id — proving zygote and Docker
// tiers can share the same dashboard taxonomy (ZOBS-02).
func TestSandboxTerminalCounter(t *testing.T) {
	collect := installManualReader(t)

	sandboxTerminal().Add(context.Background(), 1, terminalAttr("python", "exited"))
	sandboxTerminal().Add(context.Background(), 1, terminalAttr("python", "killed"))

	rm := collect()
	s, found := findSumInt64(t, rm, "code_runner.sandbox.terminal.count")
	if !found {
		t.Fatal("code_runner.sandbox.terminal.count counter not recorded")
	}
	outcomes := map[string]int64{}
	for _, dp := range s.DataPoints {
		if v, ok := dp.Attributes.Value("outcome"); ok {
			outcomes[v.AsString()] += dp.Value
		}
		if _, bad := dp.Attributes.Value("job_id"); bad {
			t.Error("terminal counter must NOT carry job_id")
		}
	}
	if outcomes["exited"] != 1 || outcomes["killed"] != 1 {
		t.Errorf("expected exited=1 killed=1, got %v", outcomes)
	}
}

// TestWarmParentGauge asserts the per-language warm-parent observable gauge
// reports the poolManager's live parent map, keyed by (language, version), via a
// fake backend (no Docker). It mirrors the worker's observable-gauge test.
func TestWarmParentGauge(t *testing.T) {
	collect := installManualReader(t)

	cfg := config.Default()
	cfg.ZygotePoolIdleMs = 0 // disable reaper for a deterministic snapshot
	pm := newPoolManager(cfg, &fakeBackend{}, newFakeDialer().dial)
	t.Cleanup(func() { _ = pm.close() })

	// Inject two warm parents directly into the map (no Docker launch).
	pm.parents[poolKey{language: "python", version: "3.12"}] = &poolParent{
		key: poolKey{language: "python", version: "3.12"},
	}
	pm.parents[poolKey{language: "r", version: "4.4"}] = &poolParent{
		key: poolKey{language: "r", version: "4.4"},
	}

	unreg, err := registerWarmParentGauge(pm)
	if err != nil {
		t.Fatalf("registerWarmParentGauge: %v", err)
	}
	t.Cleanup(func() { _ = unreg() })

	rm := collect()
	var (
		seenPython bool
		seenR      bool
	)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "code_runner.zygote.pool.warm_parents" {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("expected Gauge[int64], got %T", m.Data)
			}
			for _, dp := range g.DataPoints {
				if _, bad := dp.Attributes.Value("job_id"); bad {
					t.Error("warm_parents gauge must NOT carry job_id")
				}
				lang, _ := dp.Attributes.Value("language")
				if lang.AsString() == "python" && dp.Value == 1 {
					seenPython = true
				}
				if lang.AsString() == "r" && dp.Value == 1 {
					seenR = true
				}
			}
		}
	}
	if !seenPython || !seenR {
		t.Errorf("expected warm_parents=1 for python and r; seenPython=%v seenR=%v", seenPython, seenR)
	}
}
