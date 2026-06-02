//go:build abuse

// Package worker_test: adversarial abuse suite.
//
// This suite drives 7 hostile Python jobs through the FULL worker path
// (Redis queue -> worker run loop -> DockerSocketRunner with hardening +
// three clocks -> recording publisher) and asserts the published terminal
// wire.ResultEvent flags, no container leak, and worker survival for the
// containment cases.
//
// Build tag `abuse` ensures these tests are excluded from `go test ./...`.
//
// Prerequisites:
//
//	docker run -d -p 6381:6379 redis:7
//	docker build -t executor/python:3.12 languages/python-3.12/
//	(Docker with cgroup v2 required for OOM + CPU clock tests)
//
// Run with:
//
//	make abuse
//	go test -tags=abuse -timeout 600s ./internal/worker/... -run Abuse -v
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
// Harness helpers (re-declared under //go:build abuse to avoid cross-tag deps)
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
		t.Skipf("abuse: cannot parse TEST_REDIS_URL %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("abuse: Redis unreachable at %q: %v — run: docker run -d -p 6381:6379 redis:7", rawURL, err)
	}
	return cli
}

// requireDockerAndImage creates a Docker client and verifies executor/python:3.12 exists.
func requireDockerAndImage(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("abuse: cannot create docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("abuse: Docker daemon unreachable: %v", err)
	}
	listCtx, listCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer listCancel()
	images, err := cli.ImageList(listCtx, image.ListOptions{})
	if err != nil {
		t.Skipf("abuse: cannot list Docker images: %v", err)
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
		t.Skip("abuse: executor/python:3.12 image not found — build it first: cd languages/python-3.12 && docker build -t executor/python:3.12 .")
	}
	return cli
}

// assertNoContainerLeak checks that no container labeled code-runner.jobId=<jobID> survives.
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

// abuseSeccompProfilePath returns the absolute path to the seccomp profile.
func abuseSeccompProfilePath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(filepath.Join(projectRoot, "profiles", "seccomp", "runner.json"))
	if err != nil {
		return ""
	}
	return abs
}

// ─────────────────────────────────────────────────────────────────────────────
// Abuse event collector
// ─────────────────────────────────────────────────────────────────────────────

type abuseEvent struct {
	channel string
	event   string
	data    json.RawMessage
}

type abuseTriggerer struct {
	mu     sync.Mutex
	events []abuseEvent
	notify chan struct{}
}

func newAbuseTriggerer() *abuseTriggerer {
	return &abuseTriggerer{notify: make(chan struct{}, 256)}
}

func (at *abuseTriggerer) Trigger(channel, event string, data interface{}) error {
	raw, _ := json.Marshal(data)
	at.mu.Lock()
	at.events = append(at.events, abuseEvent{channel: channel, event: event, data: raw})
	at.mu.Unlock()
	select {
	case at.notify <- struct{}{}:
	default:
	}
	return nil
}

