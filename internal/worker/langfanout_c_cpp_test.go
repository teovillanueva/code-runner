//go:build langfanout

// Package worker_test — language fan-out integration tests for C and C++ (GCC 14).
//
// Unlike the Rust/R/SQLite fan-out tests, the job specs here are built from the
// real languages/*/manifest.json files, so a manifest change is exercised as-is.
//
// Prerequisites:
//
//	docker build -t executor/c:14 languages/c-14/
//	docker build -t executor/cpp:14 languages/cpp-14/
//	docker run -d -p 6386:6379 redis:7   (or set TEST_REDIS_URL)
//
// Run with:
//
//	make langfanout
//	# or:
//	go test -tags=langfanout -timeout 600s ./internal/worker/... -run LangFanout_CFamily -v
package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockerimage "github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/manifest"
	"github.com/teovillanueva/code-runner/internal/publisher"
	runnerPkg "github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/stdintransport"
	"github.com/teovillanueva/code-runner/internal/worker"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// cFamilyCase describes one C-family language under test.
type cFamilyCase struct {
	language string
	// echoSrc prints a prompt WITHOUT an explicit flush, blocks on stdin, then
	// echoes the line back. The prompt must reach the client before stdin is
	// sent — this proves the manifest's line-buffered stdout wrapper works.
	echoSrc string
	// brokenSrc fails to compile.
	brokenSrc string
	// promptSrc prints a prompt with NO trailing newline via printf, then reads
	// with scanf (the classic exam pattern). Line-buffered stdout alone holds
	// such a prompt until the next newline — i.e. until AFTER the user typed —
	// so this needs stdin unbuffered too (stdbuf -i0), which flushes stdout
	// before each read.
	promptSrc string
}

var cFamilyCases = []cFamilyCase{
	{
		language: "c",
		echoSrc: `#include <stdio.h>
int main(void) {
    char name[64];
    printf("name?\n");
    if (scanf("%63s", name) != 1) return 1;
    printf("hi %s\n", name);
    return 0;
}
`,
		brokenSrc: `int main(void) { return this_function_does_not_exist(); }
`,
		promptSrc: `#include <stdio.h>
int main(void) {
    int n;
    printf("n: ");
    if (scanf("%d", &n) != 1) return 1;
    printf("double: %d\n", 2 * n);
    return 0;
}
`,
	},
	{
		language: "cpp",
		echoSrc: `#include <iostream>
#include <string>
int main() {
    std::cout << "name?\n";
    std::string name;
    std::cin >> name;
    std::cout << "hi " << name << "\n";
}
`,
		brokenSrc: `int main() { return this_function_does_not_exist(); }
`,
		// iostream is safe (cin is tied to cout); printf/scanf in C++ is not.
		promptSrc: `#include <cstdio>
int main() {
    int n;
    std::printf("n: ");
    if (std::scanf("%d", &n) != 1) return 1;
    std::printf("double: %d\n", 2 * n);
    return 0;
}
`,
	},
}

// cppSlowCompileSrc takes seconds to compile (bits/stdc++.h pulls in the whole
// standard library: ~3 s on a laptop, never under a second), far past the
// 500 ms wall-time budget the test gives it. A constexpr spin loop would not
// do: GCC aborts it fast with a compile error (-fconstexpr-loop-limit).
const cppSlowCompileSrc = `#include <bits/stdc++.h>
int main() {
    std::vector<int> v{3, 1, 2};
    std::sort(v.begin(), v.end());
    return v[0] - 1;
}
`

// cSegfaultSrc dereferences a null pointer.
const cSegfaultSrc = `int main(void) {
    volatile int *p = 0;
    *p = 1;
    return 0;
}
`

func cFamilyRedisURL() string {
	if u := os.Getenv("TEST_REDIS_URL"); u != "" {
		return u
	}
	return "redis://localhost:6386"
}

