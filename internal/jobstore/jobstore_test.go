package jobstore_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/internal/redisx"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// dialOrSkip returns a live Redis client or skips the test. Implements the
// two-gate pattern: (1) parse URL, (2) Ping. Skips cleanly without infra.
func dialOrSkip(t *testing.T) *redis.Client {
	t.Helper()

	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6379"
	}

	client, err := redisx.NewFromURL(rawURL)
	if err != nil {
		t.Skipf("dialOrSkip: could not parse TEST_REDIS_URL %q: %v (skipping live Redis tests)", rawURL, err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("dialOrSkip: Redis unreachable at %q: %v (skipping live Redis tests)", rawURL, err)
		return nil
	}

	return client
}

// uniqueJobID returns a job ID that is unique enough for test isolation.
func uniqueJobID(base string) string {
	return base + "-" + time.Now().Format("20060102150405.000000")
}

// TestJobStore_SpecRoundTrip verifies WriteSpec → ReadSpec returns an equal value.
func TestJobStore_SpecRoundTrip(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	store := jobstore.New(client)
	ctx := context.Background()

	jobID := uniqueJobID("spec-rt")
	want := wire.JobSpec{
		JobId:        jobID,
		Channel:      "private-run-" + jobID,
		Language:     "python",
		Version:      "3.12",
		Image:        "ghcr.io/code-runner/python-3.12:latest",
		Entrypoint:   "main.py",
		Run:          []string{"python", "main.py"},
		Interactive:  true,
		EnqueuedAtMs: 1700000000000,
		Limits: wire.Limits{
			WallTimeMs: 30000,
			CpuMs:      10000,
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   512,
			IdleMs:     5000,
		},
		Files: []wire.FileInput{
			{Name: "main.py", Content: wire.Ptr("print('hello')")},
		},
	}

	if err := store.WriteSpec(ctx, want); err != nil {
		t.Fatalf("WriteSpec: %v", err)
	}

	got, err := store.ReadSpec(ctx, jobID)
	if err != nil {
		t.Fatalf("ReadSpec: %v", err)
	}

	if got.JobId != want.JobId {
		t.Errorf("JobId: got %q, want %q", got.JobId, want.JobId)
	}
	if got.Language != want.Language {
		t.Errorf("Language: got %q, want %q", got.Language, want.Language)
	}
	if got.Version != want.Version {
		t.Errorf("Version: got %q, want %q", got.Version, want.Version)
	}
	if got.Interactive != want.Interactive {
		t.Errorf("Interactive: got %v, want %v", got.Interactive, want.Interactive)
	}
	if len(got.Files) != len(want.Files) || (len(want.Files) > 0 && got.Files[0].Content != want.Files[0].Content) {
		t.Errorf("Files mismatch: got %v, want %v", got.Files, want.Files)
	}
}

// TestJobStore_StatusRoundTrip verifies WriteStatus → ReadStatus returns an
// equal value (modulo UpdatedAtMs which is stamped by WriteStatus).
func TestJobStore_StatusRoundTrip(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	store := jobstore.New(client)
	ctx := context.Background()

	jobID := uniqueJobID("status-rt")
	want := wire.JobStatus{
		JobId:    jobID,
		Channel:  "private-run-" + jobID,
		Language: "python",
		Version:  "3.12",
		State:    wire.JobStateRunning,
	}

	beforeWrite := time.Now().UnixMilli()
	if err := store.WriteStatus(ctx, want); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}

	got, err := store.ReadStatus(ctx, jobID)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}

	if got.JobId != want.JobId {
		t.Errorf("JobId: got %q, want %q", got.JobId, want.JobId)
	}
	if got.State != want.State {
		t.Errorf("State: got %q, want %q", got.State, want.State)
	}
	if int64(got.UpdatedAtMs) < beforeWrite {
		t.Errorf("UpdatedAtMs %d is before write time %d", got.UpdatedAtMs, beforeWrite)
	}
}

// TestJobStore_ReadStatus_NotFound verifies that ReadStatus on a missing key
// returns an error wrapping ErrNotFound (the typed sentinel for API-09).
func TestJobStore_ReadStatus_NotFound(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	store := jobstore.New(client)
	ctx := context.Background()

	_, err := store.ReadStatus(ctx, "nonexistent-job-"+time.Now().Format("20060102150405"))
	if err == nil {
		t.Fatal("expected ErrNotFound for missing jobID, got nil")
	}
	if !errors.Is(err, jobstore.ErrNotFound) {
		t.Errorf("expected errors.Is(err, ErrNotFound); got %v", err)
	}
	if !jobstore.IsNotFound(err) {
		t.Errorf("expected jobstore.IsNotFound(err) == true; got false")
	}
}

// TestJobStore_ReadSpec_NotFound verifies that ReadSpec on a missing key
// returns an error wrapping ErrNotFound.
func TestJobStore_ReadSpec_NotFound(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	store := jobstore.New(client)
	ctx := context.Background()

	_, err := store.ReadSpec(ctx, "nonexistent-spec-"+time.Now().Format("20060102150405"))
	if err == nil {
		t.Fatal("expected ErrNotFound for missing jobID, got nil")
	}
	if !errors.Is(err, jobstore.ErrNotFound) {
		t.Errorf("expected errors.Is(err, ErrNotFound); got %v", err)
	}
}

// TestJobStore_ClaimEnqueue verifies LPUSH/BRPOP round-trip: Enqueue then
// Claim returns the same jobID.
func TestJobStore_ClaimEnqueue(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	store := jobstore.New(client)
	ctx := context.Background()

	jobID := uniqueJobID("queue-rt")

	if err := store.Enqueue(ctx, jobID); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, err := store.Claim(ctx, 3*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got != jobID {
		t.Errorf("Claim returned %q; want %q", got, jobID)
	}
}

// TestJobStore_ClaimTimeout verifies that Claim returns ErrTimeout when no
// job is enqueued within the timeout window.
func TestJobStore_ClaimTimeout(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	store := jobstore.New(client)
	ctx := context.Background()

	_, err := store.Claim(ctx, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected ErrTimeout for empty queue, got nil")
	}
	if !errors.Is(err, jobstore.ErrTimeout) {
		t.Errorf("expected errors.Is(err, ErrTimeout); got %v", err)
	}
}

// TestJobStore_WasStartRequested verifies the durable start-handshake flag:
// false when absent (job not started yet), true once the API has SET it. This
// is what lets the worker recover a /start that was sent while the job was still
// queued (no live ctrl:<id> subscriber).
func TestJobStore_WasStartRequested(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	store := jobstore.New(client)
	ctx := context.Background()

	jobID := uniqueJobID("startflag")

	// Absent flag → not started.
	started, err := store.WasStartRequested(ctx, jobID)
	if err != nil {
		t.Fatalf("WasStartRequested (absent): %v", err)
	}
	if started {
		t.Error("WasStartRequested must be false before /start sets the flag")
	}

	// Simulate POST /start writing the durable flag (the API SETs it with a TTL).
	if err := client.Set(ctx, keys.StartFlagKey(jobID), "1", time.Minute).Err(); err != nil {
		t.Fatalf("SET start flag: %v", err)
	}

	started, err = store.WasStartRequested(ctx, jobID)
	if err != nil {
		t.Fatalf("WasStartRequested (present): %v", err)
	}
	if !started {
		t.Error("WasStartRequested must be true after the start flag is SET")
	}
}
