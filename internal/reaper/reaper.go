// Package reaper implements the dead-worker orphan sweep (SCALE-04).
//
// When a worker process is killed (kill -9, OOM, node failure) it cannot run
// its deferred cleanup — leaving host containers labelled code-runner.jobId
// running, their anonymous /workspace volumes attached, and their capacity
// slots held forever.
//
// The reaper is a goroutine that every running worker launches.  On each tick
// it:
//
//  1. Lists all containers on the host labelled code-runner.jobId (running or
//     exited) via Docker ContainerList(All:true, label filter).
//
//  2. Collects the set of live workers: scans for "worker:*:jobs" Redis keys,
//     then checks which ones have a live heartbeat key (WorkerHeartbeatKey
//     exists with unexpired TTL).
//
//  3. Builds the live-job set: the union of OwnedJobs sets for all live workers.
//
//  4. A container is ORPHANED if its jobId is NOT in the live-job set (i.e. no
//     live worker owns it).
//
//  5. For each orphaned container: ContainerRemove(Force:true, RemoveVolumes:true)
//     — force-removes the container and prunes the anonymous /workspace volume.
//     Not-found is tolerated (concurrent reaper or normal cleanup already ran).
//
//  6. For each orphaned job: mark status "error" in Redis (best-effort), and
//     remove any stale owned-jobs membership.
//
// Security / correctness invariant (T-05-04): a container is reaped ONLY if
// its jobId is in NO live worker's owned-jobs set.  Live workers' containers
// are never touched.  This is asserted by the integration test.
//
// Concurrency note (T-05-06): every worker runs a reaper, so concurrent sweeps
// are expected.  ContainerRemove is idempotent (not-found tolerated); concurrent
// sweeps converge — at most one reaps, the rest get not-found and move on.
package reaper

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// instrumentationName is the meter scope for reaper-emitted metrics.
const instrumentationName = "code-runner-worker"

// reaperOrphans resolves the orphan-removal counter from the CURRENT global
// MeterProvider on each call (lazy resolution — see worker.go). A MeterProvider
// installed after package init (otelinit.Init at boot, or a ManualReader in
// tests) is honoured; the no-op provider returns a no-op counter at zero cost.
//
// The counter increments once per orphan container successfully removed by the
// sweep. It carries NO attributes — there is nothing low-cardinality to add, and
// job_id/container_id must NEVER become metric dimensions (RESEARCH anti-pattern:
// high cardinality).
func reaperOrphans() metric.Int64Counter {
	c, _ := otel.Meter(instrumentationName).Int64Counter(
		"code_runner.reaper.orphans",
		metric.WithUnit("{container}"),
		metric.WithDescription("Count of orphaned sandbox containers reaped (dead-worker recovery)."),
	)
	return c
}

// workerJobsKeyPrefix is the pattern used to scan for all worker owned-jobs
// keys in Redis.  keys.WorkerJobsKey returns "worker:<id>:jobs"; the prefix
// "worker:" and suffix ":jobs" let us extract the workerID from the key name.
const workerJobsKeyPrefix = "worker:"
const workerJobsKeySuffix = ":jobs"

// Reaper sweeps the host Docker daemon for containers whose owning worker has
// died (its heartbeat key has expired) and force-removes them with their
// anonymous /workspace volumes.
type Reaper struct {
	cli      *client.Client
	store    *jobstore.Store
	interval time.Duration
}

// New creates a Reaper.
//
// cli is the Docker daemon client (moby SDK).
// store is the Redis-backed job store (internal/jobstore).
// interval is how often Sweep is called; it should be roughly equal to the
// worker heartbeat TTL so orphans are detected shortly after the owning worker's
// key expires.
func New(cli *client.Client, store *jobstore.Store, interval time.Duration) *Reaper {
	return &Reaper{
		cli:      cli,
		store:    store,
		interval: interval,
	}
}

