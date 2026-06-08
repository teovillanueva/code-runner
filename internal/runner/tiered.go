// Package runner — TieredRunner (TIER-01..04).
//
// The TieredRunner is the runner the worker actually holds when the zygote tier
// is enabled. It owns two backends — a DockerSocketRunner (always present) and
// an optional ZygoteRunner — and routes each job to one of them via a single,
// manifest-driven predicate:
//
//   - zygote-eligible language (the manifest declares a non-empty preimport set,
//     i.e. manifest.ZygoteEligible == true) AND a zygote runner is available
//     → ZygoteRunner (warm-pool CoW tier).
//   - everything else (compiled / no-import languages, or the zygote runner is
//     nil because ZYGOTE_ENABLED is off) → DockerSocketRunner (per-job hardened
//     container).
//
// There is NO language-name branching anywhere: the routing signal comes purely
// from the manifest via the eligible predicate (TIER-02). When the zygote runner
// is nil (disabled — the safe default) every job goes to Docker, so a worker with
// ZYGOTE_ENABLED unset behaves EXACTLY as a plain DockerSocketRunner worker
// (TIER-04).
package runner

import (
	"context"
	"log/slog"

	"github.com/teovillanueva/code-runner/internal/manifest"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// Compile-time assertion: TieredRunner must implement Runner.
var _ Runner = (*TieredRunner)(nil)

// TieredRunner routes Create calls to either the zygote runner (when the job's
// language is zygote-eligible and a zygote runner is configured) or the docker
// runner (everything else). The routing predicate is injected so the package
// stays decoupled from the manifest registry and is trivially unit-testable.
type TieredRunner struct {
	docker   Runner
	zygote   Runner // may be nil → all jobs route to docker (TIER-04)
	eligible func(spec wire.JobSpec) bool
}

// NewTieredRunner builds a TieredRunner.
//
//   - docker is the always-present per-job runner (must be non-nil).
//   - zygote is the optional warm-pool runner; pass nil when ZYGOTE_ENABLED is
//     off, in which case every job routes to docker.
//   - eligible is the routing predicate: it returns true when a job should run on
//     the zygote tier. Use ZygoteEligibleFromRegistry to build the canonical,
//     manifest-driven predicate.
func NewTieredRunner(docker Runner, zygote Runner, eligible func(spec wire.JobSpec) bool) *TieredRunner {
	return &TieredRunner{docker: docker, zygote: zygote, eligible: eligible}
}

// Create routes the job. A job goes to the zygote runner only when (a) a zygote
// runner is configured AND (b) the eligible predicate returns true for the spec.
// In every other case it goes to the docker runner — the safe, hardened default.
//
// Resilience (prod safety): if a zygote-eligible job's zygote Create FAILS (the
// warm pool won't start, the agent dial fails, the image lacks the agent, etc.),
// the job transparently falls back to the always-present hardened Docker tier
// rather than failing. The fallback is logged loudly and counted
// (code_runner.zygote.fallback.count) so a degraded zygote tier silently serving
// via Docker is observable, never invisible. Only Create-TIME failures can fall
// back; once a Sandbox is returned the session owns its lifecycle. A cancelled
// context is not a zygote fault, so it is returned as-is (no fallback).
func (t *TieredRunner) Create(ctx context.Context, spec wire.JobSpec) (Sandbox, error) {
	if t.zygote != nil && t.eligible != nil && t.eligible(spec) {
		sb, err := t.zygote.Create(ctx, spec)
		if err == nil {
			return sb, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		slog.Warn("zygote Create failed; falling back to Docker tier",
			"language", spec.Language, "version", spec.Version, "job_id", spec.JobId, "err", err)
		zygoteFallbackCount().Add(ctx, 1, langVersionAttr(spec.Language, spec.Version))
		return t.docker.Create(ctx, spec)
	}
	return t.docker.Create(ctx, spec)
}

// Close releases the zygote runner's pool resources if it exposes a Close method
// (the ZygoteRunner does). The docker runner has no pooled state to release here.
// Safe to call even when the zygote runner is nil. Intended for worker shutdown.
func (t *TieredRunner) Close() error {
	if c, ok := t.zygote.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

// ZygoteEligibleFromRegistry returns the canonical routing predicate used by the
// worker: it resolves the job's (Language, Version) through the manifest registry
// and reports manifest.ZygoteEligible for the resolved manifest. This is the ONLY
// place tiering is decided, and it is entirely manifest-driven — no language name
// is ever hardcoded. If the manifest cannot be resolved (which should not happen
// for an API-resolved JobSpec), the job is treated as NOT eligible and falls back
// to the hardened Docker tier.
func ZygoteEligibleFromRegistry(reg *manifest.Registry) func(spec wire.JobSpec) bool {
	return func(spec wire.JobSpec) bool {
		m, err := reg.Resolve(spec.Language, spec.Version)
		if err != nil {
			return false
		}
		return manifest.ZygoteEligible(m)
	}
}
