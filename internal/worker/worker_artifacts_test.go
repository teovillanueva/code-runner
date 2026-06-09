package worker_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/redisx"
	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/worker"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// ─────────────────────────────────────────────────────────────────────────────
// Live-Redis gate (mirrors internal/jobstore/jobstore_test.go dialOrSkip).
//
// These tests assert RunResult persistence via the real *jobstore.Store, so they
// need a Redis the store can SET/GET against. They SKIP cleanly when no Redis is
// reachable — no Docker, no network dependency in the worker logic itself; the
// store is the only Redis touch-point and the worker run path stays in-process.
// ─────────────────────────────────────────────────────────────────────────────

func dialOrSkipArtifacts(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6379"
	}
	client, err := redisx.NewFromURL(rawURL)
	if err != nil {
		t.Skipf("dialOrSkipArtifacts: bad TEST_REDIS_URL %q: %v (skipping)", rawURL, err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("dialOrSkipArtifacts: Redis unreachable at %q: %v (skipping)", rawURL, err)
		return nil
	}
	return client
}

func uniqueArtifactJobID(base string) string {
	return base + "-" + time.Now().Format("20060102150405.000000")
}

// ─────────────────────────────────────────────────────────────────────────────
// artifactSandbox — fake DockerSandbox recording call order, capturing the
// exclude map, and returning a configurable []runner.CapturedArtifact.
// ─────────────────────────────────────────────────────────────────────────────

type artifactSandbox struct {
	*scriptedSandbox

	mu sync.Mutex

	captured []runner.CapturedArtifact

	readSeq     int // sequence number at which ReadArtifacts was called (0 = not called)
	cleanupSeq  int // sequence number at which Cleanup was called
	readCount   int
	lastExclude map[string]bool

	seqCounter *atomic.Int32
}

func newArtifactSandbox(captured []runner.CapturedArtifact, seqCounter *atomic.Int32) *artifactSandbox {
	return &artifactSandbox{
		scriptedSandbox: newScriptedSandbox(),
		captured:        captured,
		seqCounter:      seqCounter,
	}
}

func (a *artifactSandbox) ReadArtifacts(_ context.Context, exclude map[string]bool) ([]runner.CapturedArtifact, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.readCount++
	a.readSeq = int(a.seqCounter.Add(1))
	// Copy the exclude map so later mutation by the worker can't affect assertions.
	cp := make(map[string]bool, len(exclude))
	for k, v := range exclude {
		cp[k] = v
	}
	a.lastExclude = cp
	out := make([]runner.CapturedArtifact, len(a.captured))
	copy(out, a.captured)
	return out, nil
}

func (a *artifactSandbox) Cleanup() error {
	a.mu.Lock()
	a.cleanupSeq = int(a.seqCounter.Add(1))
	a.mu.Unlock()
	return a.scriptedSandbox.Cleanup()
}

// CPUReader returns runner.CPUUsageFunc (the type alias) so artifactSandbox
// satisfies worker.DockerSandbox — the embedded scriptedSandbox.CPUReader
// returns the distinct named session.CPUUsageFunc, which would fail the
// type assertion the worker uses to reach ReadArtifacts.
func (a *artifactSandbox) CPUReader() runner.CPUUsageFunc {
	return func(_ context.Context) (int, error) { return 0, nil }
}

func (a *artifactSandbox) snapshot() (readSeq, cleanupSeq, readCount int, exclude map[string]bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.readSeq, a.cleanupSeq, a.readCount, a.lastExclude
}

// Ensure artifactSandbox satisfies worker.DockerSandbox (compile-time check via
// scriptedSandbox embedding for CPUReader/Limits + the methods above).
var _ worker.DockerSandbox = (*artifactSandbox)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// fakeArtifactStore — in-memory artifactstore.ArtifactStore.
// ─────────────────────────────────────────────────────────────────────────────

type fakeArtifactStore struct {
	mu       sync.Mutex
	putCount int
	putNames []string
}

func (f *fakeArtifactStore) Put(_ context.Context, jobID, name, _ string, _ []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCount++
	f.putNames = append(f.putNames, name)
	return "https://artifacts.example/" + jobID + "/" + name, nil
}

func (f *fakeArtifactStore) EnsureLifecycle(_ context.Context) error { return nil }

func (f *fakeArtifactStore) puts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.putCount
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func collectSpec(jobID string) wire.JobSpec {
	s := testSpec(jobID)
	t := true
	s.CollectOutput = &t
	s.Limits.MaxArtifacts = 20
	s.Limits.MaxArtifactBytes = 4 * 1024 * 1024
	return s
}

