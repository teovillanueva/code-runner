//go:build langfanout

// Package worker langfanout SQLite integration test.
//
// Proves LANG-08: a submitted .sql file runs against an ephemeral :memory: DB AND
// an interactive sqlite3 shell returns SELECT rows over stdin, with a clean EOF
// (exit 0) — zero changes to the worker or API. compile is null.
//
// Prerequisites:
//
//	docker build -t executor/sqlite:3 languages/sqlite-3/
//	docker run -d -p 6386:6379 redis:7   (or set LANGFANOUT_REDIS_URL)
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

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
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

// sqliteRedisURL returns the Redis URL for langfanout tests (port 6386 by default).
func sqliteRedisURL() string {
	if v := os.Getenv("LANGFANOUT_REDIS_URL"); v != "" {
		return v
	}
	return "redis://localhost:6386"
}

// sqliteDialRedis returns a live *redis.Client or skips the test.
// Uses a uniquely-named function to avoid conflicts with other langfanout files.
func sqliteDialRedis(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := sqliteRedisURL()
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Skipf("langfanout-sqlite: cannot parse LANGFANOUT_REDIS_URL %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout-sqlite: Redis unreachable at %q: %v — run: docker run -d -p 6386:6379 redis:7", rawURL, err)
	}
	return cli
}

// sqliteDockerGuard creates a Docker client, pings the daemon, and verifies the
// executor/sqlite:3 image is present. Skips with a clear message if any gate fails.
// Named uniquely to avoid conflicts with other langfanout files in the same package.
func sqliteDockerGuard(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("langfanout-sqlite: cannot create docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout-sqlite: Docker daemon unreachable: %v", err)
	}
	// Verify the SQLite image is present.
	listCtx, listCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer listCancel()
	images, err := cli.ImageList(listCtx, image.ListOptions{})
	if err != nil {
		t.Skipf("langfanout-sqlite: cannot list Docker images: %v", err)
	}
	found := false
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == "executor/sqlite:3" {
				found = true
			}
		}
	}
	if !found {
		t.Skip("langfanout-sqlite: executor/sqlite:3 image not found — build it first: docker build -t executor/sqlite:3 languages/sqlite-3/")
	}
	return cli
}

// sqliteWorkerStack builds a ready-to-use worker stack backed by real Redis + Docker.
func sqliteWorkerStack(t *testing.T, rc *redis.Client) (*worker.Worker, *integrationTriggerer) {
	t.Helper()
	cfg := config.Default()
	cfg.RedisURL = sqliteRedisURL()

	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, integrationSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	it := newIntegrationTriggerer()
	pub := publisher.NewForTest(it)
	transport := stdintransport.NewRedis(rc)
	store := jobstore.New(rc)

	w := worker.New(store, transport, dockerRunner, pub, worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     10000,
		ClaimTimeout: 2 * time.Second,
	})
	return w, it
}

// sqliteLimits returns the modest job limits used by all SQLite langfanout tests.
func sqliteLimits() wire.Limits {
	return wire.Limits{
		WallTimeMs: 30000,
		IdleMs:     10000,
		CpuMs:      15000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}
}

// sqliteRunAndStart enqueues a JobSpec, starts the worker loop, waits for the
// "queued" stage, sends the "start" control, and waits for the "running" stage.
// Returns cancel (to shut down worker) and workerDone channel.
func sqliteRunAndStart(
	t *testing.T,
	ctx context.Context,
	rc *redis.Client,
	w *worker.Worker,
	it *integrationTriggerer,
	spec wire.JobSpec,
) (cancel context.CancelFunc, workerDone <-chan struct{}) {
	t.Helper()
	store := jobstore.New(rc)
	require.NoError(t, store.WriteSpec(ctx, spec))
	require.NoError(t, store.Enqueue(ctx, spec.JobId))

	wCtx, wCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(wCtx)
	}()

	// Wait for "queued" stage.
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

	// Brief pause to ensure pub/sub subscription is established.
	time.Sleep(200 * time.Millisecond)

	// Send "start".
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, rc.Publish(ctx, fmt.Sprintf("ctrl:%s", spec.JobId), startPayload).Err())

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

	return wCancel, done
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLangFanout_SQLite_FileJob
//
// Case A: A submitted .sql file creates a table, inserts rows, and SELECTs a
// known token. The manifest run argv (sqlite3 -batch :memory: -init main.sql)
// runs the file first; the worker then sends stdin_close to deliver EOF and
// assert the process exits 0 (clean EOF exit).
// ─────────────────────────────────────────────────────────────────────────────

