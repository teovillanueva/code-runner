package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/publisher"
	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/session"
	"github.com/teovillanueva/code-runner/internal/stdintransport"
	"github.com/teovillanueva/code-runner/internal/worker"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fake triggerer for the publisher (implements publisher.Triggerer)
// ─────────────────────────────────────────────────────────────────────────────

type capturedEvent struct {
	channel string
	event   string
	data    interface{}
}

type fakeTriggerer struct {
	mu     sync.Mutex
	events []capturedEvent
}

func (f *fakeTriggerer) Trigger(channel, event string, data interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, capturedEvent{channel: channel, event: event, data: data})
	return nil
}

func (f *fakeTriggerer) all() []capturedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedEvent, len(f.events))
	copy(out, f.events)
	return out
}

func (f *fakeTriggerer) stagePhases() []wire.StagePhase {
	var phases []wire.StagePhase
	for _, ev := range f.all() {
		if ev.event != "stage" {
			continue
		}
		raw, _ := json.Marshal(ev.data)
		var se wire.StageEvent
		if err := json.Unmarshal(raw, &se); err == nil {
			phases = append(phases, se.Phase)
		}
	}
	return phases
}

func (f *fakeTriggerer) resultEvent() *wire.ResultEvent {
	for _, ev := range f.all() {
		if ev.event != "result" {
			continue
		}
		raw, _ := json.Marshal(ev.data)
		var re wire.ResultEvent
		if err := json.Unmarshal(raw, &re); err == nil {
			return &re
		}
	}
	return nil
}

func newFakePublisher(t *testing.T) (*publisher.Publisher, *fakeTriggerer) {
	t.Helper()
	ft := &fakeTriggerer{}
	return publisher.NewForTest(ft), ft
}

// ─────────────────────────────────────────────────────────────────────────────
// scriptedSandbox — fake runner.Sandbox for worker tests
// ─────────────────────────────────────────────────────────────────────────────

type scriptedSandbox struct {
	mu           sync.Mutex
	cleanupCount int

	stdout    *bytes.Reader
	stderr    *bytes.Reader
	stdinImpl io.WriteCloser // injectable stdin (defaults to a pipe)
	stdinPR   *io.PipeReader // the read side of the default pipe (for inspection)

	waitResult runner.Result
	waitErr    error
	waitDelay  time.Duration
}

func newScriptedSandbox() *scriptedSandbox {
	pr, pw := io.Pipe()
	return &scriptedSandbox{
		stdout:     bytes.NewReader(nil),
		stderr:     bytes.NewReader(nil),
		stdinImpl:  pw,
		stdinPR:    pr,
		waitResult: runner.Result{ExitCode: intPtr(0)},
	}
}

func (s *scriptedSandbox) Stdin() io.WriteCloser { return s.stdinImpl }
func (s *scriptedSandbox) Stdout() io.Reader     { return s.stdout }
func (s *scriptedSandbox) Stderr() io.Reader     { return s.stderr }

func (s *scriptedSandbox) Wait(ctx context.Context) (runner.Result, error) {
	if s.waitDelay > 0 {
		select {
		case <-time.After(s.waitDelay):
		case <-ctx.Done():
			return runner.Result{}, ctx.Err()
		}
	}
	return s.waitResult, s.waitErr
}

func (s *scriptedSandbox) Kill(_ context.Context) error {
	// Do NOT close stdinImpl — the real docker sandbox's Kill sends SIGKILL to
	// the container but does NOT close the attach stdin pipe. The worker owns
	// stdin closing via its sync.Once closeStdin().
	return nil
}

func (s *scriptedSandbox) Cleanup() error {
	s.mu.Lock()
	s.cleanupCount++
	s.mu.Unlock()
	return nil
}