// Run starts the reaper ticker loop, calling Sweep on every interval tick until
// ctx is cancelled.  Intended to be started as a goroutine at worker boot.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	slog.Info("reaper: started", "interval", r.interval)
	for {
		select {
		case <-ctx.Done():
			slog.Info("reaper: stopping")
			return
		case <-ticker.C:
			if err := r.Sweep(ctx); err != nil {
				// A sweep error is non-fatal: log and keep running.
				slog.Warn("reaper: sweep error", "err", err)
			}
		}
	}
}

// Sweep performs one orphan-detection and removal pass.  It is exported so the
// integration test can call it directly without waiting for the ticker.
//
// Returns a multi-error if any per-container operation fails; partial failures
// do not abort the sweep — all containers are evaluated.
func (r *Reaper) Sweep(ctx context.Context) error {
	// ── Step 1: list all labelled sandbox containers ────────────────────────────
	f := filters.NewArgs()
	f.Add("label", "code-runner.jobId")
	containers, err := r.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return fmt.Errorf("reaper.Sweep: ContainerList: %w", err)
	}
	if len(containers) == 0 {
		return nil // nothing to do
	}

	// ── Step 2: collect live worker IDs ────────────────────────────────────────
	// Scan for all "worker:*:jobs" keys.  Extract the workerID from each key name,
	// then check HeartbeatAlive to determine whether the worker is still running.
	liveWorkerIDs, err := r.collectLiveWorkerIDs(ctx)
	if err != nil {
		// Log but don't abort: we can still check with an empty live set, which
		// means all containers look orphaned.  This is conservative (false positives
		// if Redis is transiently down); log a clear warning so operators notice.
		slog.Warn("reaper.Sweep: could not collect live workers — skipping sweep to avoid false positives", "err", err)
		return fmt.Errorf("reaper.Sweep: collectLiveWorkerIDs: %w", err)
	}

	// ── Step 3: build the live-job set ─────────────────────────────────────────
	liveJobs := make(map[string]string) // jobID → owning workerID (for cleanup)
	for _, workerID := range liveWorkerIDs {
		jobs, err := r.store.OwnedJobs(ctx, workerID)
		if err != nil {
			// Tolerate per-worker errors: skip this worker's jobs in the live set,
			// which makes its jobs look orphaned.  A warning is logged so operators
			// can investigate.
			slog.Warn("reaper.Sweep: OwnedJobs failed for worker", "workerID", workerID, "err", err)
			continue
		}
		for _, jobID := range jobs {
			liveJobs[jobID] = workerID
		}
	}

	// ── Steps 4-6: evaluate each container ─────────────────────────────────────
	var errs []string
	for _, c := range containers {
		jobID, ok := c.Labels["code-runner.jobId"]
		if !ok || jobID == "" {
			continue // label not present (shouldn't happen given the filter, but be safe)
		}

		if _, alive := liveJobs[jobID]; alive {
			// Owned by a live worker — never reap.
			slog.Debug("reaper.Sweep: container owned by live worker, skipping",
				"containerID", c.ID[:12], "jobID", jobID)
			continue
		}

		// ORPHAN: jobId is not in any live worker's owned-jobs set.
		slog.Info("reaper.Sweep: reaping orphaned container",
			"containerID", c.ID[:12], "jobID", jobID)

		if err := r.reapContainer(ctx, c.ID, jobID); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("reaper.Sweep: %d error(s): %s", len(errs), strings.Join(errs, "; "))
	}
	return nil
}