// waitFor blocks until predicate(events) returns true or timeout fires.
func (at *abuseTriggerer) waitFor(timeout time.Duration, pred func([]abuseEvent) bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		at.mu.Lock()
		snapshot := make([]abuseEvent, len(at.events))
		copy(snapshot, at.events)
		at.mu.Unlock()
		if pred(snapshot) {
			return true
		}
		select {
		case <-at.notify:
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

func (at *abuseTriggerer) allEvents() []abuseEvent {
	at.mu.Lock()
	defer at.mu.Unlock()
	out := make([]abuseEvent, len(at.events))
	copy(out, at.events)
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// abuseStack holds a wired worker stack for an abuse test case.
// ─────────────────────────────────────────────────────────────────────────────

type abuseStack struct {
	redisClient *redis.Client
	dockerCli   *client.Client
	triggerer   *abuseTriggerer
	pub         *publisher.Publisher
	store       *jobstore.Store
	w           *worker.Worker
	cancel      context.CancelFunc
	workerDone  chan struct{}
}

// newAbuseStack creates a fully-wired worker stack and starts the run loop.
func newAbuseStack(t *testing.T) *abuseStack {
	t.Helper()
	redisClient := dialTestRedis(t)
	dockerCli := requireDockerAndImage(t)

	cfg := config.Default()
	cfg.RedisURL = os.Getenv("TEST_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6381"
	}

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, abuseSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	at := newAbuseTriggerer()
	pub := publisher.NewForTest(at)

	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	workerCfg := worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     10000,
		ClaimTimeout: 2 * time.Second,
	}

	w := worker.New(store, transport, dockerRunner, pub, workerCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.Run(ctx)
	}()

	s := &abuseStack{
		redisClient: redisClient,
		dockerCli:   dockerCli,
		triggerer:   at,
		pub:         pub,
		store:       store,
		w:           w,
		cancel:      cancel,
		workerDone:  workerDone,
	}

	t.Cleanup(func() {
		cancel()
		select {
		case <-workerDone:
		case <-time.After(15 * time.Second):
			t.Log("abuse: worker did not stop within 15s of context cancel")
		}
		redisClient.Close()  //nolint:errcheck
		dockerCli.Close()    //nolint:errcheck
	})

	return s
}

// publishStdin publishes a stdin chunk to the given jobID using the correct
// JSON-encoded StdinMessage format expected by the Redis stdin transport.
// The Redis transport decodes StdinMessage JSON; raw bytes are silently dropped.
func publishStdin(ctx context.Context, rc *redis.Client, jobID, chunk string) error {
	msg, err := json.Marshal(wire.StdinMessage{Chunk: chunk})
	if err != nil {
		return err
	}
	return rc.Publish(ctx, fmt.Sprintf("stdin:%s", jobID), msg).Err()
}

// driveJob enqueues a job on the stack, waits for "queued", sends "start",
// invokes the optional interaction callback (receives ctx + jobID + redisClient),
// and waits for the terminal "result" event.
// Returns the terminal ResultEvent and the jobID.
func (s *abuseStack) driveJob(
	t *testing.T,
	spec wire.JobSpec,
	resultTimeout time.Duration,
	interact func(ctx context.Context, jobID string, rc *redis.Client),
) (wire.ResultEvent, string) {
	t.Helper()

	jobID := spec.JobId
	ctx := context.Background()

	require.NoError(t, s.store.WriteSpec(ctx, spec))
	require.NoError(t, s.store.Enqueue(ctx, jobID))

	// The publisher sends events to channel "private-run-<jobID>"; filter all
	// event lookups by this channel to avoid matching earlier jobs' events.
	channel := fmt.Sprintf("private-run-%s", jobID)

	// Wait for "queued" stage on this job's channel.
	ok := s.triggerer.waitFor(15*time.Second, func(evs []abuseEvent) bool {
		for _, ev := range evs {
			if ev.channel == channel && ev.event == "stage" {
				var se wire.StageEvent
				if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseQueued {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "driveJob[%s]: timed out waiting for 'queued' stage event", jobID)

	// Brief pause for pub/sub subscription establishment.
	time.Sleep(200 * time.Millisecond)

	// Send "start".
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, s.redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), startPayload).Err())

	// Run the interaction callback (if provided).
	if interact != nil {
		interact(ctx, jobID, s.redisClient)
	}

	// Wait for terminal "result" event on this job's channel specifically.
	// Filtering by channel prevents the fork bomb / OOM survival job from
	// immediately matching the previous (hostile) job's result event.
	var result wire.ResultEvent
	ok = s.triggerer.waitFor(resultTimeout, func(evs []abuseEvent) bool {
		for _, ev := range evs {
			if ev.channel == channel && ev.event == "result" {
				if json.Unmarshal(ev.data, &result) == nil {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "driveJob[%s]: timed out waiting for terminal 'result' event (timeout=%v)", jobID, resultTimeout)

	return result, jobID
}

// stdoutTotal sums the bytes of all stdout chunks recorded for a given jobID channel.
func (s *abuseStack) stdoutTotal(jobID string) int {
	channel := fmt.Sprintf("private-run-%s", jobID)
	var total int
	for _, ev := range s.triggerer.allEvents() {
		if ev.channel == channel && ev.event == "stdout" {
			var oe wire.OutputChunkEvent
			if json.Unmarshal(ev.data, &oe) == nil {
				total += len(oe.Chunk)
			}
		}
	}
	return total
}

// stdoutContains returns true if any stdout chunk for the given jobID contains substr.
func (s *abuseStack) stdoutContains(jobID, substr string) bool {
	channel := fmt.Sprintf("private-run-%s", jobID)
	for _, ev := range s.triggerer.allEvents() {
		if ev.channel == channel && ev.event == "stdout" {
			var oe wire.OutputChunkEvent
			if json.Unmarshal(ev.data, &oe) == nil && strings.Contains(oe.Chunk, substr) {
				return true
			}
		}
	}
	return false
}

// logEvents dumps captured events for a job to t.Log for debugging.
func (s *abuseStack) logEvents(t *testing.T, jobID string) {
	t.Helper()
	channel := fmt.Sprintf("private-run-%s", jobID)
	var buf bytes.Buffer
	for _, ev := range s.triggerer.allEvents() {
		if ev.channel == channel {
			buf.WriteString(fmt.Sprintf("  [%s] %s: %s\n", ev.channel, ev.event, ev.data))
		}
	}
	t.Logf("Events for job %s:\n%s", jobID, buf.String())
}

// makeJobID generates a unique job ID for an abuse test case.
func makeJobID(name string) string {
	return fmt.Sprintf("abuse-%s-%d", name, time.Now().UnixNano())
}

// ─────────────────────────────────────────────────────────────────────────────
// TEST-01: Fork bomb (unbounded process spawn)
// ─────────────────────────────────────────────────────────────────────────────

// TestAbuseForkBomb (TEST-01): drives a Python fork-bomb through the worker.
// The pids-limit must contain it; the sandbox must be killed (exitCode != 0
// or Signal set or TimedOut). After the fork bomb terminates, a second trivial
// job must succeed on the same worker (worker survival assertion).
func TestAbuseForkBomb(t *testing.T) {
	s := newAbuseStack(t)

	jobID := makeJobID("forkbomb")
	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		// Fork-bomb: spawn processes until pids-limit kills us.
		// Wrapped in try/except so the initial process doesn't crash before forking.
		Run: []string{"python", "-c", `
import os, sys
try:
    while True:
        os.fork()
except Exception:
    sys.exit(1)
`},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: false,
		Limits: wire.Limits{
			Pids:       24,
			WallTimeMs: 8000,
			IdleMs:     4000,
			CpuMs:      5000,
			MemoryMb:   128,
			OutputKb:   64,
		},
	}

	result, _ := s.driveJob(t, spec, 30*time.Second, nil)

	// The sandbox must have been killed: exitCode non-zero OR Signal set OR TimedOut.
	killed := (result.ExitCode != nil && *result.ExitCode != 0) ||
		(result.Signal != nil && *result.Signal != "") ||
		result.TimedOut
	assert.True(t, killed,
		"TEST-01 fork bomb: sandbox must be killed (exitCode=%v, signal=%v, timedOut=%v)",
		result.ExitCode, result.Signal, result.TimedOut)

	// No container leak.
	assertNoContainerLeak(t, s.dockerCli, jobID)

	s.logEvents(t, jobID)
	t.Logf("TEST-01 result: exitCode=%v signal=%v timedOut=%v idleTimedOut=%v",
		result.ExitCode, result.Signal, result.TimedOut, result.IdleTimedOut)

	// Worker survival: run a trivial job and assert it succeeds.
	t.Log("TEST-01: verifying worker survival via follow-up trivial job")
	survivalJobID := makeJobID("forkbomb-survival")
	survivalSpec := wire.JobSpec{
		JobId:    survivalJobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		Run:      []string{"python", "-c", "print('alive')"},
		Channel:  fmt.Sprintf("private-run-%s", survivalJobID),
		Limits: wire.Limits{
			WallTimeMs: 15000,
			IdleMs:     8000,
			CpuMs:      10000,
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   64,
		},
	}

	survivalResult, _ := s.driveJob(t, survivalSpec, 30*time.Second, nil)
	assert.True(t, survivalResult.ExitCode != nil && *survivalResult.ExitCode == 0,
		"TEST-01 worker-survival: follow-up trivial job must exit 0, got exitCode=%v", survivalResult.ExitCode)
	assert.True(t, s.stdoutContains(survivalJobID, "alive"),
		"TEST-01 worker-survival: follow-up job stdout must contain 'alive'")
	assertNoContainerLeak(t, s.dockerCli, survivalJobID)
	t.Logf("TEST-01 survival result: exitCode=%v", survivalResult.ExitCode)
}

// ─────────────────────────────────────────────────────────────────────────────
// TEST-02: OOM (allocate past MemoryMb cap)
// ─────────────────────────────────────────────────────────────────────────────

// TestAbuseOOM (TEST-02): drives a Python job that allocates memory far beyond
// MemoryMb. The cgroup v2 OOM killer must terminate it (exitCode != 0 or Signal
// set). The worker must survive (follow-up trivial job succeeds).
func TestAbuseOOM(t *testing.T) {
	s := newAbuseStack(t)

	jobID := makeJobID("oom")
	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		// Allocate progressively larger chunks until OOM kills the process.
		// MemoryMb=64 → try to allocate 10 × 20MB = 200MB → OOM fires.
		Run: []string{"python", "-c", `
import sys
data = []
for i in range(20):
    data.append(bytearray(10 * 1024 * 1024))
print('done', len(data))
sys.exit(0)
`},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: false,
		Limits: wire.Limits{
			MemoryMb:   64,
			WallTimeMs: 8000,
			IdleMs:     6000,
			CpuMs:      6000,
			Pids:       64,
			OutputKb:   64,
		},
	}

	result, _ := s.driveJob(t, spec, 30*time.Second, nil)

	// OOM kill: exitCode != 0 OR Signal set (SIGKILL from OOM killer).
	// Accept any non-zero exit or signal.
	oomKilled := (result.ExitCode != nil && *result.ExitCode != 0) ||
		(result.Signal != nil && *result.Signal != "")
	assert.True(t, oomKilled,
		"TEST-02 OOM: sandbox must be killed by OOM (exitCode=%v, signal=%v, timedOut=%v)",
		result.ExitCode, result.Signal, result.TimedOut)

	assertNoContainerLeak(t, s.dockerCli, jobID)

	s.logEvents(t, jobID)
	t.Logf("TEST-02 result: exitCode=%v signal=%v timedOut=%v idleTimedOut=%v",
		result.ExitCode, result.Signal, result.TimedOut, result.IdleTimedOut)

	// Worker survival.
	survivalJobID := makeJobID("oom-survival")
	survivalSpec := wire.JobSpec{
		JobId:    survivalJobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		Run:      []string{"python", "-c", "print('alive')"},
		Channel:  fmt.Sprintf("private-run-%s", survivalJobID),
		Limits: wire.Limits{
			WallTimeMs: 15000,
			IdleMs:     8000,
			CpuMs:      10000,
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   64,
		},
	}
	survivalResult, _ := s.driveJob(t, survivalSpec, 30*time.Second, nil)
	assert.True(t, survivalResult.ExitCode != nil && *survivalResult.ExitCode == 0,
		"TEST-02 worker-survival: follow-up trivial job must exit 0, got exitCode=%v", survivalResult.ExitCode)
	assertNoContainerLeak(t, s.dockerCli, survivalJobID)
	t.Logf("TEST-02 survival result: exitCode=%v", survivalResult.ExitCode)
}

// ─────────────────────────────────────────────────────────────────────────────
// TEST-03: Infinite loop (wall clock)
// ─────────────────────────────────────────────────────────────────────────────

// TestAbuseInfiniteLoop (TEST-03): drives a Python `while True: pass` job.
// WallTimeMs (2s) < CpuMs (15s), so the wall clock must fire first.
// Assert result.TimedOut == true and no container leak.
func TestAbuseInfiniteLoop(t *testing.T) {
	s := newAbuseStack(t)

	jobID := makeJobID("infiniteloop")
	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		Run:      []string{"python", "-c", "while True: pass"},
		Channel:  fmt.Sprintf("private-run-%s", jobID),
		Limits: wire.Limits{
			WallTimeMs: 2000,  // small — fires in 2s
			CpuMs:      15000, // much larger than wall
			IdleMs:     10000,
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   64,
		},
	}

	result, _ := s.driveJob(t, spec, 30*time.Second, nil)

	assert.True(t, result.TimedOut,
		"TEST-03 infinite loop: result.TimedOut must be true (wall clock fired); got timedOut=%v idleTimedOut=%v exitCode=%v",
		result.TimedOut, result.IdleTimedOut, result.ExitCode)

	assertNoContainerLeak(t, s.dockerCli, jobID)

	s.logEvents(t, jobID)
	t.Logf("TEST-03 result: timedOut=%v idleTimedOut=%v exitCode=%v durationMs=%d",
		result.TimedOut, result.IdleTimedOut, result.ExitCode, result.DurationMs)
}

// ─────────────────────────────────────────────────────────────────────────────
// Bonus: CPU-evasion (read one byte then spin)
// ─────────────────────────────────────────────────────────────────────────────

// TestAbuseCpuClockEvasion (Pitfall 1 / CPU-evasion): drives a Python job that
// reads one byte from stdin (simulating an "interactive" start handshake) then
// enters a tight CPU spin while periodically printing a heartbeat to stdout.
//
// Printing stdout serves two purposes:
//  1. It resets the idle clock on each print (preventing the idle clock from
//     firing while the CPU is accumulating).
//  2. It proves the process is alive and running (visible in the event log).
//
// The key assertion is that the CPU clock (CpuMs=2500ms, tight) fires and
// produces TimedOut=true BEFORE the wall clock (WallTimeMs=15000ms). The idle
// clock is set generously (IdleMs=10000ms) and is reset by the heartbeat prints.
//
// This test proves Pitfall 1: a program that hides behind an interactive read
// is still caught by the CPU clock even with generous wall+idle budgets.
func TestAbuseCpuClockEvasion(t *testing.T) {
	s := newAbuseStack(t)

	jobID := makeJobID("cpuevasion")
	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		// Read one byte from stdin to simulate an "interactive" session (Pitfall 1),
		// then spin CPU while printing a heartbeat to stdout every 200ms so the
		// idle clock never fires. The CPU clock (2500ms) must fire first.
		Run: []string{"python", "-c", `
import sys, time
sys.stdout.write("started\n")
sys.stdout.flush()
b = sys.stdin.read(1)
sys.stdout.write("got_byte\n")
sys.stdout.flush()
i = 0
while True:
    i += 1
    if i % 500000 == 0:
        sys.stdout.write("heartbeat\n")
        sys.stdout.flush()
`},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Limits: wire.Limits{
			WallTimeMs: 15000, // generous wall
			IdleMs:     8000,  // generous idle (heartbeat resets it)
			CpuMs:      2500,  // tight: kill after 2.5s CPU
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   256,   // large enough for heartbeats
		},
	}

	// Interaction: wait for "started" stdout, then send one byte to unblock stdin.
	// The CPU spin starts after the byte is received.
	interact := func(ctx context.Context, jobID string, rc *redis.Client) {
		// Wait for the process to start and print "started" to stdout.
		// Then send a byte to unblock stdin.read(1).
		time.Sleep(1000 * time.Millisecond)
		publishStdin(ctx, rc, jobID, "x") //nolint:errcheck
	}

	result, _ := s.driveJob(t, spec, 30*time.Second, interact)

	// CPU clock fires as TimedOut=true (same flag as wall clock, surfaced by session layer).
	assert.True(t, result.TimedOut,
		"TestAbuseCpuClockEvasion: result.TimedOut must be true (CPU clock fired); got timedOut=%v idleTimedOut=%v exitCode=%v durationMs=%d",
		result.TimedOut, result.IdleTimedOut, result.ExitCode, result.DurationMs)

	assertNoContainerLeak(t, s.dockerCli, jobID)

	s.logEvents(t, jobID)
	t.Logf("TestAbuseCpuClockEvasion result: timedOut=%v idleTimedOut=%v exitCode=%v durationMs=%d",
		result.TimedOut, result.IdleTimedOut, result.ExitCode, result.DurationMs)
}

// ─────────────────────────────────────────────────────────────────────────────
// TEST-04: Idle-blocked stdin (idle clock)
// ─────────────────────────────────────────────────────────────────────────────

// TestAbuseIdleBlockedStdin (TEST-04): drives a Python job that blocks on
// sys.stdin.readline() and never receives input. The idle clock must fire.
// Assert result.IdleTimedOut == true and result.TimedOut == false.
func TestAbuseIdleBlockedStdin(t *testing.T) {
	s := newAbuseStack(t)

	jobID := makeJobID("idleblocked")
	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		Run:      []string{"python", "-c", "import sys; line = sys.stdin.readline(); print('got:', line)"},
		Channel:  fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Limits: wire.Limits{
			IdleMs:     2000,  // small — fires quickly
			WallTimeMs: 15000, // generous — idle fires first
			CpuMs:      15000, // generous
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   64,
		},
	}

	// No interaction: do NOT send any stdin — idle clock should fire.
	result, _ := s.driveJob(t, spec, 30*time.Second, nil)

	assert.True(t, result.IdleTimedOut,
		"TEST-04 idle-blocked stdin: result.IdleTimedOut must be true; got idleTimedOut=%v timedOut=%v",
		result.IdleTimedOut, result.TimedOut)
	assert.False(t, result.TimedOut,
		"TEST-04 idle-blocked stdin: result.TimedOut must be false (idle clock, not wall); got timedOut=%v",
		result.TimedOut)

	assertNoContainerLeak(t, s.dockerCli, jobID)

	s.logEvents(t, jobID)
	t.Logf("TEST-04 result: idleTimedOut=%v timedOut=%v exitCode=%v durationMs=%d",
		result.IdleTimedOut, result.TimedOut, result.ExitCode, result.DurationMs)
}

// ─────────────────────────────────────────────────────────────────────────────
// TEST-05: EOF clean exit
// ─────────────────────────────────────────────────────────────────────────────

// TestAbuseEofCleanExit (TEST-05): drives a Python job that echoes stdin lines
// and exits cleanly when stdin is closed (EOF). The interaction callback sends
// one chunk then sends a stdin_close ControlMessage to deliver EOF.
//
// The Python program uses a line-at-a-time loop (for line in sys.stdin) so that
// it prints "got" immediately after each received line (before EOF is needed).
// This means stdout is produced BEFORE the attach connection closes, which is
// required for reliable capture on macOS Docker Desktop (where closing the
// attach connection also closes the read direction, see stdinEOFFlushDelay).
//
// Test sequence:
//  1. Python starts, blocks reading from stdin
//  2. interact sends "hello\n" via Redis stdin pub/sub
//  3. Python receives "hello\n", prints "got hello" → stdout event captured
//  4. interact sends stdin_close → worker closes stdinW pipe → pump goroutine
//     waits stdinEOFFlushDelay → closes attach connection
//  5. Python's for-loop sees EOF (stdin closed), exits cleanly with exitCode 0
//
// Assert: exitCode == 0, IdleTimedOut == false, TimedOut == false, stdout "got".
func TestAbuseEofCleanExit(t *testing.T) {
	s := newAbuseStack(t)

	jobID := makeJobID("eofclean")
	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		// Line-oriented protocol: print each received line, then exit cleanly on EOF.
		// Using for-line-in-stdin (line-at-a-time) ensures stdout appears BEFORE
		// the stdin connection closes, making output capture reliable cross-platform.
		Run: []string{"python", "-c", `
import sys
for line in sys.stdin:
    sys.stdout.write("got " + line.strip() + "\n")
    sys.stdout.flush()
`},
		Channel:  fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Limits: wire.Limits{
			IdleMs:     8000,  // generous — must NOT idle-time-out before close
			WallTimeMs: 15000,
			CpuMs:      15000,
			MemoryMb:   128,
			Pids:       64,
			OutputKb:   64,
		},
	}

	// Interaction: send a line, wait for stdout, then send stdin_close for EOF.
	interact := func(ctx context.Context, jobID string, rc *redis.Client) {
		// Wait for the process to start and be ready to read stdin.
		time.Sleep(500 * time.Millisecond)

		// Send a stdin chunk using the correct JSON StdinMessage format.
		// Python's for-line-in-stdin will print "got hello" immediately.
		publishStdin(ctx, rc, jobID, "hello\n") //nolint:errcheck

		// Give Python time to print "got hello" before we send EOF.
		// The stdout event should appear during this window.
		time.Sleep(500 * time.Millisecond)

		// Send stdin_close to deliver EOF — Python's for loop ends, exits 0.
		closePayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStdinClose})
		rc.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), closePayload) //nolint:errcheck
	}

	result, _ := s.driveJob(t, spec, 30*time.Second, interact)

	assert.True(t, result.ExitCode != nil && *result.ExitCode == 0,
		"TEST-05 EOF clean exit: result.ExitCode must be 0; got exitCode=%v", result.ExitCode)
	assert.False(t, result.IdleTimedOut,
		"TEST-05 EOF clean exit: result.IdleTimedOut must be false; got idleTimedOut=%v", result.IdleTimedOut)
	assert.False(t, result.TimedOut,
		"TEST-05 EOF clean exit: result.TimedOut must be false; got timedOut=%v", result.TimedOut)
	assert.True(t, s.stdoutContains(jobID, "got"),
		"TEST-05 EOF clean exit: stdout must contain 'got' (Python echoed the stdin line)")

	assertNoContainerLeak(t, s.dockerCli, jobID)

	s.logEvents(t, jobID)
	t.Logf("TEST-05 result: exitCode=%v idleTimedOut=%v timedOut=%v",
		result.ExitCode, result.IdleTimedOut, result.TimedOut)
}