// dialCFamilyRedis returns a live *redis.Client or skips the test.
func dialCFamilyRedis(t *testing.T) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(cFamilyRedisURL())
	if err != nil {
		t.Skipf("langfanout/c-family: cannot parse redis URL %q: %v", cFamilyRedisURL(), err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout/c-family: Redis unreachable at %q: %v — run: docker run -d -p 6386:6379 redis:7", cFamilyRedisURL(), err)
	}
	return cli
}

// requireDockerAndImage creates a Docker client and verifies image is present.
// Skips cleanly if not.
func requireDockerAndImage(t *testing.T, image string) *dockerclient.Client {
	t.Helper()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("langfanout/c-family: cannot create docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout/c-family: Docker daemon unreachable: %v", err)
	}
	images, err := cli.ImageList(ctx, dockerimage.ListOptions{})
	if err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("langfanout/c-family: cannot list Docker images: %v", err)
	}
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == image {
				return cli
			}
		}
	}
	cli.Close() //nolint:errcheck
	t.Skipf("langfanout/c-family: %s image not found — run: make build-images", image)
	return nil
}

// cFamilySpec builds a JobSpec for language from its real manifest.
func cFamilySpec(t *testing.T, language, jobID, src string) wire.JobSpec {
	t.Helper()
	reg, err := manifest.Load(filepath.Join("..", "..", "languages"))
	require.NoError(t, err, "manifest.Load")
	m, err := reg.Resolve(language, "")
	require.NoError(t, err, "Resolve(%q)", language)
	require.NotNil(t, m.Compile, "%s must be a compiled language", language)
	compile := wire.JobSpecCompile(*m.Compile)
	return wire.JobSpec{
		JobId:       jobID,
		Language:    m.Language,
		Version:     m.Version,
		Image:       m.Image,
		Entrypoint:  m.Entrypoint,
		Compile:     &compile,
		Run:         m.Run,
		Channel:     fmt.Sprintf("private-run-%s", jobID),
		Interactive: m.Interactive,
		Files:       []wire.FileInput{{Name: m.Entrypoint, Content: wire.Ptr(src)}},
		Limits:      m.DefaultLimits,
	}
}

// startCFamilyJob enqueues spec, runs a worker, and sends "start" once the job
// is claimed. The returned stop func cancels the worker and asserts no leak.
func startCFamilyJob(t *testing.T, spec wire.JobSpec) (*integrationTriggerer, *redis.Client, func()) {
	t.Helper()
	redisClient := dialCFamilyRedis(t)
	dockerCli := requireDockerAndImage(t, spec.Image)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)

	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	cfg := config.Default()
	cfg.RedisURL = cFamilyRedisURL()
	dockerRunner, err := runnerPkg.NewDockerSocketRunner(cfg, integrationSeccompProfilePath())
	require.NoError(t, err, "NewDockerSocketRunner")

	it := newIntegrationTriggerer()
	w := worker.New(store, transport, dockerRunner, publisher.NewForTest(it), worker.Config{
		MaxSandboxes: 2,
		WarmupMs:     30000,
		ClaimTimeout: 2 * time.Second,
	})

	require.NoError(t, store.WriteSpec(ctx, spec))
	require.NoError(t, store.Enqueue(ctx, spec.JobId))

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.Run(ctx)
	}()

	require.True(t, waitStage(it, 30*time.Second, wire.StagePhaseQueued), "timed out waiting for 'queued' stage event")
	// Brief pause for Redis pub/sub subscription establishment.
	time.Sleep(200 * time.Millisecond)
	startPayload, _ := json.Marshal(wire.ControlMessage{Type: wire.ControlTypeStart})
	require.NoError(t, redisClient.Publish(ctx, fmt.Sprintf("ctrl:%s", spec.JobId), startPayload).Err())

	stop := func() {
		cancel()
		select {
		case <-workerDone:
		case <-time.After(15 * time.Second):
			t.Error("worker did not stop after context cancel")
		}
		assertNoContainerLeak(t, dockerCli, spec.JobId)
		for _, ev := range it.allEvents() {
			t.Logf("  [%s] %s: %s", ev.channel, ev.event, ev.data)
		}
		dockerCli.Close()   //nolint:errcheck
		redisClient.Close() //nolint:errcheck
	}
	return it, redisClient, stop
}