// Compile satisfies the extended Sandbox interface. The scripted sandbox is
// used only for worker-layer tests that don't exercise the compile path — so
// this always returns exit 0 unless overridden by compileResult.
func (s *scriptedSandbox) Compile(_ context.Context, _ []string, _ func([]byte)) (runner.CompileResult, error) {
	return runner.CompileResult{ExitCode: 0}, nil
}

func (s *scriptedSandbox) cleanups() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupCount
}

// CPUReader satisfies the worker.DockerSandbox interface.
func (s *scriptedSandbox) CPUReader() session.CPUUsageFunc {
	return func(_ context.Context) (int, error) { return 0, nil }
}

// Limits satisfies the worker.DockerSandbox interface.
func (s *scriptedSandbox) Limits() wire.Limits { return wire.Limits{} }

// ─────────────────────────────────────────────────────────────────────────────
// scriptedRunner — fake runner.Runner
// ─────────────────────────────────────────────────────────────────────────────

type scriptedRunner struct {
	sandbox runner.Sandbox
	err     error
}

func (r *scriptedRunner) Create(_ context.Context, _ wire.JobSpec) (runner.Sandbox, error) {
	return r.sandbox, r.err
}

// ─────────────────────────────────────────────────────────────────────────────
// inMemoryControlTransport — implements worker.Transport without Redis
// ─────────────────────────────────────────────────────────────────────────────

type inMemoryControlTransport struct {
	mu            sync.Mutex
	stdinHandlers map[string][]func([]byte)
	ctrlHandlers  map[string][]func(wire.ControlMessage)
}

func newInMemoryControlTransport() *inMemoryControlTransport {
	return &inMemoryControlTransport{
		stdinHandlers: make(map[string][]func([]byte)),
		ctrlHandlers:  make(map[string][]func(wire.ControlMessage)),
	}
}

func (t *inMemoryControlTransport) Subscribe(_ context.Context, jobID string, handler func([]byte)) (stdintransport.Subscription, error) {
	t.mu.Lock()
	t.stdinHandlers[jobID] = append(t.stdinHandlers[jobID], handler)
	t.mu.Unlock()
	return &nopSub{}, nil
}

func (t *inMemoryControlTransport) SubscribeControl(_ context.Context, jobID string, handler func(wire.ControlMessage)) (stdintransport.Subscription, error) {
	t.mu.Lock()
	t.ctrlHandlers[jobID] = append(t.ctrlHandlers[jobID], handler)
	t.mu.Unlock()
	return &nopSub{}, nil
}

// Publish delivers a raw stdin chunk (test helper).
func (t *inMemoryControlTransport) Publish(_ context.Context, jobID string, chunk []byte) error {
	t.mu.Lock()
	handlers := make([]func([]byte), len(t.stdinHandlers[jobID]))
	copy(handlers, t.stdinHandlers[jobID])
	t.mu.Unlock()
	for _, h := range handlers {
		h(chunk)
	}
	return nil
}

// PublishControl delivers a ControlMessage (test helper).
func (t *inMemoryControlTransport) PublishControl(_ context.Context, jobID string, msg wire.ControlMessage) {
	t.mu.Lock()
	handlers := make([]func(wire.ControlMessage), len(t.ctrlHandlers[jobID]))
	copy(handlers, t.ctrlHandlers[jobID])
	t.mu.Unlock()
	for _, h := range handlers {
		h(msg)
	}
}

type nopSub struct{}

func (n *nopSub) Close() error { return nil }

// intPtr is a test helper.
func intPtr(v int) *int { return &v }

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestWorker_StartHandshakePublishesRunning verifies that after "start" arrives,
// the worker publishes "queued" then "running" stage events (SESS-01).
func TestWorker_StartHandshakePublishesRunning(t *testing.T) {
	sb := newScriptedSandbox()
	sb.waitDelay = 5 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	pub, events := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	jobID := "test-handshake-001"
	spec := testSpec(jobID)

	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 500, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, jobID, wire.ControlMessage{Type: wire.ControlTypeStart})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job handler did not finish within 3s")
	}

	phases := events.stagePhases()
	require.GreaterOrEqual(t, len(phases), 2, "must have at least queued + running stage events")
	assert.Equal(t, wire.StagePhaseQueued, phases[0], "first stage must be queued")
	assert.Equal(t, wire.StagePhaseRunning, phases[1], "second stage must be running")
}