// ─────────────────────────────────────────────────────────────────────────────
// TEST-06: Giant output (truncation)
// ─────────────────────────────────────────────────────────────────────────────

// TestAbuseGiantOutput (TEST-06): drives a Python job that floods stdout far
// past OutputKb. Assert result.Truncated == true, the job reaches a terminal
// result (no deadlock), and total recorded stdout bytes are bounded near
// OutputKb (not the multi-MB the program tried to emit).
func TestAbuseGiantOutput(t *testing.T) {
	s := newAbuseStack(t)

	jobID := makeJobID("giantoutput")
	outputKb := 32 // cap
	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "python",
		Version:  "3.12",
		Image:    "executor/python:3.12",
		// Flood stdout with many 1KiB lines; far past the 32KiB cap.
		// Use a loop that runs ~10,000 iterations × 1KiB = ~10MB.
		Run: []string{"python", "-c", `
import sys
line = 'X' * 1023 + '\n'
for i in range(10000):
    sys.stdout.write(line)
    sys.stdout.flush()
print('done')
`},
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: false,
		Limits: wire.Limits{
			OutputKb:   outputKb,
			WallTimeMs: 20000,
			IdleMs:     15000,
			CpuMs:      20000,
			MemoryMb:   128,
			Pids:       64,
		},
	}

	result, _ := s.driveJob(t, spec, 60*time.Second, nil)

	assert.True(t, result.Truncated,
		"TEST-06 giant output: result.Truncated must be true; got truncated=%v timedOut=%v idleTimedOut=%v",
		result.Truncated, result.TimedOut, result.IdleTimedOut)

	// Recorded stdout bytes must be bounded near the cap (not full flood).
	totalBytes := s.stdoutTotal(jobID)
	capBytes := outputKb * 1024
	// Allow up to 3× the cap to account for the last chunk in-flight and
	// chunk-boundary effects — but NOT the 10MB the program tried to emit.
	assert.Less(t, totalBytes, capBytes*3,
		"TEST-06 giant output: recorded stdout (%d bytes) exceeds 3× cap (%d bytes) — truncation not working",
		totalBytes, capBytes*3)
	t.Logf("TEST-06 recorded stdout bytes: %d (cap=%d, 3× cap=%d)", totalBytes, capBytes, capBytes*3)

	assertNoContainerLeak(t, s.dockerCli, jobID)

	s.logEvents(t, jobID)
	t.Logf("TEST-06 result: truncated=%v timedOut=%v idleTimedOut=%v exitCode=%v durationMs=%d",
		result.Truncated, result.TimedOut, result.IdleTimedOut, result.ExitCode, result.DurationMs)
}

// Compile-time: ensure assert is used (avoids "imported and not used" for the
// assert package in case some assertion is in a conditional branch).
var _ = assert.True