func waitStage(it *integrationTriggerer, timeout time.Duration, phase wire.StagePhase) bool {
	return it.waitFor(timeout, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			var se wire.StageEvent
			if ev.event == "stage" && json.Unmarshal(ev.data, &se) == nil && se.Phase == phase {
				return true
			}
		}
		return false
	})
}

func waitOutput(it *integrationTriggerer, timeout time.Duration, stream, substr string) bool {
	return it.waitFor(timeout, func(evs []integrationEvent) bool {
		var sb strings.Builder
		for _, ev := range evs {
			var oe wire.OutputChunkEvent
			if ev.event == stream && json.Unmarshal(ev.data, &oe) == nil {
				sb.WriteString(oe.Chunk)
			}
		}
		return strings.Contains(sb.String(), substr)
	})
}

func waitResult(it *integrationTriggerer, timeout time.Duration, pred func(code int) bool) bool {
	return it.waitFor(timeout, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			var re wire.ResultEvent
			if ev.event == "result" && json.Unmarshal(ev.data, &re) == nil && re.ExitCode != nil && pred(int(*re.ExitCode)) {
				return true
			}
		}
		return false
	})
}

// TestLangFanout_CFamily_InteractiveRun: compile, see the unflushed prompt
// BEFORE sending stdin, then get the echo and a zero exit code.
func TestLangFanout_CFamily_InteractiveRun(t *testing.T) {
	for _, tc := range cFamilyCases {
		t.Run(tc.language, func(t *testing.T) {
			jobID := fmt.Sprintf("langfanout-%s-run-%d", tc.language, time.Now().UnixNano())
			spec := cFamilySpec(t, tc.language, jobID, tc.echoSrc)
			it, redisClient, stop := startCFamilyJob(t, spec)
			defer stop()

			require.True(t, waitStage(it, 30*time.Second, wire.StagePhaseCompiling), "timed out waiting for 'compiling' stage")
			require.True(t, waitStage(it, 120*time.Second, wire.StagePhaseRunning), "timed out waiting for 'running' stage")

			// The prompt is printed without a flush; it must still arrive while
			// the program is blocked on stdin.
			require.True(t, waitOutput(it, 10*time.Second, "stdout", "name?"),
				"prompt not delivered before stdin — stdout is not line-buffered")

			publishStdinRaw(t, context.Background(), redisClient, jobID, "gopher\n")

			require.True(t, waitOutput(it, 15*time.Second, "stdout", "hi gopher"), "timed out waiting for stdout 'hi gopher'")
			require.True(t, waitResult(it, 15*time.Second, func(code int) bool { return code == 0 }), "timed out waiting for result exitCode=0")
		})
	}
}

// TestLangFanout_CFamily_CompileError: a broken program forwards compiler
// diagnostics, ends with a non-zero exit code and never reaches "running".
func TestLangFanout_CFamily_CompileError(t *testing.T) {
	for _, tc := range cFamilyCases {
		t.Run(tc.language, func(t *testing.T) {
			jobID := fmt.Sprintf("langfanout-%s-err-%d", tc.language, time.Now().UnixNano())
			spec := cFamilySpec(t, tc.language, jobID, tc.brokenSrc)
			it, _, stop := startCFamilyJob(t, spec)
			defer stop()

			require.True(t, waitStage(it, 30*time.Second, wire.StagePhaseCompiling), "timed out waiting for 'compiling' stage")
			require.True(t, waitResult(it, 60*time.Second, func(code int) bool { return code != 0 }), "timed out waiting for non-zero result on compile error")
			require.True(t, waitOutput(it, time.Second, "compile_output", "error:"), "compiler diagnostics must be forwarded as compile_output")
			for _, ev := range it.allEvents() {
				var se wire.StageEvent
				if ev.event == "stage" && json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseRunning {
					t.Errorf("running stage must NOT be published after a compile error")
				}
			}
		})
	}
}