// TestWorker_WarmupTimeoutNoStart verifies that if "start" never arrives within
// WarmupMs, the slot is reclaimed without publishing "running" (SESS-03).
func TestWorker_WarmupTimeoutNoStart(t *testing.T) {
	sb := newScriptedSandbox()
	sb.waitDelay = 10 * time.Second // Would block forever without warmup
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	pub, events := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	jobID := "test-warmup-001"
	spec := testSpec(jobID)

	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 100, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	// Do NOT send "start" — let the warmup timer fire.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job handler did not finish within 3s after warmup timeout")
	}

	phases := events.stagePhases()
	for _, p := range phases {
		if p == wire.StagePhaseRunning {
			t.Errorf("SESS-03: must not publish running stage when warmup expires, got phases: %v", phases)
		}
	}

	re := events.resultEvent()
	assert.NotNil(t, re, "must publish a result event on warmup teardown")
}

// TestWorker_KillBeforeStart verifies that a "kill" before "start" tears down
// without publishing "running".
func TestWorker_KillBeforeStart(t *testing.T) {
	sb := newScriptedSandbox()
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	pub, events := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	jobID := "test-kill-before-start-001"
	spec := testSpec(jobID)

	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 5000, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, jobID, wire.ControlMessage{Type: wire.ControlTypeKill})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job handler did not finish within 3s after kill")
	}

	for _, p := range events.stagePhases() {
		if p == wire.StagePhaseRunning {
			t.Errorf("must not publish running stage when killed before start")
		}
	}
}

// TestWorker_StdinChunkReachesSandbox verifies that a stdin chunk published via
// the transport reaches the fake sandbox's stdin pipe (WRK-02).
func TestWorker_StdinChunkReachesSandbox(t *testing.T) {
	sb := newScriptedSandbox()
	sb.stdout = bytes.NewReader([]byte("output\n"))
	sb.waitDelay = 80 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	// Capture stdin writes by installing a tee-writer.
	pr, pw := io.Pipe()
	var stdinBuf bytes.Buffer
	var stdinMu sync.Mutex
	sb.stdinImpl = pw
	sb.stdinPR = pr

	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				stdinMu.Lock()
				stdinBuf.Write(buf[:n])
				stdinMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	pub, _ := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	jobID := "test-stdin-chunk-001"
	spec := testSpec(jobID)

	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 500, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, jobID, wire.ControlMessage{Type: wire.ControlTypeStart})

	time.Sleep(30 * time.Millisecond)
	inMem.Publish(ctx, jobID, []byte("hello stdin\n")) //nolint:errcheck

	time.Sleep(30 * time.Millisecond)
	inMem.PublishControl(ctx, jobID, wire.ControlMessage{Type: wire.ControlTypeStdinClose})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job handler did not finish within 3s")
	}

	select {
	case <-stdinDone:
	case <-time.After(time.Second):
	}

	stdinMu.Lock()
	got := stdinBuf.String()
	stdinMu.Unlock()

	assert.Contains(t, got, "hello stdin\n",
		"stdin chunk must reach the sandbox stdin pipe (WRK-02)")
}

