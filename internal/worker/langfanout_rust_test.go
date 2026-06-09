//go:build langfanout

// Package worker_test — language fan-out integration test for Rust 1.83.
//
// Prerequisites:
//
//	docker build -t executor/rust:1.83 languages/rust-1.83/
//	docker run -d -p 6387:6379 redis:7   (or set TEST_REDIS_URL)
//
// Run with:
//
//	make langfanout
//	# or:
//	go test -tags=langfanout -timeout 600s ./internal/worker/... -run LangFanout -v
package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	dockerimage "github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/redis/go-redis/v9"
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
// Rust-specific harness helpers
// ─────────────────────────────────────────────────────────────────────────────

// dialRustRedis returns a live *redis.Client or skips the test.
// Defaults to port 6387 (the Rust langfanout Redis port).
func dialRustRedis(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6387"
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Skipf("langfanout/rust: cannot parse TEST_REDIS_URL %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout/rust: Redis unreachable at %q: %v — run: docker run -d -p 6387:6379 redis:7", rawURL, err)
	}
	return cli
}

// requireDockerAndRustImage creates a Docker client, pings the daemon, and
// verifies the executor/rust:1.83 image is present. Skips cleanly if not.
func requireDockerAndRustImage(t *testing.T) *dockerclient.Client {
	t.Helper()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("langfanout/rust: cannot create docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout/rust: Docker daemon unreachable: %v", err)
	}
	listCtx, listCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer listCancel()
	images, err := cli.ImageList(listCtx, dockerimage.ListOptions{})
	if err != nil {
		t.Skipf("langfanout/rust: cannot list Docker images: %v", err)
	}
	found := false
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == "executor/rust:1.83" {
				found = true
			}
		}
	}
	if !found {
		t.Skip("langfanout/rust: executor/rust:1.83 image not found — run: docker build -t executor/rust:1.83 languages/rust-1.83/")
	}
	return cli
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLangFanout_Rust_CompileAndRun
//
// Case A: submit a Rust main.rs that reads a line from stdin and echoes it.
// Expected flow: queued → compiling → running → stdout "hi rustacean" → result exitCode 0.
// ─────────────────────────────────────────────────────────────────────────────