// waitResultEvent waits for a result event matching pred.
func waitResultEvent(it *integrationTriggerer, timeout time.Duration, pred func(re wire.ResultEvent) bool) bool {
	return it.waitFor(timeout, func(evs []integrationEvent) bool {
		for _, ev := range evs {
			var re wire.ResultEvent
			if ev.event == "result" && json.Unmarshal(ev.data, &re) == nil && pred(re) {
				return true
			}
		}
		return false
	})
}

// TestLangFanout_CFamily_PromptWithoutNewline: printf("n: ") + scanf — the
// prompt must reach the client BEFORE stdin is sent, even with no newline.
func TestLangFanout_CFamily_PromptWithoutNewline(t *testing.T) {
	for _, tc := range cFamilyCases {
		t.Run(tc.language, func(t *testing.T) {
			jobID := fmt.Sprintf("langfanout-%s-prompt-%d", tc.language, time.Now().UnixNano())
			spec := cFamilySpec(t, tc.language, jobID, tc.promptSrc)
			it, redisClient, stop := startCFamilyJob(t, spec)
			defer stop()

			require.True(t, waitStage(it, 120*time.Second, wire.StagePhaseRunning), "timed out waiting for 'running' stage")
			require.True(t, waitOutput(it, 10*time.Second, "stdout", "n: "),
				"prompt without a trailing newline not delivered before stdin — stdin/stdout buffering holds it")

			publishStdinRaw(t, context.Background(), redisClient, jobID, "21\n")

			require.True(t, waitOutput(it, 15*time.Second, "stdout", "double: 42"), "timed out waiting for stdout 'double: 42'")
			require.True(t, waitResult(it, 15*time.Second, func(code int) bool { return code == 0 }), "timed out waiting for result exitCode=0")
		})
	}
}

// TestLangFanout_CFamily_CompileStoppedAtWallTime: a compile that outlives the
// wall-time budget is stopped at the deadline; the run never starts.
func TestLangFanout_CFamily_CompileStoppedAtWallTime(t *testing.T) {
	jobID := fmt.Sprintf("langfanout-cpp-slowcompile-%d", time.Now().UnixNano())
	spec := cFamilySpec(t, "cpp", jobID, cppSlowCompileSrc)
	spec.Limits.WallTimeMs = 500
	it, _, stop := startCFamilyJob(t, spec)
	defer stop()

	require.True(t, waitStage(it, 30*time.Second, wire.StagePhaseCompiling), "timed out waiting for 'compiling' stage")
	compilingAt := time.Now()
	require.True(t, waitResultEvent(it, 20*time.Second, func(re wire.ResultEvent) bool { return re.TimedOut }),
		"a compile past the wall-time limit must end with timedOut=true")
	require.Less(t, time.Since(compilingAt), 2500*time.Millisecond, "the compile must be stopped near the 500 ms wall-time limit, not when g++ finishes")
	for _, ev := range it.allEvents() {
		var se wire.StageEvent
		if ev.event == "stage" && json.Unmarshal(ev.data, &se) == nil && se.Phase == wire.StagePhaseRunning {
			t.Errorf("running stage must NOT be published after a compile timeout")
		}
	}
}

// TestLangFanout_CFamily_Segfault: a null-pointer write ends with SIGSEGV
// (exit 139 from the container's PID 1) — and, with the core ulimit at 0, no
// core file is written into /workspace.
func TestLangFanout_CFamily_Segfault(t *testing.T) {
	jobID := fmt.Sprintf("langfanout-c-segv-%d", time.Now().UnixNano())
	spec := cFamilySpec(t, "c", jobID, cSegfaultSrc)
	it, _, stop := startCFamilyJob(t, spec)
	defer stop()

	require.True(t, waitStage(it, 120*time.Second, wire.StagePhaseRunning), "timed out waiting for 'running' stage")
	require.True(t, waitResultEvent(it, 20*time.Second, func(re wire.ResultEvent) bool {
		return (re.ExitCode != nil && int(*re.ExitCode) == 139) || (re.Signal != nil && *re.Signal == "SIGSEGV")
	}), "a segfault must end with exit 139 / SIGSEGV")
}
