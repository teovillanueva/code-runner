//go:build worker_integration

// Package worker integration tests drive a full interactive Python job over
// real Redis + Docker without a live soketi instance. Events are captured by
// a fake triggerer injected through publisher.NewForTest.
//
// Prerequisites:
//
//	docker pull redis:7
//	docker build -t executor/python:3.12 languages/python-3.12/   (already done)
//	docker run -d -p 6381:6379 redis:7  (or any redis:7 on TEST_REDIS_URL)
//
// Run with:
//
//	go test -tags=worker_integration -timeout 300s ./internal/worker/... -run Integration -v
package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/publisher"
	runnerPkg "github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/stdintransport"
	"github.com/teovillanueva/code-runner/internal/worker"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test infrastructure guards
// ─────────────────────────────────────────────────────────────────────────────

// dialTestRedis returns a live *redis.Client or skips the test.
func dialTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6381"
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Skipf("integration: cannot parse TEST_REDIS_URL %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("integration: Redis unreachable at %q: %v — run: docker run -d -p 6381:6379 redis:7", rawURL, err)
	}
	return cli
}

// requireDockerAndImage creates a Docker client, pings the daemon, and verifies
// the executor/python:3.12 image is present. Skips with a clear message if any
// gate fails.
func requireDockerAndImage(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("integration: cannot create docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("integration: Docker daemon unreachable: %v", err)
	}
	// Verify the Python image is present.
	listCtx, listCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer listCancel()
	images, err := cli.ImageList(listCtx, image.ListOptions{})
	if err != nil {
		t.Skipf("integration: cannot list Docker images: %v", err)
	}
	found := false
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == "executor/python:3.12" {
				found = true
			}
		}
	}
	if !found {
		t.Skip("integration: executor/python:3.12 image not found — build it first: cd languages/python-3.12 && docker build -t executor/python:3.12 .")
	}
	return cli
}

// assertNoContainerLeak checks that no container with label
// code-runner.jobId=<jobID> survives.
func assertNoContainerLeak(t *testing.T, cli *client.Client, jobID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := filters.NewArgs()
	f.Add("label", fmt.Sprintf("code-runner.jobId=%s", jobID))
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		t.Errorf("assertNoContainerLeak: ContainerList: %v", err)
		return
	}
	if len(containers) > 0 {
		ids := make([]string, len(containers))
		for i, c := range containers {
			ids[i] = c.ID[:12]
		}
		t.Errorf("LEAK: %d container(s) with jobId=%s still alive: %v", len(containers), jobID, ids)
	}
}

// seccompProfilePath returns the absolute path to the seccomp profile.
func integrationSeccompProfilePath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(filepath.Join(projectRoot, "profiles", "seccomp", "runner.json"))
	if err != nil {
		return ""
	}
	return abs
}

// ─────────────────────────────────────────────────────────────────────────────
// Integration event collector
// ─────────────────────────────────────────────────────────────────────────────

type integrationEvent struct {
	channel string
	event   string
	data    json.RawMessage
}

type integrationTriggerer struct {
	mu     sync.Mutex
	events []integrationEvent
	// notify is signalled whenever a new event arrives (up to channel capacity).
	notify chan struct{}
}

func newIntegrationTriggerer() *integrationTriggerer {
	return &integrationTriggerer{notify: make(chan struct{}, 128)}
}

func (it *integrationTriggerer) Trigger(channel, event string, data interface{}) error {
	raw, _ := json.Marshal(data)
	it.mu.Lock()
	it.events = append(it.events, integrationEvent{channel: channel, event: event, data: raw})
	it.mu.Unlock()
	select {
	case it.notify <- struct{}{}:
	default:
	}
	return nil
}

