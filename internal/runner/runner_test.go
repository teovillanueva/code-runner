package runner_test

import (
	"context"
	"testing"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// TestStubCreate verifies that NewStub().Create returns a non-nil Sandbox
// without error, confirming the seam compiles and basic contract holds.
func TestStubCreate(t *testing.T) {
	r := runner.NewStub()
	sb, err := r.Create(context.Background(), wire.JobSpec{
		JobId:   "test-job-1",
		Language: "python",
		Version:  "3.12",
		Image:    "code-runner/python:3.12",
		Run:      []string{"python3", "main.py"},
		Limits: wire.Limits{
			WallTimeMs: 5000,
			IdleMs:     3000,
			CpuMs:      2000,
			MemoryMb:   64,
			Pids:       32,
			OutputKb:   512,
		},
	})
	if err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	if sb == nil {
		t.Fatal("Create returned nil Sandbox")
	}
	t.Cleanup(func() {
		if cleanupErr := sb.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup returned unexpected error: %v", cleanupErr)
		}
	})
}

// TestStubPipeAccessors verifies that Stdin, Stdout and Stderr return non-nil
// values so callers can safely attach them without nil-checks.
func TestStubPipeAccessors(t *testing.T) {
	r := runner.NewStub()
	sb, err := r.Create(context.Background(), wire.JobSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() {
		if err := sb.Cleanup(); err != nil {
			t.Errorf("Cleanup: %v", err)
		}
	}()

	if sb.Stdin() == nil {
		t.Error("Stdin() returned nil")
	}
	if sb.Stdout() == nil {
		t.Error("Stdout() returned nil")
	}
	if sb.Stderr() == nil {
		t.Error("Stderr() returned nil")
	}
}

// TestStubWait verifies that Wait returns without blocking and without error.
func TestStubWait(t *testing.T) {
	r := runner.NewStub()
	sb, err := r.Create(context.Background(), wire.JobSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sb.Cleanup() //nolint:errcheck

	_, err = sb.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait returned unexpected error: %v", err)
	}
}

// TestStubKill verifies that Kill does not error or panic.
func TestStubKill(t *testing.T) {
	r := runner.NewStub()
	sb, err := r.Create(context.Background(), wire.JobSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sb.Cleanup() //nolint:errcheck

	if err := sb.Kill(context.Background()); err != nil {
		t.Fatalf("Kill returned unexpected error: %v", err)
	}
}

// TestStubCleanupIdempotent verifies that calling Cleanup multiple times does
// not return an error or panic (idempotency requirement).
func TestStubCleanupIdempotent(t *testing.T) {
	r := runner.NewStub()
	sb, err := r.Create(context.Background(), wire.JobSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := range 5 {
		if err := sb.Cleanup(); err != nil {
			t.Fatalf("Cleanup call %d returned unexpected error: %v", i+1, err)
		}
	}
}

// TestStubStdinWriteAndClose verifies basic Stdin write + close behaviour.
func TestStubStdinWriteAndClose(t *testing.T) {
	r := runner.NewStub()
	sb, err := r.Create(context.Background(), wire.JobSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sb.Cleanup() //nolint:errcheck

	stdin := sb.Stdin()
	if _, err := stdin.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Stdin Write: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("Stdin Close: %v", err)
	}
}