// TestWorker_StdinClosedExactlyOnce verifies that stdin_close delivers EOF
// exactly once even if stdin_close is sent multiple times (STDIN-02).
func TestWorker_StdinClosedExactlyOnce(t *testing.T) {
	closeCount := &atomic.Int32{}

	// Build a pipe and wrap the writer with a counting closer.
	pr, pw := io.Pipe()
	countingWriter := &countingWriteCloser{inner: pw, closes: closeCount}

	sb := newScriptedSandbox()
	sb.stdinImpl = countingWriter
	sb.stdinPR = pr
	sb.waitDelay = 50 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	// Drain the pipe so writes don't block.
	go io.Copy(io.Discard, pr) //nolint:errcheck

	pub, _ := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	jobID := "test-stdin-close-once-001"
	spec := testSpec(jobID)

	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 500, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, jobID, wire.ControlMessage{Type: wire.ControlTypeStart})

	time.Sleep(20 * time.Millisecond)
	// Send stdin_close THREE times — must only close once.
	for i := 0; i < 3; i++ {
		inMem.PublishControl(ctx, jobID, wire.ControlMessage{Type: wire.ControlTypeStdinClose})
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job handler did not finish")
	}

	assert.LessOrEqual(t, int(closeCount.Load()), 1,
		"stdin must be closed at most once regardless of multiple stdin_close messages (STDIN-02)")
}

// TestWorker_ResultPublishedOnce verifies that the terminal result event is
// published exactly once on clean exit.
func TestWorker_ResultPublishedOnce(t *testing.T) {
	sb := newScriptedSandbox()
	sb.stdout = bytes.NewReader([]byte("done\n"))
	sb.waitDelay = 10 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	pub, events := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	jobID := "test-result-once-001"
	spec := testSpec(jobID)

	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 500, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, jobID, wire.ControlMessage{Type: wire.ControlTypeStart})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job handler did not finish")
	}

	resultCount := 0
	for _, ev := range events.all() {
		if ev.event == "result" {
			resultCount++
		}
	}
	assert.Equal(t, 1, resultCount, "result event must be published exactly once")

	re := events.resultEvent()
	require.NotNil(t, re)
	assert.NotNil(t, re.ExitCode)
	assert.Equal(t, 0, *re.ExitCode)
}

// TestWorker_BatchJobNoStdin verifies that a batch (no-stdin) job completes as
// the degenerate case — start, no stdin, clean exit (SESS-02).
func TestWorker_BatchJobNoStdin(t *testing.T) {
	sb := newScriptedSandbox()
	sb.stdout = bytes.NewReader([]byte("batch output\n"))
	sb.waitDelay = 10 * time.Millisecond
	sb.waitResult = runner.Result{ExitCode: intPtr(0)}

	pub, events := newFakePublisher(t)
	inMem := newInMemoryControlTransport()

	jobID := "test-batch-001"
	spec := testSpec(jobID)
	spec.Interactive = false

	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 500, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{sandbox: sb}, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.HandleJobForTest(ctx, spec)
	}()

	time.Sleep(50 * time.Millisecond)
	inMem.PublishControl(ctx, jobID, wire.ControlMessage{Type: wire.ControlTypeStart})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("batch job did not finish within 3s")
	}

	re := events.resultEvent()
	require.NotNil(t, re, "must publish result event for batch job")
	assert.NotNil(t, re.ExitCode)
	assert.Equal(t, 0, *re.ExitCode)
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func testSpec(jobID string) wire.JobSpec {
	return wire.JobSpec{
		JobId:    jobID,
		Language: "test",
		Version:  "1.0",
		Channel:  "private-run-" + jobID,
		Limits: wire.Limits{
			WallTimeMs: 5000,
			IdleMs:     300,
			CpuMs:      5000,
			MemoryMb:   64,
			Pids:       32,
			OutputKb:   512,
		},
	}
}

// countingWriteCloser tracks Close calls for STDIN-02 testing.
type countingWriteCloser struct {
	inner  io.WriteCloser
	closes *atomic.Int32
}

func (c *countingWriteCloser) Write(p []byte) (int, error) {
	return c.inner.Write(p)
}

func (c *countingWriteCloser) Close() error {
	c.closes.Add(1)
	return c.inner.Close()
}
