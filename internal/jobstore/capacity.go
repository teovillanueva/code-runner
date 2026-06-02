// Package jobstore — capacity.go provides heartbeat and owned-jobs set
// operations used by the worker for statelessness (SCALE-01, SCALE-02) and by
// the reaper (plan 05-02) for dead-worker recovery.
//
// These methods are intentionally kept in a separate file from jobstore.go and
// queue.go to minimise the merge surface between plans in the same wave.
package jobstore

import (
	"context"
	"fmt"
	"time"

	"github.com/teovillanueva/code-runner/internal/keys"
)

// Heartbeat writes (or refreshes) the worker's heartbeat key in Redis with the
// given TTL.  The value is the current Unix time in milliseconds (a simple
// human-readable timestamp for debugging; the reaper only cares whether the
// key exists).
//
// The key format is keys.WorkerHeartbeatKey(workerID) — "worker:<id>:heartbeat".
// Each call resets the TTL, so a worker that calls this on a regular interval
// keeps the key alive.  If the worker stops, the key expires after ttl and the
// reaper detects it.
func (s *Store) Heartbeat(ctx context.Context, workerID string, ttl time.Duration) error {
	k := keys.WorkerHeartbeatKey(workerID)
	val := fmt.Sprintf("%d", time.Now().UnixMilli())
	if err := s.client.Set(ctx, k, val, ttl).Err(); err != nil {
		return fmt.Errorf("jobstore.Heartbeat: SET %s: %w", k, err)
	}
	return nil
}

// AddOwnedJob records jobID in the worker's owned-jobs set.  The worker calls
// this after successfully creating a sandbox.  The reaper reads this set to
// find orphaned jobs when a worker dies.
//
// The key format is keys.WorkerJobsKey(workerID) — "worker:<id>:jobs".
// The set has no TTL — entries are removed explicitly via RemoveOwnedJob.
// Stale entries are cleaned up by the reaper in plan 05-02.
func (s *Store) AddOwnedJob(ctx context.Context, workerID, jobID string) error {
	k := keys.WorkerJobsKey(workerID)
	if err := s.client.SAdd(ctx, k, jobID).Err(); err != nil {
		return fmt.Errorf("jobstore.AddOwnedJob: SADD %s %s: %w", k, jobID, err)
	}
	return nil
}

// RemoveOwnedJob removes jobID from the worker's owned-jobs set.  The worker
// calls this inside its single sync.Once teardown so it happens exactly once on
// every terminal path (clean exit, kill, wall/idle/CPU timeout, ctx cancel,
// sandbox create failure).
func (s *Store) RemoveOwnedJob(ctx context.Context, workerID, jobID string) error {
	k := keys.WorkerJobsKey(workerID)
	if err := s.client.SRem(ctx, k, jobID).Err(); err != nil {
		return fmt.Errorf("jobstore.RemoveOwnedJob: SREM %s %s: %w", k, jobID, err)
	}
	return nil
}

// OwnedJobs returns all jobIDs currently recorded in the worker's owned-jobs
// set.  Used by the reaper (plan 05-02) to find jobs that need recovery after
// a worker dies.
func (s *Store) OwnedJobs(ctx context.Context, workerID string) ([]string, error) {
	k := keys.WorkerJobsKey(workerID)
	jobs, err := s.client.SMembers(ctx, k).Result()
	if err != nil {
		return nil, fmt.Errorf("jobstore.OwnedJobs: SMEMBERS %s: %w", k, err)
	}
	return jobs, nil
}

// IncrFreeSlots increments the global best-effort free-slot counter.  The
// worker calls this when it releases a slot (a sandbox finishes).  This is a
// secondary capacity signal — the authoritative gate is queue depth (LLEN
// jobs:queue, plan 05-03).  Errors are tolerated by the caller.
func (s *Store) IncrFreeSlots(ctx context.Context) error {
	if err := s.client.Incr(ctx, keys.CapacityFree).Err(); err != nil {
		return fmt.Errorf("jobstore.IncrFreeSlots: INCR %s: %w", keys.CapacityFree, err)
	}
	return nil
}

// DecrFreeSlots decrements the global best-effort free-slot counter.  The
// worker calls this when it acquires a slot (claims a job).  Errors are
// tolerated by the caller.
func (s *Store) DecrFreeSlots(ctx context.Context) error {
	if err := s.client.Decr(ctx, keys.CapacityFree).Err(); err != nil {
		return fmt.Errorf("jobstore.DecrFreeSlots: DECR %s: %w", keys.CapacityFree, err)
	}
	return nil
}