func pngArtifacts(n int) []runner.CapturedArtifact {
	out := make([]runner.CapturedArtifact, n)
	for i := 0; i < n; i++ {
		out[i] = runner.CapturedArtifact{
			Name:     "plot" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".png",
			MimeType: "image/png",
			Data:     []byte{0x89, 0x50, 0x4e, 0x47}, // tiny PNG-ish payload
		}
	}
	return out
}

// runCollectedJob drives a collected job to completion through HandleJobForTest
// and returns the artifact sandbox + store for assertions.
func runCollectedJob(t *testing.T, store *jobstore.Store, captured []runner.CapturedArtifact, artStore *fakeArtifactStore, spec wire.JobSpec) *artifactSandbox {
	t.Helper()

	seq := &atomic.Int32{}
	sb := newArtifactSandbox(captured, seq)
	sb.waitDelay = 5 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	pub, _ := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	cfg := worker.Config{
		MaxSandboxes: 4,
		WarmupMs:     500,
		ClaimTimeout: 100 * time.Millisecond,
		Artifacts:    nil, // overwritten below if artStore != nil
		RunResultTTL: 600 * time.Second,
	}
	if artStore != nil {
		cfg.Artifacts = artStore
	}
	w := worker.NewWithTransport(store, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, spec.JobId, wire.ControlMessage{Type: wire.ControlTypeStart})

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("collected job did not finish within 4s")
	}
	return sb
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestArtifacts_ReadBeforeCleanup asserts ReadArtifacts runs BEFORE Cleanup (D-07).
func TestArtifacts_ReadBeforeCleanup(t *testing.T) {
	client := dialOrSkipArtifacts(t)
	defer client.Close() //nolint:errcheck
	store := jobstore.New(client)

	jobID := uniqueArtifactJobID("read-before-cleanup")
	artStore := &fakeArtifactStore{}
	sb := runCollectedJob(t, store, pngArtifacts(2), artStore, collectSpec(jobID))

	readSeq, cleanupSeq, readCount, _ := sb.snapshot()
	require.Equal(t, 1, readCount, "ReadArtifacts must be called exactly once")
	require.NotZero(t, readSeq, "ReadArtifacts must have been called")
	require.NotZero(t, cleanupSeq, "Cleanup must have been called")
	assert.Less(t, readSeq, cleanupSeq, "ReadArtifacts MUST run before Cleanup (D-07)")
}

// TestArtifacts_TwoPNGs asserts a collected job with 2 PNGs yields exactly 2
// artifacts and ArtifactsTruncated=false.
func TestArtifacts_TwoPNGs(t *testing.T) {
	client := dialOrSkipArtifacts(t)
	defer client.Close() //nolint:errcheck
	store := jobstore.New(client)
	ctx := context.Background()

	jobID := uniqueArtifactJobID("two-pngs")
	artStore := &fakeArtifactStore{}
	runCollectedJob(t, store, pngArtifacts(2), artStore, collectSpec(jobID))

	rr, err := store.ReadRunResult(ctx, jobID)
	require.NoError(t, err)
	assert.Len(t, rr.Artifacts, 2, "two PNG files -> exactly 2 artifacts")
	assert.False(t, rr.ArtifactsTruncated, "no truncation under the cap")
	assert.Equal(t, 2, artStore.puts(), "each kept artifact uploaded once")
	for _, a := range rr.Artifacts {
		assert.NotEmpty(t, a.Url, "each artifact carries a presigned URL")
	}
}

// TestArtifacts_TwentyFiveFilesCapped asserts a 25-file job yields 20 artifacts +
// ArtifactsTruncated=true while the terminal exitCode is unchanged (job not failed).
func TestArtifacts_TwentyFiveFilesCapped(t *testing.T) {
	client := dialOrSkipArtifacts(t)
	defer client.Close() //nolint:errcheck
	store := jobstore.New(client)
	ctx := context.Background()

	jobID := uniqueArtifactJobID("twenty-five")
	artStore := &fakeArtifactStore{}
	runCollectedJob(t, store, pngArtifacts(25), artStore, collectSpec(jobID))

	rr, err := store.ReadRunResult(ctx, jobID)
	require.NoError(t, err)
	assert.Len(t, rr.Artifacts, 20, "25 files capped at MaxArtifacts=20")
	assert.True(t, rr.ArtifactsTruncated, "excess past the cap sets ArtifactsTruncated")
	require.NotNil(t, rr.ExitCode, "job still reports a terminal exit code")
	assert.Equal(t, 0, *rr.ExitCode, "the real exitCode is preserved (job NOT failed)")
	assert.False(t, rr.TimedOut, "job not timed out")
}

