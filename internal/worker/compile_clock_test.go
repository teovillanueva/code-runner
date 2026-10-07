// Package worker_test — compile-step clocks and control messages.
//
// The compile step shares the session's wall-time budget (Limits.WallTimeMs is
// the max total lifetime of the session) and honours a kill sent while it runs:
//
//  1. A compile that does not finish is stopped at the wall-time deadline:
//     terminal result timedOut=true, signal SIGKILL, no exit code; run never starts.
//  2. A kill sent while compiling stops the compile at once (it used to wait for
//     the compiler, because the run-phase ctrl consumer only starts after it).
//  3. A stdin_close sent while compiling is not lost: the run phase still gets it.
//  4. The run step gets what is left of the wall-time budget after compiling.
//
// No Docker required; runs under plain `go test ./...`.
package worker_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/worker"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// slowCompileSandbox is a compileSandbox whose Compile takes compileDelay, or
// blocks until its context is done when compileDelay < 0.
type slowCompileSandbox struct {
	*compileSandbox
	compileDelay time.Duration

	ctxMu         sync.Mutex
	compileCtxErr error
}

func newSlowCompileSandbox(compileDelay, runDelay time.Duration) *slowCompileSandbox {
	cs := newCompileSandbox(0, nil)
	cs.inner.waitDelay = runDelay
	return &slowCompileSandbox{compileSandbox: cs, compileDelay: compileDelay}
}

func (s *slowCompileSandbox) Compile(ctx context.Context, _ []string, _ func([]byte)) (runner.CompileResult, error) {
	s.mu.Lock()
	s.compileCalled = true
	s.mu.Unlock()

	var finished <-chan time.Time
	if s.compileDelay >= 0 {
		finished = time.After(s.compileDelay)
	}
	select {
	case <-finished:
		return runner.CompileResult{ExitCode: 0, DurationMs: int(s.compileDelay.Milliseconds())}, nil
	case <-ctx.Done():
		s.ctxMu.Lock()
		s.compileCtxErr = ctx.Err()
		s.ctxMu.Unlock()
		return runner.CompileResult{DurationMs: 1}, ctx.Err()
	}
}

func (s *slowCompileSandbox) ctxErr() error {
	s.ctxMu.Lock()
	defer s.ctxMu.Unlock()
	return s.compileCtxErr
}

// startCompileJob starts the job, sends "start" and returns without waiting, so
// the test can send control messages while the job runs.
func startCompileJob(t *testing.T, spec wire.JobSpec, sb runner.Sandbox) (*fakeTriggerer, *inMemoryControlTransport, <-chan struct{}) {
	t.Helper()
	pub, ft := newFakePublisher(t)
	inMem := newInMemoryControlTransport()
	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 500}
	w := worker.NewWithTransport(nil, inMem, &compileSandboxRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, spec.JobId, wire.ControlMessage{Type: wire.ControlTypeStart})
	return ft, inMem, done
}

func waitJob(t *testing.T, done <-chan struct{}, within time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("job did not finish within %s", within)
	}
}

func hasRunningStage(phases []wire.StagePhase) bool {
	for _, p := range phases {
		if p == wire.StagePhaseRunning {
			return true
		}
	}
	return false
}

func TestWorker_Compile_StoppedAtWallTimeDeadline(t *testing.T) {
	sb := newSlowCompileSandbox(-1, 0) // never finishes on its own
	spec := compileSpec("compile-wall-001", []string{"/usr/bin/compile-tool", "main.src"})
	spec.Limits.WallTimeMs = 300

	start := time.Now()
	events, _, done := startCompileJob(t, spec, sb)
	waitJob(t, done, 3*time.Second)
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 300*time.Millisecond, "compile must run until the wall-time deadline")
	assert.ErrorIs(t, sb.ctxErr(), context.DeadlineExceeded, "compile context must hit the deadline")
	assert.False(t, sb.wasRunInteractiveCalled(), "run must not start after a compile timeout")
	assert.False(t, hasRunningStage(events.stagePhases()), "running stage must not be published")

	re := events.resultEvent()
	require.NotNil(t, re, "terminal result event must be published")
	assert.True(t, re.TimedOut, "a compile stopped at the deadline is a timeout")
	assert.Nil(t, re.ExitCode, "the compiler did not exit on its own: no exit code")
	require.NotNil(t, re.Signal)
	assert.Equal(t, "SIGKILL", *re.Signal)
}

