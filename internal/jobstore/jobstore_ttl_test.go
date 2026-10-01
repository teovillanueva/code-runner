package jobstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// TestJobStore_WriteStatus_TTL verifies that a Store built WithStatusTTL
// expires job:<id>:status and refreshes that expiry on every write, while the
// default Store keeps the legacy no-expiry behaviour.
func TestJobStore_WriteStatus_TTL(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck
	ctx := context.Background()

	status := func(jobID string) wire.JobStatus {
		return wire.JobStatus{
			JobId:    jobID,
			Channel:  "private-run-" + jobID,
			Language: "python",
			Version:  "3.12",
			State:    wire.JobStateQueued,
		}
	}

	t.Run("sets and refreshes the TTL", func(t *testing.T) {
		store := jobstore.New(client).WithStatusTTL(time.Hour)
		jobID := uniqueJobID("status-ttl")
		key := keys.JobStatusKey(jobID)
		defer client.Del(ctx, key) //nolint:errcheck

		if err := store.WriteStatus(ctx, status(jobID)); err != nil {
			t.Fatalf("WriteStatus: %v", err)
		}
		ttl, err := client.TTL(ctx, key).Result()
		if err != nil {
			t.Fatalf("TTL: %v", err)
		}
		if ttl <= 0 || ttl > time.Hour {
			t.Fatalf("ttl=%v, want within (0, 1h]", ttl)
		}

		// Shorten it, write again: the write must restore the full TTL.
		if err := client.Expire(ctx, key, 10*time.Second).Err(); err != nil {
			t.Fatalf("Expire: %v", err)
		}
		if err := store.WriteStatus(ctx, status(jobID)); err != nil {
			t.Fatalf("WriteStatus (refresh): %v", err)
		}
		ttl, err = client.TTL(ctx, key).Result()
		if err != nil {
			t.Fatalf("TTL after refresh: %v", err)
		}
		if ttl <= 10*time.Second {
			t.Fatalf("ttl=%v after refresh, want > 10s", ttl)
		}
	})

	t.Run("default store keeps no expiry", func(t *testing.T) {
		store := jobstore.New(client)
		jobID := uniqueJobID("status-nottl")
		key := keys.JobStatusKey(jobID)
		defer client.Del(ctx, key) //nolint:errcheck

		if err := store.WriteStatus(ctx, status(jobID)); err != nil {
			t.Fatalf("WriteStatus: %v", err)
		}
		ttl, err := client.TTL(ctx, key).Result()
		if err != nil {
			t.Fatalf("TTL: %v", err)
		}
		if ttl != -1 { // go-redis maps Redis' "-1: exists, no expiry" to -1ns
			t.Fatalf("ttl=%v, want -1 (no expiry)", ttl)
		}
	})
}