// TestArtifacts_CompileOutputExcluded asserts that for a compiled-language spec
// the exclude set passed to ReadArtifacts contains the compile-output binary
// basename (R4 — the binary is never returned as an artifact).
func TestArtifacts_CompileOutputExcluded(t *testing.T) {
	client := dialOrSkipArtifacts(t)
	defer client.Close() //nolint:errcheck
	store := jobstore.New(client)

	jobID := uniqueArtifactJobID("compile-excluded")
	spec := collectSpec(jobID)
	// Compiled language: gcc -o app main.c; run argv invokes /workspace/app.
	compile := wire.JobSpecCompile([]string{"gcc", "-O2", "-o", "app", "main.c"})
	spec.Compile = &compile
	spec.Run = []string{"/workspace/app"}
	spec.Files = []wire.FileInput{
		{Name: "main.c", Content: wire.Ptr("int main(){return 0;}")},
		{Name: "data/in.csv", Content: wire.Ptr("a,b\n1,2\n")},
	}

	artStore := &fakeArtifactStore{}
	sb := runCollectedJob(t, store, pngArtifacts(1), artStore, spec)

	_, _, readCount, exclude := sb.snapshot()
	require.Equal(t, 1, readCount, "ReadArtifacts called once")
	require.NotNil(t, exclude, "exclude map captured")
	assert.True(t, exclude["app"], "compile-output binary basename must be excluded (R4)")
	assert.True(t, exclude[".compile_ready"], "compile marker must be excluded")
	assert.True(t, exclude["main.c"], "flat input file must be excluded by relative path")
	assert.True(t, exclude["data/in.csv"], "subdir input file must be excluded by FULL relative path (FILES-05)")
	assert.False(t, exclude["in.csv"], "subdir input must NOT be excluded by basename alone")
}

// TestArtifacts_NoCollectOutputNoRunResult asserts a job WITHOUT collectOutput
// writes no RunResult key and never calls ReadArtifacts / Put.
func TestArtifacts_NoCollectOutputNoRunResult(t *testing.T) {
	client := dialOrSkipArtifacts(t)
	defer client.Close() //nolint:errcheck
	store := jobstore.New(client)
	ctx := context.Background()

	jobID := uniqueArtifactJobID("no-collect")
	spec := testSpec(jobID) // CollectOutput nil -> false
	artStore := &fakeArtifactStore{}
	sb := runCollectedJob(t, store, pngArtifacts(3), artStore, spec)

	_, err := store.ReadRunResult(ctx, jobID)
	assert.True(t, jobstore.IsNotFound(err), "no RunResult key without collectOutput")

	_, _, readCount, _ := sb.snapshot()
	assert.Zero(t, readCount, "ReadArtifacts must not be called without collectOutput")
	assert.Zero(t, artStore.puts(), "no uploads without collectOutput")
}

// TestArtifacts_NilStoreGraceful asserts a collected job with a nil ArtifactStore
// still persists a RunResult (stdout/stderr present) with zero artifacts, no panic.
func TestArtifacts_NilStoreGraceful(t *testing.T) {
	client := dialOrSkipArtifacts(t)
	defer client.Close() //nolint:errcheck
	store := jobstore.New(client)
	ctx := context.Background()

	jobID := uniqueArtifactJobID("nil-store")
	spec := collectSpec(jobID)
	// nil ArtifactStore -> capture disabled, RunResult still persisted (D-04).
	sb := runCollectedJob(t, store, pngArtifacts(2), nil, spec)

	rr, err := store.ReadRunResult(ctx, jobID)
	require.NoError(t, err, "RunResult persists even with nil ArtifactStore")
	assert.Empty(t, rr.Artifacts, "zero artifacts when ArtifactStore is nil")
	assert.False(t, rr.ArtifactsTruncated)
	require.NotNil(t, rr.ExitCode)
	assert.Equal(t, 0, *rr.ExitCode)

	// With a nil ArtifactStore the worker short-circuits the type-assert+nil guard
	// before calling ReadArtifacts, so it is never invoked.
	_, _, readCount, _ := sb.snapshot()
	assert.Zero(t, readCount, "ReadArtifacts skipped when ArtifactStore is nil")
}
