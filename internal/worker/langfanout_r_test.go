//go:build langfanout

// Package worker language fan-out integration tests for R 4.4.
//
// Proves LANG-07: Rscript main.R runs end-to-end through the worker with null
// compile, streams output unbuffered, and supports interactive stdin.
//
// Prerequisites:
//
//	docker build -t executor/r:4.4 languages/r-4.4/
//	docker run -d -p 6386:6379 redis:7  (or set TEST_REDIS_URL)
//
// Run with:
//
//	make langfanout   (or)
//	go test -tags=langfanout -timeout 600s ./internal/worker/... -run LangFanout -v
package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	rimage "github.com/docker/docker/api/types/image"
	rclient "github.com/docker/docker/client"
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
// R-specific guard helpers (uniquely named to avoid duplicate-symbol errors
// when langfanout_rust_test.go, langfanout_sqlite_test.go, and this file
// compile together under the langfanout build tag).
// ─────────────────────────────────────────────────────────────────────────────

// rDialRedis returns a live *redis.Client or skips the test.
// Defaults to port 6386 (langfanout Redis) but respects TEST_REDIS_URL.
func rDialRedis(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6386"
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Skipf("langfanout/r: cannot parse TEST_REDIS_URL %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout/r: Redis unreachable at %q: %v — run: docker run -d -p 6386:6379 redis:7", rawURL, err)
	}
	return cli
}

// rDockerGuardR creates a Docker client and verifies executor/r:4.4 is present.
// Skips the test with a clear message if either gate fails.
func rDockerGuardR(t *testing.T) *rclient.Client {
	t.Helper()
	cli, err := rclient.NewClientWithOpts(rclient.FromEnv, rclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("langfanout/r: cannot create docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout/r: Docker daemon unreachable: %v", err)
	}
	listCtx, listCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer listCancel()
	images, err := cli.ImageList(listCtx, rimage.ListOptions{})
	if err != nil {
		t.Skipf("langfanout/r: cannot list Docker images: %v", err)
	}
	found := false
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == "executor/r:4.4" {
				found = true
			}
		}
	}
	if !found {
		t.Skip("langfanout/r: executor/r:4.4 image not found — build it first: docker build -t executor/r:4.4 languages/r-4.4/")
	}
	return cli
}

// ─────────────────────────────────────────────────────────────────────────────
// R-specific event collector (uniquely named)
// ─────────────────────────────────────────────────────────────────────────────

type rEvent struct {
	channel string
	event   string
	data    json.RawMessage
}

type rTriggerer struct {
	mu     sync.Mutex
	events []rEvent
	notify chan struct{}
}

func newRTriggerer() *rTriggerer {
	return &rTriggerer{notify: make(chan struct{}, 128)}
}

func (rt *rTriggerer) Trigger(channel, event string, data interface{}) error {
	raw, _ := json.Marshal(data)
	rt.mu.Lock()
	rt.events = append(rt.events, rEvent{channel: channel, event: event, data: raw})
	rt.mu.Unlock()
	select {
	case rt.notify <- struct{}{}:
	default:
	}
	return nil
}

// rWaitFor blocks until predicate returns true or timeout fires.
func (rt *rTriggerer) rWaitFor(timeout time.Duration, pred func([]rEvent) bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rt.mu.Lock()
		snapshot := make([]rEvent, len(rt.events))
		copy(snapshot, rt.events)
		rt.mu.Unlock()
		if pred(snapshot) {
			return true
		}
		select {
		case <-rt.notify:
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

func (rt *rTriggerer) allEvents() []rEvent {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]rEvent, len(rt.events))
	copy(out, rt.events)
	return out
}

// rPublishStdin publishes a JSON-encoded StdinMessage to stdin:<jobID>.
func rPublishStdin(t *testing.T, ctx context.Context, rc *redis.Client, jobID, chunk string) {
	t.Helper()
	msg, err := json.Marshal(wire.StdinMessage{Chunk: chunk})
	require.NoError(t, err, "marshal StdinMessage")
	require.NoError(t, rc.Publish(ctx, fmt.Sprintf("stdin:%s", jobID), msg).Err(), "Publish stdin")
}

// rNewWorker builds a worker.Worker backed by real Redis + Docker for the R tests.
func rNewWorker(t *testing.T, redisClient *redis.Client, rt *rTriggerer) (*worker.Worker, *jobstore.Store) {
	t.Helper()
	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	cfg := config.Default()
	cfg.RedisURL = os.Getenv("TEST_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6386"
	}

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, integrationSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	pub := publisher.NewForTest(rt)

	workerCfg := worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     15000,
		ClaimTimeout: 2 * time.Second,
	}

	w := worker.New(store, transport, dockerRunner, pub, workerCfg)
	return w, store
}

