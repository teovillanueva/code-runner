// Package jobstore provides Redis-backed storage for JobSpec and JobStatus,
// keyed via internal/keys. It is the persistence seam between the Hono API
// (writer) and the Go worker (reader/writer).
//
// Key layout (mirroring packages/contract/src/index.ts):
//
//	job:<id>:spec   — JSON-encoded wire.JobSpec (written by the API)
//	job:<id>:status — JSON-encoded wire.JobStatus (written by the worker, read by the API)
package jobstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// ErrNotFound is returned by ReadStatus (and ReadSpec) when the requested key
// does not exist in Redis. The API maps this to an HTTP 404 / API-09 unknown
// jobId response.
var ErrNotFound = errors.New("jobstore: key not found")

// Store is a Redis-backed job persistence layer. All methods are safe for
// concurrent use.
type Store struct {
	client *redis.Client
}

// New returns a new Store backed by the provided *redis.Client.
func New(client *redis.Client) *Store {
	return &Store{client: client}
}

// WriteSpec serialises spec to JSON and stores it at keys.JobSpecKey(spec.JobId).
// The key has no expiry; the worker is expected to clean up or the operator
// sets a global keyspace policy.
func (s *Store) WriteSpec(ctx context.Context, spec wire.JobSpec) error {
	b, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("jobstore.WriteSpec: marshal: %w", err)
	}
	if err := s.client.Set(ctx, keys.JobSpecKey(spec.JobId), b, 0).Err(); err != nil {
		return fmt.Errorf("jobstore.WriteSpec: SET %s: %w", keys.JobSpecKey(spec.JobId), err)
	}
	return nil
}

// ReadSpec retrieves and deserialises the JobSpec stored at
// keys.JobSpecKey(jobID). Returns ErrNotFound if the key is absent.
func (s *Store) ReadSpec(ctx context.Context, jobID string) (wire.JobSpec, error) {
	b, err := s.client.Get(ctx, keys.JobSpecKey(jobID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return wire.JobSpec{}, fmt.Errorf("jobstore.ReadSpec %s: %w", jobID, ErrNotFound)
		}
		return wire.JobSpec{}, fmt.Errorf("jobstore.ReadSpec: GET %s: %w", keys.JobSpecKey(jobID), err)
	}

	var spec wire.JobSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return wire.JobSpec{}, fmt.Errorf("jobstore.ReadSpec: unmarshal: %w", err)
	}
	return spec, nil
}

// WriteStatus serialises st to JSON, stamps UpdatedAtMs to the current Unix
// epoch milliseconds, and stores it at keys.JobStatusKey(st.JobId).
func (s *Store) WriteStatus(ctx context.Context, st wire.JobStatus) error {
	st.UpdatedAtMs = int(time.Now().UnixMilli())
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("jobstore.WriteStatus: marshal: %w", err)
	}
	if err := s.client.Set(ctx, keys.JobStatusKey(st.JobId), b, 0).Err(); err != nil {
		return fmt.Errorf("jobstore.WriteStatus: SET %s: %w", keys.JobStatusKey(st.JobId), err)
	}
	return nil
}

// ReadStatus retrieves and deserialises the JobStatus stored at
// keys.JobStatusKey(jobID). Returns ErrNotFound when the key is absent (the
// API maps this to API-09 unknown jobId).
func (s *Store) ReadStatus(ctx context.Context, jobID string) (wire.JobStatus, error) {
	b, err := s.client.Get(ctx, keys.JobStatusKey(jobID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return wire.JobStatus{}, fmt.Errorf("jobstore.ReadStatus %s: %w", jobID, ErrNotFound)
		}
		return wire.JobStatus{}, fmt.Errorf("jobstore.ReadStatus: GET %s: %w", keys.JobStatusKey(jobID), err)
	}

	var st wire.JobStatus
	if err := json.Unmarshal(b, &st); err != nil {
		return wire.JobStatus{}, fmt.Errorf("jobstore.ReadStatus: unmarshal: %w", err)
	}
	return st, nil
}

// WriteRunResult serialises rr to JSON and stores it at keys.JobOutputKey(jobID)
// with the provided TTL. This is the first keyed write that carries a real
// expiry (spec/status writes use 0 = no expiry): the collected RunResult is
// ephemeral pull state that auto-expires after w.cfg.RunResultTTL (R6/D-09,
// threat T-09-14). The worker calls this inside teardown only when
// spec.CollectOutput is set.
func (s *Store) WriteRunResult(ctx context.Context, jobID string, rr wire.RunResult, ttl time.Duration) error {
	b, err := json.Marshal(rr)
	if err != nil {
		return fmt.Errorf("jobstore.WriteRunResult: marshal: %w", err)
	}
	if err := s.client.Set(ctx, keys.JobOutputKey(jobID), b, ttl).Err(); err != nil {
		return fmt.Errorf("jobstore.WriteRunResult: SET %s: %w", keys.JobOutputKey(jobID), err)
	}
	return nil
}

// ReadRunResult retrieves and deserialises the RunResult stored at
// keys.JobOutputKey(jobID). Returns ErrNotFound when the key is absent, expired,
// or the job was not collected (no RunResult was ever written). The API maps
// this to an HTTP 404 on GET /v1/jobs/:id/output.
func (s *Store) ReadRunResult(ctx context.Context, jobID string) (wire.RunResult, error) {
	b, err := s.client.Get(ctx, keys.JobOutputKey(jobID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return wire.RunResult{}, fmt.Errorf("jobstore.ReadRunResult %s: %w", jobID, ErrNotFound)
		}
		return wire.RunResult{}, fmt.Errorf("jobstore.ReadRunResult: GET %s: %w", keys.JobOutputKey(jobID), err)
	}

	var rr wire.RunResult
	if err := json.Unmarshal(b, &rr); err != nil {
		return wire.RunResult{}, fmt.Errorf("jobstore.ReadRunResult: unmarshal: %w", err)
	}
	return rr, nil
}

// IsNotFound reports whether err (or any wrapped error in its chain) is
// ErrNotFound. Callers can use this instead of errors.Is to handle the sentinel.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