func TestLangFanout_SQLite_FileJob(t *testing.T) {
	rc := sqliteDialRedis(t)
	defer rc.Close() //nolint:errcheck
	dockerCli := sqliteDockerGuard(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	w, it := sqliteWorkerStack(t, rc)

	jobID := fmt.Sprintf("langfanout-sqlite-file-%d", time.Now().UnixNano())

	// main.sql: create table, insert two rows, select a known token.
	mainSQL := "CREATE TABLE t(x);\nINSERT INTO t VALUES('hi sqlite');\nSELECT x FROM t;\n"

	spec := wire.JobSpec{
		JobId:    jobID,
		Language: "sqlite",
		Version:  "3",
		Image:    "executor/sqlite:3",
		// run argv exactly as declared in the manifest.
		Run:         []string{"sqlite3", "-batch", ":memory:", "-init", "main.sql"},
		Entrypoint:  "main.sql",
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Files: []wire.FileInput{
			{Name: "main.sql", Content: mainSQL},
		},
		Limits: sqliteLimits(),
	}

	wCancel, workerDone := sqliteRunAndStart(t, ctx, rc, w, it, spec)

	// Give sqlite3 time to execute the init file and emit its SELECT output.
	time.Sleep(500 * time.Millisecond)

	// Assert stdout contains the SELECT result token.
	ok := it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil && strings.Contains(oe.Chunk, "hi sqlite") {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout containing 'hi sqlite'")

	// Assert NO "compiling" stage event was published (compile is null).
	it.mu.Lock()
	for _, ev := range it.events {
		if ev.event == "stage" {
			var se wire.StageEvent
			if json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseCompiling {
				t.Error("unexpected 'compiling' stage event — compile must be null for SQLite")
			}
		}
	}
	it.mu.Unlock()

	// Send stdin_close (EOF) so sqlite3 exits cleanly.
	stdinClosePayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStdinClose})
	require.NoError(t, rc.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), stdinClosePayload).Err())

	// Assert terminal result with ExitCode 0 (clean EOF exit).
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
	require.True(t, ok, "timed out waiting for terminal result with exitCode=0 (clean EOF exit)")

	// Shut down worker.
	wCancel()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	// Assert no container leak.
	assertNoContainerLeak(t, dockerCli, jobID)

	// Log captured events.
	t.Logf("SQLite file-job captured %d events:", len(it.allEvents()))
	for _, ev := range it.allEvents() {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLangFanout_SQLite_InteractiveStdin
//
// Case B: Interactive stdin path. main.sql sets up a table only; after the
// "running" stage, an interactive SQL statement is sent over stdin ("SELECT
// 1+1;\n") and the result row ("2") is asserted in stdout. Then stdin_close
// delivers EOF and sqlite3 exits 0 cleanly (NOT idle-timeout).
// ─────────────────────────────────────────────────────────────────────────────

func TestLangFanout_SQLite_InteractiveStdin(t *testing.T) {
	rc := sqliteDialRedis(t)
	defer rc.Close() //nolint:errcheck
	dockerCli := sqliteDockerGuard(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	w, it := sqliteWorkerStack(t, rc)

	jobID := fmt.Sprintf("langfanout-sqlite-interactive-%d", time.Now().UnixNano())

	// main.sql: minimal setup — just create a table. Interactive statements
	// come over stdin after the "running" stage.
	mainSQL := "CREATE TABLE demo(val TEXT);\n"

	spec := wire.JobSpec{
		JobId:       jobID,
		Language:    "sqlite",
		Version:     "3",
		Image:       "executor/sqlite:3",
		Run:         []string{"sqlite3", "-batch", ":memory:", "-init", "main.sql"},
		Entrypoint:  "main.sql",
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: true,
		Files: []wire.FileInput{
			{Name: "main.sql", Content: mainSQL},
		},
		Limits: sqliteLimits(),
	}

	wCancel, workerDone := sqliteRunAndStart(t, ctx, rc, w, it, spec)

	// Give sqlite3 time to finish executing the init file.
	time.Sleep(500 * time.Millisecond)

	// Send interactive SQL over stdin: SELECT 1+1;
	publishStdinRaw(t, ctx, rc, jobID, "SELECT 1+1;\n")

	// Assert stdout contains the computed row "2".
	ok := it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil && strings.Contains(oe.Chunk, "2") {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout containing '2' (result of SELECT 1+1)")

	// Send more interactive SQL to further exercise the stdin path.
	publishStdinRaw(t, ctx, rc, jobID, "INSERT INTO demo VALUES('hello');\n")
	publishStdinRaw(t, ctx, rc, jobID, "SELECT val FROM demo;\n")

	// Assert stdout eventually contains "hello" from the SELECT.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "stdout" {
				var oe wire.OutputChunkEvent
				if json.Unmarshal(ev.data, &oe) == nil && strings.Contains(oe.Chunk, "hello") {
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for stdout containing 'hello' (interactive INSERT+SELECT)")

	// Send stdin_close (EOF) — sqlite3 must exit 0 cleanly (NOT idle-timeout).
	stdinClosePayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStdinClose})
	require.NoError(t, rc.Publish(ctx, fmt.Sprintf("ctrl:%s", jobID), stdinClosePayload).Err())

	// Assert terminal result with ExitCode 0 and NOT idle-timed-out.
	ok = it.waitFor(15*time.Second, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			if ev.event == "result" {
				var re wire.ResultEvent
				if json.Unmarshal(ev.data, &re) == nil && re.ExitCode != nil && *re.ExitCode == 0 {
					if re.IdleTimedOut {
						t.Errorf("sqlite3 exited via idle-timeout instead of clean EOF (stdin_close)")
					}
					return true
				}
			}
		}
		return false
	})
	require.True(t, ok, "timed out waiting for terminal result with exitCode=0 (clean EOF exit, not idle-timeout)")

	// Shut down worker.
	wCancel()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Error("worker did not stop after context cancel")
	}

	// Assert no container leak.
	assertNoContainerLeak(t, dockerCli, jobID)

	// Log captured events.
	t.Logf("SQLite interactive-stdin captured %d events:", len(it.allEvents()))
	for _, ev := range it.allEvents() {
		t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
	}
}