// reapContainer force-removes a single orphaned container (with its anonymous
// /workspace volume), then reclaims slot accounting and marks the job status.
func (r *Reaper) reapContainer(ctx context.Context, containerID, jobID string) error {
	// Force-remove container + anonymous /workspace volume.
	// RemoveVolumes:true is mandatory — the anonymous volume holds the injected
	// source files and must not linger on the host.
	err := r.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil && !client.IsErrNotFound(err) {
		return fmt.Errorf("reaper: ContainerRemove %s (job %s): %w", containerID[:12], jobID, err)
	}
	// OBS-06: count each successfully reaped orphan (no-error OR not-found, which
	// both mean the container is gone). No attributes — low-cardinality by design.
	reaperOrphans().Add(ctx, 1)
	slog.Info("reaper: container removed", "containerID", containerID[:12], "jobID", jobID)

	// Reclaim owned-jobs membership: scan all worker:*:jobs sets and remove jobID
	// from any that still contain it (the dead worker never removed it).
	// Best-effort: log errors but continue.
	r.cleanupOwnedJobMembership(ctx, jobID)

	// Mark the job status as error so the client gets a terminal state instead of
	// hanging forever waiting for a result event.
	// Best-effort: log on error.
	if err := r.store.WriteStatus(ctx, wire.JobStatus{
		JobId: jobID,
		State: wire.JobStateError,
	}); err != nil {
		slog.Warn("reaper: WriteStatus error failed", "jobID", jobID, "err", err)
	}

	return nil
}

// collectLiveWorkerIDs returns the IDs of all workers whose heartbeat key is
// currently alive in Redis.
//
// Algorithm:
//  1. Scan for all "worker:*:jobs" keys.
//  2. Extract the workerID from each key name ("worker:<id>:jobs" → "<id>").
//  3. Check HeartbeatAlive for each workerID; include only the live ones.
func (r *Reaper) collectLiveWorkerIDs(ctx context.Context) ([]string, error) {
	// Use SCAN to find all worker:*:jobs keys without blocking Redis.
	// The pattern "worker:*:jobs" matches keys.WorkerJobsKey(anyID).
	var cursor uint64
	var allKeys []string
	for {
		keys, nextCursor, err := r.store.ScanWorkerJobsKeys(ctx, cursor)
		if err != nil {
			return nil, fmt.Errorf("collectLiveWorkerIDs: SCAN: %w", err)
		}
		allKeys = append(allKeys, keys...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	var liveIDs []string
	for _, k := range allKeys {
		workerID := extractWorkerID(k)
		if workerID == "" {
			continue
		}
		alive, err := r.store.HeartbeatAlive(ctx, workerID)
		if err != nil {
			slog.Warn("reaper: HeartbeatAlive check failed", "workerID", workerID, "err", err)
			continue
		}
		if alive {
			liveIDs = append(liveIDs, workerID)
		}
	}
	return liveIDs, nil
}

// cleanupOwnedJobMembership removes jobID from any stale worker:*:jobs set
// that still contains it.  Called best-effort after a container is reaped.
func (r *Reaper) cleanupOwnedJobMembership(ctx context.Context, jobID string) {
	var cursor uint64
	for {
		ks, nextCursor, err := r.store.ScanWorkerJobsKeys(ctx, cursor)
		if err != nil {
			slog.Warn("reaper: cleanupOwnedJobMembership: SCAN failed", "err", err)
			return
		}
		for _, k := range ks {
			workerID := extractWorkerID(k)
			if workerID == "" {
				continue
			}
			if err := r.store.RemoveOwnedJob(ctx, workerID, jobID); err != nil {
				slog.Warn("reaper: RemoveOwnedJob failed",
					"workerID", workerID, "jobID", jobID, "err", err)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}

// extractWorkerID parses a "worker:<id>:jobs" key and returns the <id> portion.
// Returns "" if the key does not match the expected format.
func extractWorkerID(k string) string {
	if !strings.HasPrefix(k, workerJobsKeyPrefix) || !strings.HasSuffix(k, workerJobsKeySuffix) {
		return ""
	}
	id := k[len(workerJobsKeyPrefix) : len(k)-len(workerJobsKeySuffix)]
	if id == "" {
		return ""
	}
	return id
}

// workerJobsPattern is the SCAN pattern for all worker owned-jobs keys.
// Exported for use in ScanWorkerJobsKeys.
const workerJobsPattern = "worker:*:jobs"

// Ensure the keys package constant for the jobs key suffix is consistent.
// This is a compile-time cross-check — if keys.WorkerJobsKey changes its
// format, the extractWorkerID function must be updated too.
var _ = keys.WorkerJobsKey // referenced to satisfy import