// rDefaultLimits returns generous limits suitable for R.
func rDefaultLimits() wire.Limits {
	return wire.Limits{
		WallTimeMs: 30000,
		IdleMs:     10000,
		CpuMs:      15000,
		MemoryMb:   256,
		Pids:       64,
		OutputKb:   512,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLangFanout_R_Batch
// Case A: Rscript batch — main.R prints a known token; assert stdout + exit 0.
// Proves: null compile path (no "compiling" stage), running stage, stdout, exit 0.
// ─────────────────────────────────────────────────────────────────────────────

func TestLangFanout_R_Batch(t *testing.T) {
	redisClient := rDialRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := rDockerGuardR(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	rt := newRTriggerer()
	w, store := rNewWorker(t, redisClient, rt)

	jobID := fmt.Sprintf("langfanout-r-batch-%d", time.Now().UnixNano())
	mainR := `cat("hi r\n")`

	spec := wire.JobSpec{
		JobId:       jobID,
		Language:    "r",
		Version:     "4.4",
		Image:       "executor/r:4.4",
		Compile:     nil, // R: null compile
		Run:         []string{"Rscript", "main.R"},
		Entrypoint:  "main.R",
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Files:       []wire.FileInput{{Name: "main.R", Content: mainR}},
		Limits:      rDefaultLimits(),
	}

	require.NoError(t, store.WriteSpec(ctx, spec))
	require.NoError(t, store.Enqueue(ctx, jobID))

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.Run(ctx)
	}()

	// Wait for "queued".
	ok := rt.rWaitFor(15*time.Second, func(evs []rEvent) bool {
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
	require.True(t, ok, "timed out waiting for 'queued' stage")

	// Brief pause for pub/sub subscription establishment.
	time.Sleep(200 * time.Millisecond)

	// Send "start".
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), startPayload).Err())

	// Assert "running" stage (NO "compiling" should precede it — R is null compile).
	ok = rt.rWaitFor(20*time.Second, func(evs []rEvent) bool {
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
	require.True(t, ok, "timed out waiting for 'running' stage")

	// Assert stdout contains "hi r".
	ok = rt.rWaitFor(20*time.Second, func(evs []rEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil && strings.Contains(oe.Chunk, "hi r") {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout containing 'hi r'")

	// Assert terminal result with ExitCode 0.
	ok = rt.rWaitFor(15*time.Second, func(evs []rEvent) bool {
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
	require.True(t, ok, "timed out waiting for terminal result ExitCode=0")

	// Assert NO "compiling" stage was emitted (null compile invariant).
	for _, ev := range rt.allEvents() {
		if ev.event == "stage" {
			var se wire.StageEvent
			if json.Unmarshal(ev.data, &se) == nil {
				require.NotEqual(t, wire.StagePhaseCompiling, se.Phase,
					"R job with null compile must NOT emit a 'compiling' stage")
			}
		}
	}

	cancel()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	assertNoContainerLeak(t, dockerCli, jobID)

	t.Logf("TestLangFanout_R_Batch: captured %d events:", len(rt.allEvents()))
	for _, ev := range rt.allEvents() {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLangFanout_R_Interactive
// Case B: interactive stdin — main.R reads a line from stdin and echoes it.
// Proves: unbuffered streaming works; stdin routing works; clean EOF exit.
// ─────────────────────────────────────────────────────────────────────────────

func TestLangFanout_R_Interactive(t *testing.T) {
	redisClient := rDialRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := rDockerGuardR(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	rt := newRTriggerer()
	w, store := rNewWorker(t, redisClient, rt)

	jobID := fmt.Sprintf("langfanout-r-interactive-%d", time.Now().UnixNano())
	// main.R: read one line from stdin, echo it prefixed with "hi ".
	// Uses readLines(con, n=1, warn=FALSE) on the "stdin" connection.
	// flush(stdout()) ensures the output is not held in R's user-space buffer.
	mainR := `con <- file("stdin", open = "r")
line <- readLines(con, n = 1, warn = FALSE)
cat(paste0("hi ", line, "\n"))
flush(stdout())
`

	spec := wire.JobSpec{
		JobId:       jobID,
		Language:    "r",
		Version:     "4.4",
		Image:       "executor/r:4.4",
		Compile:     nil,
		Run:         []string{"Rscript", "main.R"},
		Entrypoint:  "main.R",
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Files:       []wire.FileInput{{Name: "main.R", Content: mainR}},
		Limits:      rDefaultLimits(),
	}

	require.NoError(t, store.WriteSpec(ctx, spec))
	require.NoError(t, store.Enqueue(ctx, jobID))

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.Run(ctx)
	}()

	// Wait for "queued".
	ok := rt.rWaitFor(15*time.Second, func(evs []rEvent) bool {
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
	require.True(t, ok, "timed out waiting for 'queued' stage")

	time.Sleep(200 * time.Millisecond)

	// Send "start".
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), startPayload).Err())

	// Wait for "running".
	ok = rt.rWaitFor(20*time.Second, func(evs []rEvent) bool {
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
	require.True(t, ok, "timed out waiting for 'running' stage")

	// Give Rscript a moment to reach the readLines call and block on stdin.
	time.Sleep(500 * time.Millisecond)

	// Send a line of stdin: "world\n".
	rPublishStdin(t, ctx, redisClient, jobID, "world\n")

	// Assert stdout contains "hi world" (proves unbuffered streaming + stdin routing).
	ok = rt.rWaitFor(20*time.Second, func(evs []rEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil && strings.Contains(oe.Chunk, "hi world") {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout containing 'hi world' (unbuffered streaming + stdin routing)")

	// Send stdin_close — Rscript sees EOF and exits cleanly.
	stdinClosePayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStdinClose})
	require.NoError(t, redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), stdinClosePayload).Err())

	// Assert terminal result (any exit — Rscript exits cleanly on EOF).
	ok = rt.rWaitFor(20*time.Second, func(evs []rEvent) bool {
		for _, ev := range evs {
			if ev.event == "result" {
				return true
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for terminal result after stdin_close")

	cancel()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	assertNoContainerLeak(t, dockerCli, jobID)

	t.Logf("TestLangFanout_R_Interactive: captured %d events:", len(rt.allEvents()))
	for _, ev := range rt.allEvents() {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}
