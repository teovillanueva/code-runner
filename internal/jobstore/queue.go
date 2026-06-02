package jobstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/teovillanueva/code-runner/internal/keys"
)

// ErrTimeout is the sentinel returned by Claim when the BRPOP times out with
// no job available. The worker run loop treats this as a signal to re-poll
// (not a fatal error).
var ErrTimeout = errors.New("jobstore: BRPOP timeout — no job available")

// Claim blocks on BRPOP keys.JobQueue for up to timeout and returns the
// claimed jobID. If no job is available within timeout, ErrTimeout is
// returned. Other Redis errors are returned wrapped.
//
// The worker run loop calls Claim in a loop:
//
//	for {
//	    jobID, err := queue.Claim(ctx, 5*time.Second)
//	    if errors.Is(err, jobstore.ErrTimeout) { continue }
//	    ...
//	}
func (s *Store) Claim(ctx context.Context, timeout time.Duration) (string, error) {
	result, err := s.client.BRPop(ctx, timeout, keys.JobQueue).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("jobstore.Claim: BRPOP %s: %w", keys.JobQueue, err)
	}
	// BRPop returns [key, value]; result[1] is the jobID.
	if len(result) < 2 {
		return "", fmt.Errorf("jobstore.Claim: unexpected BRPOP result length %d", len(result))
	}
	return result[1], nil
}

// Enqueue LPUSHes jobID onto keys.JobQueue. This mirrors the LPUSH the Hono
// API performs; providing it on the Go side enables worker integration tests
// and keeps the queue API symmetric.
func (s *Store) Enqueue(ctx context.Context, jobID string) error {
	if err := s.client.LPush(ctx, keys.JobQueue, jobID).Err(); err != nil {
		return fmt.Errorf("jobstore.Enqueue: LPUSH %s %s: %w", keys.JobQueue, jobID, err)
	}
	return nil
}