// waitFor blocks until predicate(events) returns true or timeout fires.
func (it *integrationTriggerer) waitFor(timeout time.Duration, pred func([]integrationEvent) bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		it.mu.Lock()
		snapshot := make([]integrationEvent, len(it.events))
		copy(snapshot, it.events)
		it.mu.Unlock()
		if pred(snapshot) {
			return true
		}
		select {
		case <-it.notify:
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

func (it *integrationTriggerer) allEvents() []integrationEvent {
	it.mu.Lock()
	defer it.mu.Unlock()
	out := make([]integrationEvent, len(it.events))
	copy(out, it.events)
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// TestIntegration_InteractivePythonJob
// Full interactive Python job: input() → print(hi, name)
// ─────────────────────────────────────────────────────────────────────────────

func TestIntegration_InteractivePythonJob(t *testing.T) {
	redisClient := dialTestRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := requireDockerAndImage(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Build the stack.
	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	cfg := config.Default()
	cfg.RedisURL = os.Getenv("TEST_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6381"
	}

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, integrationSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	it := newIntegrationTriggerer()
	pub := publisher.NewForTest(it)

	workerCfg := worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     10000,
		ClaimTimeout: 2 * time.Second,
	}

	w := worker.New(store, transport, dockerRunner, pub, workerCfg)

	// Enqueue a Python job: name=input(); print("hi", name)
	// Use "python -c" to avoid the read-only rootfs CopyToContainer limitation.
	jobID := fmt.Sprintf("integration-interactive-%d", time.Now().UnixNano())
	spec := wire.JobSpec{
		JobId:       jobID,
		Language:    "python",
		Version:     "3.12",
		Image:       "executor/python:3.12",
		Run:         []string{"python", "-c", "import sys\nname=input()\nprint('hi', name)\n"},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Files:       nil, // no file copy — avoids read-only rootfs issue
		Limits: wire.Limits{
			WallTimeMs: 30000,
			IdleMs:     10000,
			CpuMs:      15000,
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   512,
		},
	}

	require.NoError(t, store.WriteSpec(ctx, spec))
	require.NoError(t, store.Enqueue(ctx, jobID))

	// Run the worker loop in a goroutine.
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.Run(ctx)
	}()

	// Wait for the "queued" stage event (worker claimed the job).
	ok := it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stage" {
				var se wire.StageEvent
				if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseQueued {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for 'queued' stage event")

	// Brief pause to ensure the Redis pub/sub subscription is fully established
	// before publishing "start". The worker subscribes before publishing "queued",
	// but the Redis server-side confirmation may take a few milliseconds.
	time.Sleep(200 * time.Millisecond)

	// Send "start" control message.
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), startPayload).Err())

	// Wait for the "running" stage event.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stage" {
				var se wire.StageEvent
				if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseRunning {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for 'running' stage event")

	// Give the Python process a moment to reach input().
	time.Sleep(500 * time.Millisecond)

	// Send stdin: "world\n" — must use JSON StdinMessage format.
	publishStdinRaw(t, ctx, redisClient, jobID, "world\n")

	// Wait for a stdout event containing "hi world".
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil {
					if strings.Contains(oe.Chunk, "hi world") {
						return true
					}
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout event containing 'hi world'")

	// Wait for terminal result with exitCode 0.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "result" {
				var re wire.ResultEvent
				if json.Unmarshal(ev.data, &re) == nil && re.ExitCode != nil && *re.ExitCode == 0 {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for terminal result event with exitCode=0")

	// Cancel the worker loop.
	cancel()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	// Assert no container leak.
	assertNoContainerLeak(t, dockerCli, jobID)

	// Print captured events for the log.
	t.Logf("Captured %d events:", len(it.allEvents()))
	for _, ev := range it.allEvents() {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestIntegration_BatchPythonJob
// Batch (no-stdin) Python job: print("batch") — SESS-02 degenerate case.
// ─────────────────────────────────────────────────────────────────────────────

func TestIntegration_BatchPythonJob(t *testing.T) {
	redisClient := dialTestRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := requireDockerAndImage(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	cfg := config.Default()
	cfg.RedisURL = os.Getenv("TEST_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6381"
	}

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, integrationSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	it := newIntegrationTriggerer()
	pub := publisher.NewForTest(it)

	workerCfg := worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     10000,
		ClaimTimeout: 2 * time.Second,
	}

	w := worker.New(store, transport, dockerRunner, pub, workerCfg)

	// Use "python -c" to avoid the read-only rootfs CopyToContainer limitation.
	jobID := fmt.Sprintf("integration-batch-%d", time.Now().UnixNano())
	spec := wire.JobSpec{
		JobId:       jobID,
		Language:    "python",
		Version:     "3.12",
		Image:       "executor/python:3.12",
		Run:         []string{"python", "-c", "print('batch')"},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: false, // batch
		Files:       nil,   // no file copy — avoids read-only rootfs issue
		Limits: wire.Limits{
			WallTimeMs: 30000,
			IdleMs:     10000,
			CpuMs:      15000,
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   512,
		},
	}

	require.NoError(t, store.WriteSpec(ctx, spec))
	require.NoError(t, store.Enqueue(ctx, jobID))

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.Run(ctx)
	}()

	// Wait for "queued".
	ok := it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stage" {
				var se wire.StageEvent
				if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseQueued {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for 'queued' stage event")

	// Brief pause for Redis pub/sub subscription establishment.
	time.Sleep(200 * time.Millisecond)

	// Send "start" (no stdin).
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), startPayload).Err())

	// Wait for stdout containing "batch".
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil {
					if strings.Contains(oe.Chunk, "batch") {
						return true
					}
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout event containing 'batch'")

	// Wait for terminal result with exitCode 0.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "result" {
				var re wire.ResultEvent
				if json.Unmarshal(ev.data, &re) == nil && re.ExitCode != nil && *re.ExitCode == 0 {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for terminal result event with exitCode=0")

	cancel()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	assertNoContainerLeak(t, dockerCli, jobID)

	t.Logf("Batch job events:")
	for _, ev := range it.allEvents() {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestIntegration_FileBasedPythonJob
// Proves the file-injection path: submits main.py as a wire.FileInput and
// runs it via ["python", "main.py"] — NOT inline python -c.
// This is the production path that was blocked by ReadonlyRootfs before the
// /workspace tmpfs fix.
// ─────────────────────────────────────────────────────────────────────────────

func TestIntegration_FileBasedPythonJob(t *testing.T) {
	redisClient := dialTestRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := requireDockerAndImage(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	cfg := config.Default()
	cfg.RedisURL = os.Getenv("TEST_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6381"
	}

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, integrationSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	it := newIntegrationTriggerer()
	pub := publisher.NewForTest(it)

	workerCfg := worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     10000,
		ClaimTimeout: 2 * time.Second,
	}

	w := worker.New(store, transport, dockerRunner, pub, workerCfg)

	// The key difference from the existing tests: source code is supplied as a
	// real file (wire.FileInput), not inlined in the run command.
	// The manifest run command is ["python", "main.py"] — resolves relative to
	// the /workspace tmpfs where CopyToContainer places the file.
	mainPyContent := "name=input()\nprint(f\"hi {name}\")\n"

	jobID := fmt.Sprintf("integration-file-based-%d", time.Now().UnixNano())
	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		// Run main.py from the /workspace tmpfs — NOT python -c
		Run:         []string{"python", "main.py"},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Files: []wire.FileInput{
			{Name: "main.py", Content: mainPyContent},
		},
		Limits: wire.Limits{
			WallTimeMs: 30000,
			IdleMs:     10000,
			CpuMs:      15000,
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   512,
		},
	}

	require.NoError(t, store.WriteSpec(ctx, spec))
	require.NoError(t, store.Enqueue(ctx, jobID))

	// Run the worker loop in a goroutine.
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.Run(ctx)
	}()

	// Wait for the "queued" stage event.
	ok := it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stage" {
				var se wire.StageEvent
				if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseQueued {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for 'queued' stage event")

	// Brief pause for Redis pub/sub subscription establishment.
	time.Sleep(200 * time.Millisecond)

	// Send "start" control message.
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), startPayload).Err())

	// Wait for "running" stage.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stage" {
				var se wire.StageEvent
				if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseRunning {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for 'running' stage event")

	// Give Python a moment to reach input().
	time.Sleep(500 * time.Millisecond)

	// Send stdin: "world\n" — must use JSON StdinMessage format.
	publishStdinRaw(t, ctx, redisClient, jobID, "world\n")

	// Assert stdout contains "hi world" — proving main.py ran from the file.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil {
					if strings.Contains(oe.Chunk, "hi world") {
						return true
					}
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout containing 'hi world' from file-based main.py")

	// Assert terminal result exitCode 0.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "result" {
				var re wire.ResultEvent
				if json.Unmarshal(ev.data, &re) == nil && re.ExitCode != nil && *re.ExitCode == 0 {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for terminal result with exitCode=0")

	// Cancel the worker loop.
	cancel()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	// Assert no container leak.
	assertNoContainerLeak(t, dockerCli, jobID)

	// Print captured events.
	t.Logf("File-based job events (%d total):", len(it.allEvents()))
	for _, ev := range it.allEvents() {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}

// publishStdinRaw publishes a stdin chunk to the given jobID using the correct
// JSON-encoded StdinMessage format expected by the Redis stdin transport.
// The Redis transport (redis.go Subscribe) decodes StdinMessage JSON; raw bytes
// are silently dropped (bug discovered in Phase 4 abuse suite).
func publishStdinRaw(t *testing.T, ctx context.Context, rc *redis.Client, jobID, chunk string) {
	t.Helper()
	msg, err := json.Marshal(wire.StdinMessage{Chunk: chunk})
	require.NoError(t, err, "marshal StdinMessage")
	require.NoError(t, rc.Publish(ctx, fmt.Sprintf("stdin:%s", jobID), msg).Err(), "Publish stdin")
}

// stdoutChunks is a test utility that collects all stdout chunk content.
func stdoutChunks(evs []integrationEvent) string {
	var buf bytes.Buffer
	for _, ev := range evs {
		if ev.event == "stdout" {
			var oe wire.OutputChunkEvent
			if json.Unmarshal(ev.data, &oe) == nil {
				buf.WriteString(oe.Chunk)
			}
		}
	}
	return buf.String()
}

// hasResultWithExitCode returns true if any result event has the given exit code.
func hasResultWithExitCode(evs []integrationEvent, code int) bool {
	for _, ev := range evs {
		if ev.event == "result" {
			var re wire.ResultEvent
			if json.Unmarshal(ev.data, &re) == nil && re.ExitCode != nil && *re.ExitCode == code {
				return true
			}
		}
	}
	return false
}

// Ensure helpers are used (avoid "declared but not used" compiler errors).
var _ = stdoutChunks
var _ = hasResultWithExitCode
var _ = assert.True