func TestLangFanout_Rust_CompileAndRun(t *testing.T) {
	redisClient := dialRustRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := requireDockerAndRustImage(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	cfg := config.Default()
	cfg.RedisURL = os.Getenv("TEST_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6387"
	}

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, integrationSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	it := newIntegrationTriggerer()
	pub := publisher.NewForTest(it)

	workerCfg := worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     30000,
		ClaimTimeout: 2 * time.Second,
	}

	w := worker.New(store, transport, dockerRunner, pub, workerCfg)

	// Rust program: reads one line from stdin, prints "hi <line>".
	mainRsContent := `use std::io::{self, BufRead};
fn main() {
    let stdin = io::stdin();
    let mut line = String::new();
    stdin.lock().read_line(&mut line).unwrap();
    let trimmed = line.trim();
    println!("hi {}", trimmed);
}
`

	jobID := fmt.Sprintf("langfanout-rust-run-%d", time.Now().UnixNano())
	compile := wire.JobSpecCompile([]string{"rustc", "-O", "main.rs", "-o", "/workspace/prog"})
	spec := wire.JobSpec{
		JobId:       jobID,
		Language:    "rust",
		Version:     "1.83",
		Image:       "executor/rust:1.83",
		Compile:     &compile,
		Run:         []string{"/workspace/prog"},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Files: []wire.FileInput{
			{Name: "main.rs", Content: wire.Ptr(mainRsContent)},
		},
		Limits: wire.Limits{
			WallTimeMs: 120000,
			IdleMs:     15000,
			CpuMs:      60000,
			MemoryMb:   512,
			Pids:       128,
			OutputKb:   1024,
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

	// Wait for "queued" stage (worker claimed the job).
	ok := it.waitFor(30*time.Second, func(evs []integrationEvent) bool {
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

	// Wait for "compiling" stage (rustc compile in progress).
	ok = it.waitFor(30*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stage" {
				var se wire.StageEvent
				if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseCompiling {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for 'compiling' stage event")

	// Wait for "running" stage (binary started).
	ok = it.waitFor(120*time.Second, func(evs []integrationEvent) bool {
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
	require.True(t, ok, "timed out waiting for 'running' stage event (rustc compile may have taken > 120s)")

	// Give the binary a moment to start and block on stdin.
	time.Sleep(300 * time.Millisecond)

	// Send stdin: "rustacean\n".
	publishStdinRaw(t, ctx, redisClient, jobID, "rustacean\n")

	// Wait for stdout containing the echoed token.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil {
					if strings.Contains(oe.Chunk, "hi rustacean") {
						return true
					}
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout containing 'hi rustacean'")

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
	require.True(t, ok, "timed out waiting for terminal result with exitCode=0")

	// Cancel the worker loop.
	cancel()
	select {
	case <-workerDone:
	case <-time.After(15 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	// Assert no container leak.
	assertNoContainerLeak(t, dockerCli, jobID)

	// Log captured events.
	allEvs := it.allEvents()
	t.Logf("TestLangFanout_Rust_CompileAndRun: captured %d events:", len(allEvs))
	for _, ev := range allEvs {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLangFanout_Rust_CompileError
//
// Case B: submit a Rust main.rs with a deliberate compile error.
// Expected: compiling stage published, compiler stderr forwarded (contains "error"),
// terminal result has NON-ZERO exitCode, "running" stage NEVER published.
// ─────────────────────────────────────────────────────────────────────────────

func TestLangFanout_Rust_CompileError(t *testing.T) {
	redisClient := dialRustRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := requireDockerAndRustImage(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	cfg := config.Default()
	cfg.RedisURL = os.Getenv("TEST_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6387"
	}

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, integrationSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	it := newIntegrationTriggerer()
	pub := publisher.NewForTest(it)

	workerCfg := worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     30000,
		ClaimTimeout: 2 * time.Second,
	}

	w := worker.New(store, transport, dockerRunner, pub, workerCfg)

	// Deliberately broken Rust program: references an undefined symbol.
	brokenRsContent := `fn main() {
    let x = this_function_does_not_exist();
    println!("{}", x);
}
`

	jobID := fmt.Sprintf("langfanout-rust-err-%d", time.Now().UnixNano())
	compile := wire.JobSpecCompile([]string{"rustc", "-O", "main.rs", "-o", "/workspace/prog"})
	spec := wire.JobSpec{
		JobId:       jobID,
		Language:    "rust",
		Version:     "1.83",
		Image:       "executor/rust:1.83",
		Compile:     &compile,
		Run:         []string{"/workspace/prog"},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Files: []wire.FileInput{
			{Name: "main.rs", Content: wire.Ptr(brokenRsContent)},
		},
		Limits: wire.Limits{
			WallTimeMs: 120000,
			IdleMs:     15000,
			CpuMs:      60000,
			MemoryMb:   512,
			Pids:       128,
			OutputKb:   1024,
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

	// Wait for "queued" stage.
	ok := it.waitFor(30*time.Second, func(evs []integrationEvent) bool {
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

	// Send "start".
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), startPayload).Err())

	// Wait for "compiling" stage.
	ok = it.waitFor(30*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stage" {
				var se wire.StageEvent
				if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseCompiling {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for 'compiling' stage event")

	// Wait for terminal result (compile failure exits quickly).
	ok = it.waitFor(60*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "result" {
				var re wire.ResultEvent
				if json.Unmarshal(ev.data, &re) == nil && re.ExitCode != nil && *re.ExitCode != 0 {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for terminal result with non-zero exitCode on compile error")

	// Assert: "running" stage was NEVER published.
	for _, ev := range it.allEvents() {
		if ev.event == "stage" {
			var se wire.StageEvent
			if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseRunning {
				t.Errorf("running stage must NOT be published after a compile error")
			}
		}
	}

	// Assert: compiler stderr was forwarded (at least one stderr event containing "error").
	foundStderr := false
	for _, ev := range it.allEvents() {
		if ev.event == "stderr" {
			var oe wire.OutputChunkEvent
			if json.Unmarshal(ev.data, &oe) == nil && strings.Contains(oe.Chunk, "error") {
				foundStderr = true
				break
			}
		}
	}
	require.True(t, foundStderr, "compiler stderr containing 'error' must be forwarded to the client")

	// Cancel the worker loop.
	cancel()
	select {
	case <-workerDone:
	case <-time.After(15 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	// Assert no container leak.
	assertNoContainerLeak(t, dockerCli, jobID)

	// Log captured events.
	allEvs := it.allEvents()
	t.Logf("TestLangFanout_Rust_CompileError: captured %d events:", len(allEvs))
	for _, ev := range allEvs {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}