func TestWorker_Compile_KillWhileCompiling(t *testing.T) {
	sb := newSlowCompileSandbox(-1, 0)
	spec := compileSpec("compile-kill-001", []string{"/usr/bin/compile-tool", "main.src"})
	spec.Limits.WallTimeMs = 30_000

	events, inMem, done := startCompileJob(t, spec, sb)
	time.Sleep(100 * time.Millisecond) // compiling now
	killAt := time.Now()
	inMem.PublishControl(context.Background(), spec.JobId, wire.ControlMessage{Type: wire.ControlTypeKill})
	waitJob(t, done, 2*time.Second)

	assert.Less(t, time.Since(killAt), time.Second, "kill must stop the compile at once")
	assert.ErrorIs(t, sb.ctxErr(), context.Canceled, "kill must cancel the compile context")
	assert.False(t, sb.wasRunInteractiveCalled(), "run must not start after a kill")

	re := events.resultEvent()
	require.NotNil(t, re, "terminal result event must be published")
	assert.False(t, re.TimedOut, "a kill is not a timeout")
	require.NotNil(t, re.Signal)
	assert.Equal(t, "SIGKILL", *re.Signal)
}

func TestWorker_Compile_StdinCloseDuringCompileReachesRun(t *testing.T) {
	sb := newSlowCompileSandbox(300*time.Millisecond, 2*time.Second)
	spec := compileSpec("compile-stdinclose-001", []string{"/usr/bin/compile-tool", "main.src"})

	eof := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, sb.inner.stdinPR) // returns when the run phase closes stdin
		close(eof)
	}()

	_, inMem, done := startCompileJob(t, spec, sb)
	time.Sleep(100 * time.Millisecond) // compiling now
	inMem.PublishControl(context.Background(), spec.JobId, wire.ControlMessage{Type: wire.ControlTypeStdinClose})

	select {
	case <-eof:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("stdin_close sent while compiling never reached the run phase")
	}
	inMem.PublishControl(context.Background(), spec.JobId, wire.ControlMessage{Type: wire.ControlTypeKill})
	waitJob(t, done, 3*time.Second)
	// The held stdin_close is consumed by the run-phase goroutine, which can
	// close stdin before RunInteractive reaches Wait — so check this at the end.
	assert.True(t, sb.wasRunInteractiveCalled(), "run must start after a successful compile")
}

func TestWorker_Compile_RunGetsRemainingWallTime(t *testing.T) {
	// Budget 1000 ms. The run alone takes 700 ms: it fits the full budget, but
	// not what is left after a 600 ms compile.
	t.Run("after a slow compile the run times out", func(t *testing.T) {
		sb := newSlowCompileSandbox(600*time.Millisecond, 700*time.Millisecond)
		spec := compileSpec("compile-remaining-001", []string{"/usr/bin/compile-tool", "main.src"})
		spec.Limits.WallTimeMs = 1000
		spec.Limits.IdleMs = 5000

		events, _, done := startCompileJob(t, spec, sb)
		waitJob(t, done, 3*time.Second)

		require.True(t, sb.wasRunInteractiveCalled(), "run must start after a successful compile")
		re := events.resultEvent()
		require.NotNil(t, re)
		assert.True(t, re.TimedOut, "the run must only get the wall time left after compiling")
	})

	t.Run("after an instant compile the same run fits", func(t *testing.T) {
		sb := newSlowCompileSandbox(0, 700*time.Millisecond)
		spec := compileSpec("compile-remaining-002", []string{"/usr/bin/compile-tool", "main.src"})
		spec.Limits.WallTimeMs = 1000
		spec.Limits.IdleMs = 5000

		events, _, done := startCompileJob(t, spec, sb)
		waitJob(t, done, 3*time.Second)

		re := events.resultEvent()
		require.NotNil(t, re)
		assert.False(t, re.TimedOut)
	})
}
