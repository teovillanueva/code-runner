// Package worker implements the job execution run loop. The worker claims jobs
// from Redis, creates hardened sandboxes, parks at the start-handshake gate
// until a "start" control message arrives (or the warm-up timeout expires),
// then runs the session via session.RunInteractive — routing Redis stdin chunks
// into the sandbox stdin pipe and publishing stdout/stderr/result to soketi.
//
// Trust boundary: the worker talks ONLY to Redis (job queue + stdin/ctrl
// pub/sub) and soketi (output events). It makes NO HTTP calls to the API
// (WRK-04). This is asserted by the verification step (grep gate in PLAN.md).
//
// 02-04 pump/pipe race fix: the session owns the sandbox output pipes (Stdout /
// Stderr). The worker ONLY writes to stdin — it NEVER reads Stdout() or
// Stderr() directly. This is enforced by RunInteractive which sets up the pumps
// internally.
package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/teovillanueva/code-runner/internal/artifactstore"
	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/logging"
	"github.com/teovillanueva/code-runner/internal/publisher"
	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/session"
	"github.com/teovillanueva/code-runner/internal/stdintransport"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// instrumentationName is the tracer/meter scope name for worker telemetry.
// The cross-language trace is keyed only by trace_id (shared via the linked
// API span), not by scope; this name simply groups worker-emitted spans/metrics.
const instrumentationName = "code-runner-worker"

// tracer returns the worker tracer from the CURRENT global TracerProvider.
// It is resolved per-job (not cached at package init) so that a TracerProvider
// installed after init — e.g. otelinit.Init at boot, or a tracetest recorder in
// tests — is honoured. When OTEL is unconfigured the global is the SDK no-op
// (zero cost, no exporter).
func tracer() trace.Tracer { return otel.Tracer(instrumentationName) }

// terminalCounter / queueTime construct the worker instruments from the CURRENT
// global MeterProvider on each call. Resolving lazily (rather than caching an
// instrument bound to the no-op meter at package init) ensures a MeterProvider
// installed after init routes the measurements correctly. Construction is cheap
// and the no-op provider returns no-op instruments.
//
// terminalCounter (D-07): ONE counter for all terminal outcomes, distinguished
// only by the low-cardinality terminal_state attribute. job_id is NEVER a metric
// attribute (RESEARCH anti-pattern: high-cardinality; it belongs on spans/logs).
//
// queueTime (OBS-06): seconds a job waited in the queue before being claimed
// (semconv unit "s") — recorded once per claim.
func terminalCounter() metric.Int64Counter {
	c, _ := otel.Meter(instrumentationName).Int64Counter(
		"code_runner.jobs.terminal",
		metric.WithUnit("{job}"),
		metric.WithDescription("Count of jobs reaching a terminal state, by terminal_state."),
	)
	return c
}

func queueTimeHist() metric.Float64Histogram {
	h, _ := otel.Meter(instrumentationName).Float64Histogram(
		"code_runner.queue.time",
		metric.WithUnit("s"),
		metric.WithDescription("Time a job waited in the queue before being claimed, in seconds."),
	)
	return h
}

// Terminal-state attribute values (D-07 low-cardinality set). These are the
// ONLY values ever placed on terminal_state — never raw error strings.
const (
	terminalDone         = "done"
	terminalKilled       = "killed"
	terminalIdleTimedOut = "idle_timed_out"
	terminalTimedOut     = "timed_out"
	terminalError        = "error"
)

// extractLinkedSpanContext derives the SpanContext to LINK the worker root span
// to, from the (untrusted) traceparent/tracestate carried on the JobSpec across
// the Redis seam. It fails closed: nil or malformed input yields an invalid
// SpanContext (a fresh trace, no link) and never panics (threat T-08-03 /
// RESEARCH Security V5). This is the production replacement for the 08-01
// test-only extract helper.
func extractLinkedSpanContext(spec wire.JobSpec) trace.SpanContext {
	carrier := propagation.MapCarrier{}
	if spec.Traceparent != nil {
		carrier["traceparent"] = *spec.Traceparent
	}
	if spec.Tracestate != nil {
		carrier["tracestate"] = *spec.Tracestate
	}
	parentCtx := propagation.TraceContext{}.Extract(context.Background(), carrier)
	return trace.SpanContextFromContext(parentCtx)
}

// recordTerminal increments the terminal-state counter with the low-cardinality
// terminal_state attribute. The counter carries no job_id.
func recordTerminal(ctx context.Context, state string) {
	terminalCounter().Add(ctx, 1, metric.WithAttributes(attribute.String("terminal_state", state)))
}

// terminalStateFor maps a runner.Result + wire.JobState to the low-cardinality
// terminal_state attribute value (D-07).
func terminalStateFor(result runner.Result, state wire.JobState) string {
	switch {
	case result.IdleTimedOut:
		return terminalIdleTimedOut
	case result.TimedOut:
		return terminalTimedOut
	case state == wire.JobStateKilled:
		return terminalKilled
	case state == wire.JobStateError:
		return terminalError
	case state == wire.JobStateDone:
		return terminalDone
	default:
		return terminalError
	}
}

// DockerSandbox extends runner.Sandbox with the accessors that the
// DockerSocketRunner provides but are not on the interface. The worker uses
// a type assertion so it can get CPUReader/Limits without importing the Docker
// SDK in this package.
//
// CPUReader returns runner.CPUUsageFunc (a type alias for the raw func type)
// which is the same underlying type as session.CPUUsageFunc. Using the alias
// here ensures the type assertion sb.(DockerSandbox) succeeds at runtime, since
// *dockerSandbox.CPUReader() returns the alias type — not the named type.
type DockerSandbox interface {
	runner.Sandbox
	CPUReader() runner.CPUUsageFunc
	Limits() wire.Limits
	// ReadArtifacts reads new regular files from the sandbox /workspace before
	// Cleanup() destroys the volume (D-06/D-07). exclude keys are basenames not
	// to return (input files + ".compile_ready" + the compile-output binary).
	ReadArtifacts(ctx context.Context, exclude map[string]bool) ([]runner.CapturedArtifact, error)
}

// Transport is the interface the worker requires for stdin/ctrl pub-sub.
// *stdintransport.RedisTransport satisfies this interface.
// The interface enables unit tests to inject an in-memory fake without a
// live Redis instance (Rule 2 — testability is a correctness requirement).
type Transport interface {
	// Subscribe registers handler to receive raw stdin chunks for jobID.
	Subscribe(ctx context.Context, jobID string, handler func(chunk []byte)) (stdintransport.Subscription, error)
	// SubscribeControl registers handler to receive ControlMessages for jobID.
	SubscribeControl(ctx context.Context, jobID string, handler func(wire.ControlMessage)) (stdintransport.Subscription, error)
}

// Config holds the worker configuration values needed for the run loop.
type Config struct {
	// MaxSandboxes is the maximum number of concurrently live sandboxes.
	MaxSandboxes int
	// WarmupMs is the maximum time (ms) to wait for a "start" control message
	// before the slot is reclaimed (SESS-03).
	WarmupMs int
	// ClaimTimeout is how long BRPOP blocks waiting for a new job.
	// Defaults to 5s if zero.
	ClaimTimeout time.Duration
	// HeartbeatIntervalMs is how often the worker writes its heartbeat key to
	// Redis (in milliseconds).  Defaults to 5000 if zero.
	HeartbeatIntervalMs int
	// HeartbeatTTLMs is the TTL (in milliseconds) applied to the heartbeat key
	// each time it is written.  Defaults to 20000 if zero.
	HeartbeatTTLMs int

	// Artifacts is the (possibly nil) artifact store. A nil value means artifact
	// capture is DISABLED (D-04): output pull still works, collected jobs just
	// return zero artifacts. The teardown path in plan 09-04 reads this via
	// w.cfg.Artifacts. It rides on Config (NOT a New/NewWithTransport param) so
	// the constructor signatures stay byte-stable.
	Artifacts artifactstore.ArtifactStore

	// RunResultTTL is the Redis TTL applied to the persisted RunResult key.
	// Defaults to 600s if zero. Plan 09-04 reads w.cfg.RunResultTTL in teardown.
	RunResultTTL time.Duration

	// ── Content-addressed blob store (Phase 16, BLOB-06/09) ──────────────────
	// BlobStore + BlobIndex are nil when CAS is unconfigured: a job that
	// references a blob then fails cleanly (errBlobVerify), while inline-only jobs
	// are unaffected. They ride on Config (not a New param) so the constructor
	// signatures stay byte-stable, exactly like Artifacts.
	BlobStore BlobStore
	BlobIndex BlobIndex

	// BlobIdleTTL is the idle liveness TTL the worker extends (monotonically) each
	// time it leases/touches a blob. Defaults to 24h if zero.
	BlobIdleTTL time.Duration
}

// Worker is the job execution run loop. It claims jobs from the jobstore, runs
// them through the session layer, and publishes events to soketi.
type Worker struct {
	store     *jobstore.Store
	transport Transport
	runner    runner.Runner
	pub       *publisher.Publisher
	cfg       Config
	slots     chan struct{} // semaphore: len(slots) = available capacity

	// workerID is the ephemeral random identity for this worker instance.
	// Generated once at construction via newWorkerID().
	workerID string

	// heartbeatInterval is how often the heartbeat key is refreshed.
	heartbeatInterval time.Duration

	// heartbeatTTL is the TTL applied to the heartbeat key on each write.
	heartbeatTTL time.Duration

	// ── Graceful drain (scale-down / deploy safety) ──────────────────────────
	// In-flight job goroutines run under jobsCtx + are tracked by wg, NOT under
	// the claim context passed to Run. So when a SIGTERM cancels the claim
	// context (Fly stops the Machine on scale-down or deploy), the claim loop
	// stops taking NEW jobs but active sandboxes keep running. main then calls
	// Drain to wait for them to finish before the process exits — instead of
	// killing a student mid-session. The heartbeat also runs under jobsCtx so the
	// node stays "live" during the drain (its sandboxes are never seen as
	// orphans by another worker's reaper). jobsCancel is the hard stop after the
	// drain deadline.
	wg         sync.WaitGroup
	jobsCtx    context.Context
	jobsCancel context.CancelFunc
}

// New creates a Worker using the concrete *stdintransport.RedisTransport.
// This is the production constructor used by apps/worker/main.go.
func New(
	store *jobstore.Store,
	transport *stdintransport.RedisTransport,
	r runner.Runner,
	pub *publisher.Publisher,
	cfg Config,
) *Worker {
	return NewWithTransport(store, transport, r, pub, cfg)
}

// NewWithTransport creates a Worker using the Transport interface.
// Use this in tests to inject an in-memory transport fake.
func NewWithTransport(
	store *jobstore.Store,
	transport Transport,
	r runner.Runner,
	pub *publisher.Publisher,
	cfg Config,
) *Worker {
	if cfg.MaxSandboxes <= 0 {
		cfg.MaxSandboxes = 8
	}
	if cfg.WarmupMs <= 0 {
		cfg.WarmupMs = 30000
	}
	if cfg.ClaimTimeout <= 0 {
		cfg.ClaimTimeout = 5 * time.Second
	}
	if cfg.HeartbeatIntervalMs <= 0 {
		cfg.HeartbeatIntervalMs = 5000
	}
	if cfg.HeartbeatTTLMs <= 0 {
		cfg.HeartbeatTTLMs = 20000
	}
	if cfg.RunResultTTL <= 0 {
		cfg.RunResultTTL = 600 * time.Second
	}
	if cfg.BlobIdleTTL <= 0 {
		cfg.BlobIdleTTL = 24 * time.Hour
	}
	slots := make(chan struct{}, cfg.MaxSandboxes)
	for i := 0; i < cfg.MaxSandboxes; i++ {
		slots <- struct{}{}
	}
	jobsCtx, jobsCancel := context.WithCancel(context.Background())
	return &Worker{
		store:             store,
		transport:         transport,
		runner:            r,
		pub:               pub,
		cfg:               cfg,
		slots:             slots,
		workerID:          newWorkerID(),
		heartbeatInterval: time.Duration(cfg.HeartbeatIntervalMs) * time.Millisecond,
		heartbeatTTL:      time.Duration(cfg.HeartbeatTTLMs) * time.Millisecond,
		jobsCtx:           jobsCtx,
		jobsCancel:        jobsCancel,
	}
}

// WorkerIDForTest returns the worker's ephemeral identity.  This is a
// test-only accessor; production code should not depend on it.
func (w *Worker) WorkerIDForTest() string {
	return w.workerID
}

// gaugeCallbackTimeout bounds the Redis LLEN issued from inside the queue-depth
// observable-gauge callback. The callback runs on the metric export interval; it
// must never block the export cycle on a slow/unreachable Redis (RESEARCH
// Pitfall 5). On timeout/error the callback SKIPS the observation rather than
// forcing a stale zero.
const gaugeCallbackTimeout = 250 * time.Millisecond

// RegisterMetrics registers the worker's OBSERVABLE GAUGES (OBS-06) against the
// CURRENT global MeterProvider:
//
//   - code_runner.queue.depth (Int64ObservableGauge, unit "{job}") — observes
//     LLEN jobs:queue via store.QueueDepth with a short-timeout ctx; on Redis
//     error the observation is SKIPPED (no stale/forced zero — Pitfall 5).
//   - code_runner.slots.used  (Int64ObservableGauge, unit "{slot}") — observes
//     used capacity from the IN-MEMORY semaphore (MaxSandboxes − len(slots)); no
//     Redis call, so it cannot be affected by a Redis outage.
//   - code_runner.slots.max   (Int64ObservableGauge, unit "{slot}") — observes
//     the configured capacity ceiling so dashboards can plot used/max.
//
// Both gauges carry NO attributes (low-cardinality by construction; no job_id).
//
// It returns a deregister func (the MeterProvider Registration's Unregister) so
// callers/tests can detach the callback; a nil store yields a queue-depth gauge
// that always skips (used/max still report). It returns an error only if the
// instruments cannot be constructed.
func (w *Worker) RegisterMetrics() (func() error, error) {
	meter := otel.Meter(instrumentationName)

	queueDepth, err := meter.Int64ObservableGauge(
		"code_runner.queue.depth",
		metric.WithUnit("{job}"),
		metric.WithDescription("Current depth of the job queue (LLEN jobs:queue)."),
	)
	if err != nil {
		return nil, err
	}
	slotsUsed, err := meter.Int64ObservableGauge(
		"code_runner.slots.used",
		metric.WithUnit("{slot}"),
		metric.WithDescription("Sandbox slots currently in use (in-memory semaphore)."),
	)
	if err != nil {
		return nil, err
	}
	slotsMax, err := meter.Int64ObservableGauge(
		"code_runner.slots.max",
		metric.WithUnit("{slot}"),
		metric.WithDescription("Configured maximum concurrent sandbox slots."),
	)
	if err != nil {
		return nil, err
	}

	reg, err := meter.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			// Slots come from the in-memory semaphore — no Redis, never skipped.
			used := int64(w.cfg.MaxSandboxes - len(w.slots))
			if used < 0 {
				used = 0
			}
			o.ObserveInt64(slotsUsed, used)
			o.ObserveInt64(slotsMax, int64(w.cfg.MaxSandboxes))

			// Queue depth reads Redis under a short-timeout ctx; SKIP on error
			// (no stale/forced-zero observation — Pitfall 5).
			if w.store != nil {
				cctx, cancel := context.WithTimeout(ctx, gaugeCallbackTimeout)
				defer cancel()
				if depth, qErr := w.store.QueueDepth(cctx); qErr == nil {
					o.ObserveInt64(queueDepth, depth)
				}
			}
			return nil
		},
		queueDepth, slotsUsed, slotsMax,
	)
	if err != nil {
		return nil, err
	}
	return reg.Unregister, nil
}

// Run is the main event loop. It blocks until ctx is cancelled. On each
// iteration it acquires a slot, claims a job from the queue (BRPOP), and
// handles the job in a goroutine. The slot is released inside the single
// sync.Once teardown in runJobFromSpec on every terminal path.
func (w *Worker) Run(ctx context.Context) {
	// Start heartbeat goroutine only when we have a real store to write to.
	// It runs under jobsCtx (NOT the claim ctx) so the node stays "live" while
	// in-flight jobs drain after a SIGTERM — otherwise its draining sandboxes
	// would look orphaned to other workers' reapers once the heartbeat lapsed.
	if w.store != nil {
		w.startHeartbeat(w.jobsCtx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Acquire a slot (blocks until one is available or ctx is done).
		select {
		case <-ctx.Done():
			return
		case <-w.slots:
		}

		// Claim a job from the queue. Release slot + continue on timeout.
		jobID, err := w.store.Claim(ctx, w.cfg.ClaimTimeout)
		if err != nil {
			w.slots <- struct{}{} // release slot back
			if errors.Is(err, jobstore.ErrTimeout) {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			slog.Error("worker: Claim error", "err", err)
			continue
		}

		// Handle the job in a goroutine under jobsCtx (NOT the claim ctx) and
		// track it in wg, so a SIGTERM stops claiming but lets this sandbox run
		// to completion during Drain. The slot is released inside teardown (or on
		// early-return paths) inside runJobFromSpec — NOT here.
		w.wg.Add(1)
		go func(id string) {
			defer w.wg.Done()
			w.runJob(w.jobsCtx, id, w.releaseSlot)
		}(jobID)
	}
}

// Drain waits for in-flight jobs to finish after the claim loop has stopped
// (i.e. after Run returns because a SIGTERM cancelled its context). It gives
// active sandboxes up to `timeout` to complete on their own — so a Fly
// scale-down or deploy does not kill a student mid-session — then force-cancels
// any stragglers and stops the heartbeat. Fly's kill_timeout (fly.toml) must be
// ≥ this timeout, or Fly SIGKILLs the Machine before the drain completes.
func (w *Worker) Drain(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("worker: in-flight jobs drained cleanly")
	case <-time.After(timeout):
		slog.Warn("worker: drain deadline exceeded — cancelling remaining in-flight jobs", "timeout", timeout)
		w.jobsCancel()
		w.wg.Wait()
	}
	w.jobsCancel() // stop the heartbeat goroutine; release jobsCtx
}

// HandleJobForTest is a test-only entry point that runs a job from a fully
// resolved spec without going through the claim loop. It is called by
// worker_test.go to drive individual job scenarios without Redis.
// No slot is acquired, so a no-op release function is passed.
func (w *Worker) HandleJobForTest(ctx context.Context, spec wire.JobSpec) {
	w.runJobFromSpec(ctx, spec, func() {}) // no-op slot release — no slot was acquired
}

// runJob reads the spec from Redis and delegates to runJobFromSpec.
// releaseSlot is called exactly once inside runJobFromSpec on every terminal path.
func (w *Worker) runJob(ctx context.Context, jobID string, releaseSlot func()) {
	spec, err := w.store.ReadSpec(ctx, jobID)
	if err != nil {
		slog.Error("worker: ReadSpec failed", "jobID", jobID, "err", err)
		releaseSlot() // release immediately — we never entered runJobFromSpec
		return
	}
	w.runJobFromSpec(ctx, spec, releaseSlot)
}

// runJobFromSpec is the per-job handler. It subscribes to stdin/ctrl, creates
// the sandbox, parks at the start-handshake gate, then runs the session.
//
// releaseSlot is called exactly once on every terminal path — either in the
// single sync.Once teardown (after Create succeeds) or on early-return paths
// before teardown is defined (Subscribe/SubscribeControl/Create failure).
func (w *Worker) runJobFromSpec(ctx context.Context, spec wire.JobSpec, releaseSlot func()) {
	jobID := spec.JobId
	log := slog.With("jobID", jobID)

	// collectOutput gates ALL output-accumulation + artifact-capture work (D-08):
	// the Sinks closures append output, and teardown reads/uploads artifacts and
	// persists the RunResult only when this is true. spec.CollectOutput is a
	// nil-tolerant *bool (the API always writes an explicit boolean, but the
	// worker must not panic on a nil from an older enqueue).
	collectOutput := spec.CollectOutput != nil && *spec.CollectOutput

	// Output accumulation buffers (D-08): when collectOutput is set, the Sinks
	// closures append the same within-budget bytes that are streamed to soketi —
	// the session pump only forwards bytes inside the outputKb budget, so this
	// reuses ONE truncation semantics (no second cap). The persisted RunResult's
	// stdout/stderr therefore equal the soketi stream. A mutex guards concurrent
	// appends from the stdout + stderr pump goroutines.
	var (
		outputMu  sync.Mutex
		stdoutBuf bytes.Buffer
		stderrBuf bytes.Buffer
	)

	// Compile-stage result (Piston-style separate `compile` block). Set once in
	// the compile section below (for compiled languages); nil for interpreted
	// languages or when no compile step ran. Threaded into the persisted
	// RunResult by teardown. Written before the run stage starts and read in
	// teardown afterward, so no synchronization is needed.
	var compileBlock *wire.CompileResult

	// Artifact capture stash (R4/R5/D-07): the session supervisor's terminate()
	// removes the container (Kill+Cleanup) on EVERY terminal path, so the read
	// MUST happen inside the session's BeforeCleanup hook — while the container
	// still exists — NOT in the worker teardown below (which runs after
	// RunInteractive returns, when the container is already gone). The hook
	// stashes the captured files here; the teardown block uploads + persists them.
	var (
		capturedMu        sync.Mutex
		capturedArtifacts []runner.CapturedArtifact
	)

	// ── Trace: extract the API-injected traceparent and start the root span. ──
	// The worker LINKS (not parents) the API's execute span because /v1/execute
	// returns 202 before the run starts (D-13). The link shares the API trace_id;
	// nil/malformed traceparent fails closed to a fresh trace (threat T-08-03).
	linkedSC := extractLinkedSpanContext(spec)
	ctx = logging.WithJobID(ctx, jobID) // job_id rides on logs/spans (never metrics)
	ctx, root := tracer().Start(ctx, "claim",
		trace.WithLinks(trace.Link{SpanContext: linkedSC}))
	defer root.End()

	// Time-in-queue (OBS-06): how long the job waited before this claim, in
	// seconds. enqueuedAtMs is set by the API at enqueue time.
	if spec.EnqueuedAtMs > 0 {
		waitedMs := time.Now().UnixMilli() - int64(spec.EnqueuedAtMs)
		if waitedMs < 0 {
			waitedMs = 0
		}
		queueTimeHist().Record(ctx, float64(waitedMs)/1000.0)
	}

	// 1. Subscribe stdin:<id> and ctrl:<id> FIRST, before publishing "queued".
	//    This guarantees that the subscriptions are active before any external
	//    client can see the "queued" event and immediately send "start". If we
	//    published "queued" first and subscribed after, there is a race window
	//    where "start" is lost (PITFALLS §7 / start-handshake race).
	//    Handlers write to a buffered channel so they never block the delivery
	//    goroutine.
	stdinCh := make(chan []byte, 256)
	ctrlCh := make(chan wire.ControlMessage, 32)

	stdinSub, err := w.transport.Subscribe(ctx, jobID, func(chunk []byte) {
		// Copy chunk so the slice is safe after the handler returns.
		cp := make([]byte, len(chunk))
		copy(cp, chunk)
		select {
		case stdinCh <- cp:
		default:
			log.Warn("worker: stdin channel full, dropping chunk", "len", len(chunk))
		}
	})
	if err != nil {
		log.Error("worker: Subscribe stdin failed", "err", err)
		releaseSlot() // early return — slot was acquired but we can't proceed
		return
	}

	ctrlSub, err := w.transport.SubscribeControl(ctx, jobID, func(msg wire.ControlMessage) {
		select {
		case ctrlCh <- msg:
		default:
			log.Warn("worker: ctrl channel full, dropping message", "type", msg.Type)
		}
	})
	if err != nil {
		log.Error("worker: SubscribeControl failed", "err", err)
		stdinSub.Close() //nolint:errcheck
		releaseSlot()    // early return — slot was acquired but we can't proceed
		return
	}

	// 2. NOW publish "queued" stage + write queued status. Subscriptions are
	//    active, so any "start" that arrives immediately after the client sees
	//    "queued" will be delivered to ctrlCh (no race window).
	if err := w.pub.Stage(jobID, wire.StagePhaseQueued); err != nil {
		log.Warn("worker: publish queued stage failed", "err", err)
	}
	if w.store != nil {
		if err := w.store.WriteStatus(ctx, wire.JobStatus{
			JobId:    jobID,
			Language: spec.Language,
			Version:  spec.Version,
			Channel:  spec.Channel,
			State:    wire.JobStateQueued,
		}); err != nil {
			log.Warn("worker: WriteStatus queued failed", "err", err)
		}
	}

	// 2c. Resolve content-addressed blob refs → ordinary inline workspace files
	//     (BLOB-06/09), BEFORE the runner is invoked, so BOTH the DockerSocketRunner
	//     and the zygote relay see refs as normal inputs. For each ref: lease the
	//     blob, stream it from OUR configured store while hashing, verify sha256 ==
	//     ref (authoritative tamper gate), and inline the verified bytes as base64.
	//     A store-miss / mismatch / unconfigured-store fails the job cleanly here —
	//     NO sandbox is created, NO partial run, NO leak. Every leased hash is
	//     released on EVERY terminal path (the resolve-failure path here releases
	//     immediately; the success path releases inside the once-only teardown).
	// resolveBlobRefs emits its OWN "blobs.resolve" span ONLY when the job
	// actually references a blob — an inline-only job (the common case) produces
	// no extra span, preserving the exact phase-span set (OBS-03).
	resolvedSpec, leasedBlobs, blobErr := w.resolveBlobRefs(ctx, spec)
	if blobErr != nil {
		log.Error("worker: blob ref resolution failed", "err", blobErr)
		w.releaseLeases(ctx, leasedBlobs, jobID) // release any partial leases now
		stdinSub.Close()                         //nolint:errcheck
		ctrlSub.Close()                          //nolint:errcheck
		w.publishError(ctx, jobID, spec)
		recordTerminal(ctx, terminalError)
		releaseSlot() // early return — no sandbox was created
		return
	}
	// From here on the runner sees the RESOLVED spec (refs → inline files).
	spec = resolvedSpec

	// 3. Create the sandbox. The container is started by Create (docker.go calls
	//    ContainerStart inside Create) but the process reads from stdin so it will
	//    block waiting for input. The start-handshake below gates when we begin
	//    the session clocks.
	//
	//    sandbox.create is a REAL child span: we start it here and pass its ctx
	//    into Create, so docker.go's ContainerCreate/ContainerStart execute WITHIN
	//    this span. (The create-latency histogram is wired separately in 08-04
	//    inside the same Create function — span here, histogram there, no overlap.)
	// Record ownership BEFORE creating the container (reaper-race fix). The
	// reaper marks a container orphaned if its jobID is in NO live worker's
	// owned-jobs set. If we created the container first, a reaper sweep landing
	// in the window before AddOwnedJob would force-remove a perfectly healthy
	// sandbox — observed as ~6% spurious failures under a burst with concurrent
	// scale-up. Adding ownership first closes the window: our heartbeat is
	// already live (startHeartbeat writes the first beat synchronously before the
	// claim loop), so the instant the container exists its jobID is already owned
	// by a live worker. On Create failure we roll the membership back below.
	if w.store != nil {
		if err := w.store.AddOwnedJob(ctx, w.workerID, jobID); err != nil {
			log.Warn("worker: AddOwnedJob failed", "err", err)
		}
	}

	createCtx, createSpan := tracer().Start(ctx, "sandbox.create")
	sb, err := w.runner.Create(createCtx, spec)
	createSpan.End()
	if err != nil {
		log.Error("worker: Create sandbox failed", "err", err)
		// Roll back the ownership recorded above — no container was created.
		if w.store != nil {
			if rmErr := w.store.RemoveOwnedJob(ctx, w.workerID, jobID); rmErr != nil {
				log.Warn("worker: RemoveOwnedJob failed", "err", rmErr)
			}
		}
		stdinSub.Close() //nolint:errcheck
		ctrlSub.Close()  //nolint:errcheck
		// Release any blob leases taken during resolution — the run never started.
		w.releaseLeases(ctx, leasedBlobs, jobID)
		w.publishError(ctx, jobID, spec)
		recordTerminal(ctx, terminalError)
		releaseSlot() // early return — sandbox never occupied a slot
		return
	}

	// Single sync.Once teardown — called on every terminal path after Create.
	// It releases the slot, removes the job from the owned-jobs set, cleans up
	// the sandbox, publishes the result event, and writes the terminal status.
	var teardownOnce sync.Once
	teardown := func(result runner.Result, state wire.JobState) {
		teardownOnce.Do(func() {
			// Terminal-state counter (D-07): exactly once per job, on every
			// terminal path after Create. Low-cardinality terminal_state attr only.
			recordTerminal(ctx, terminalStateFor(result, state))

			// Close subscriptions first to stop delivery goroutines.
			stdinSub.Close() //nolint:errcheck
			ctrlSub.Close()  //nolint:errcheck

			// Remove from owned-jobs set (best-effort — log on error).
			if w.store != nil {
				if rmErr := w.store.RemoveOwnedJob(ctx, w.workerID, jobID); rmErr != nil {
					log.Warn("worker: RemoveOwnedJob failed", "err", rmErr)
				}
			}

			// Release the capacity slot — exactly once on every terminal path.
			releaseSlot()

			// Release every blob lease taken for this job (BLOB-09) — idempotent
			// (SREM), so a double terminal path is harmless. Done inside the
			// once-only teardown so GC can reclaim the blob once no run pins it.
			w.releaseLeases(ctx, leasedBlobs, jobID)

			// Publish result event (publish.result phase span).
			_, pubSpan := tracer().Start(ctx, "publish.result")
			ev := toResultEvent(result)
			if pubErr := w.pub.Result(jobID, ev); pubErr != nil {
				log.Warn("worker: publish Result failed", "err", pubErr)
			}
			pubSpan.End()

			// Write terminal status.
			if w.store != nil {
				if stErr := w.store.WriteStatus(ctx, wire.JobStatus{
					JobId:    jobID,
					Language: spec.Language,
					Version:  spec.Version,
					Channel:  spec.Channel,
					State:    state,
				}); stErr != nil {
					log.Warn("worker: WriteStatus terminal failed", "err", stErr)
				}
			}

			// ── Artifact capture + RunResult persist (R4/R5/R6/R8) ────────────
			// MUST run BEFORE sb.Cleanup() (D-07): Cleanup force-removes the
			// /workspace anonymous volume (RemoveVolumes=true), so reading after
			// would race a gone volume. All sub-steps are best-effort — a nil
			// store, a capture failure, or an upload error never fails the job
			// (the job keeps its real exitCode). Gated entirely by collectOutput.
			if collectOutput {
				outputMu.Lock()
				runResult := assembleRunResult(result, stdoutBuf.String(), stderrBuf.String())
				outputMu.Unlock()

				// Attach the compile-stage result (build logs kept separate from
				// the run stdout/stderr, mirroring Piston's `compile` object).
				runResult.Compile = compileBlock

				if _, ok := sb.(DockerSandbox); ok && w.cfg.Artifacts != nil {
					// Artifacts were read in the session BeforeCleanup hook above —
					// the container is already gone by now (terminate() killed it),
					// so we must NOT call ReadArtifacts here. Consume the stash.
					capturedMu.Lock()
					captured := capturedArtifacts
					capturedMu.Unlock()

					maxArtifacts := spec.Limits.MaxArtifacts
					maxArtifactBytes := spec.Limits.MaxArtifactBytes
					byteBudget := 0
					for _, a := range captured {
						// Caps (R5): keep the first N within both the file-count and
						// total-byte budgets; drop the rest and mark truncated.
						if maxArtifacts > 0 && len(runResult.Artifacts) >= maxArtifacts {
							runResult.ArtifactsTruncated = true
							break
						}
						if maxArtifactBytes > 0 && byteBudget+len(a.Data) > maxArtifactBytes {
							runResult.ArtifactsTruncated = true
							continue
						}

						url, putErr := w.cfg.Artifacts.Put(ctx, jobID, a.Name, a.MimeType, a.Data)
						if putErr != nil {
							log.Warn("worker: artifact upload failed", "name", a.Name, "err", putErr)
							continue
						}
						byteBudget += len(a.Data)
						art := wire.Artifact{
							Name:     a.Name,
							MimeType: a.MimeType,
							Bytes:    len(a.Data),
							Url:      url,
						}
						runResult.Artifacts = append(runResult.Artifacts, art)
						if pubErr := w.pub.Artifact(jobID, art); pubErr != nil {
							log.Warn("worker: publish Artifact failed", "name", a.Name, "err", pubErr)
						}
					}

					// OR in any transport-level truncation the sandbox itself
					// reported (the zygote relay drops an artifact whose frame would
					// exceed the relay payload cap and flags it). dockerSandbox does
					// not implement this accessor, so this is a no-op on the Docker
					// tier; ORed so a cap-loop truncation above is preserved.
					if at, ok := sb.(interface{ ArtifactsTruncated() bool }); ok && at.ArtifactsTruncated() {
						runResult.ArtifactsTruncated = true
					}
				}

				// Persist the RunResult with the env-configured TTL (R6/D-09).
				// A nil store or nil Artifacts still persists stdout/stderr +
				// zero artifacts (D-04).
				if w.store != nil {
					if wrErr := w.store.WriteRunResult(ctx, jobID, runResult, w.cfg.RunResultTTL); wrErr != nil {
						log.Warn("worker: WriteRunResult failed", "err", wrErr)
					}
				}
			}

			// Cleanup sandbox (idempotent — Cleanup already calls ContainerRemove).
			if cleanErr := sb.Cleanup(); cleanErr != nil {
				log.Warn("worker: sandbox Cleanup failed", "err", cleanErr)
			}
		})
	}

	// 4. PARK at the start-handshake gate: wait for "start" or fail fast on
	//    "kill" or warm-up timeout (SESS-01, SESS-03).
	//    handshake.wait span covers the time parked at the gate.
	_, handshakeSpan := tracer().Start(ctx, "handshake.wait")
	warmupTimer := time.NewTimer(time.Duration(w.cfg.WarmupMs) * time.Millisecond)
	defer warmupTimer.Stop()

	// Durable start (start-handshake race fix): /start may have been called while
	// this job was still queued — at that moment no worker was subscribed to
	// ctrl:<id>, so the fire-and-forget ctrl publish was lost. The API also
	// persists a durable start flag; check it now that we ARE subscribed (step 1).
	// This is race-free combined with the live ctrlCh below: a start that landed
	// BEFORE this read is caught here; one that lands AFTER is delivered live.
	started := false
	if w.store != nil {
		if s, sErr := w.store.WasStartRequested(ctx, jobID); sErr != nil {
			log.Warn("worker: WasStartRequested failed", "err", sErr)
		} else {
			started = s
		}
	}

	if !started {
	parkLoop:
		for {
			select {
			case <-ctx.Done():
				handshakeSpan.End()
				teardown(runner.Result{}, wire.JobStateError)
				return

			case <-warmupTimer.C:
				// Warm-up expired — reclaim slot, tear down (SESS-03).
				log.Info("worker: warmup timeout — no start received, tearing down")
				handshakeSpan.End()
				teardown(runner.Result{}, wire.JobStateError)
				return

			case msg := <-ctrlCh:
				switch msg.Type {
				case wire.ControlTypeStart:
					break parkLoop
				case wire.ControlTypeKill:
					handshakeSpan.End()
					teardown(runner.Result{}, wire.JobStateKilled)
					return
				default:
					// stdin_close before start — ignore.
					log.Warn("worker: received ctrl before start", "type", msg.Type)
				}
			}
		}
	}
	handshakeSpan.End()

	// 5a. Generic compile pre-step (manifest-argv-driven, no language branching).
	//     Runs only when spec.Compile is non-nil. Must execute BEFORE the
	//     StagePhaseRunning publish so the client sees: compiling → running.
	//     The compile step runs under the same wall/CPU/idle clocks and tree-kill
	//     as the run step: we call sb.Compile with the live session context (ctx),
	//     which is cancelled by Kill/Cleanup on clock expiry — a compile-bomb is
	//     tree-killed exactly like a run-bomb.
	if spec.Compile != nil {
		compileCtx, compileSpan := tracer().Start(ctx, "compile")
		if err := w.pub.Stage(jobID, wire.StagePhaseCompiling); err != nil {
			log.Warn("worker: publish compiling stage failed", "err", err)
		}

		compileResult, compileErr := sb.Compile(compileCtx, []string(*spec.Compile), func(b []byte) {
			// Live real-time build log on its OWN event (compile_output), kept
			// separate from the run stdout/stderr streams.
			if pubErr := w.pub.CompileOutput(jobID, string(b)); pubErr != nil {
				log.Warn("worker: publish compile output failed", "err", pubErr)
			}
		})

		// Infrastructure failure (Docker exec error, context cancelled): treat as
		// a non-zero exit so the client receives a correct failure.
		compileExitCode := compileResult.ExitCode
		if compileErr != nil && compileExitCode == 0 {
			compileExitCode = 1
		}

		// Build the Piston-style compile block (build logs kept separate from the
		// run stdout/stderr). Persisted into RunResult.compile by teardown on
		// BOTH the failure path here and the run-completion path below.
		ce := compileExitCode
		compileBlock = &wire.CompileResult{
			ExitCode:   &ce,
			Signal:     nil,
			Stdout:     compileResult.Stdout,
			Stderr:     compileResult.Stderr,
			Output:     compileResult.Output,
			DurationMs: compileResult.DurationMs,
		}

		// On compile failure (non-zero exit OR infrastructure error), publish a
		// terminal result and return — the run argv MUST NOT execute.
		if compileErr != nil || compileResult.ExitCode != 0 {
			if compileErr != nil {
				log.Warn("worker: compile step error", "err", compileErr)
			}
			// Build a Result with the compile exit code so the client receives
			// the correct non-zero exit in the terminal event.
			failResult := runner.Result{ExitCode: &compileExitCode, DurationMs: compileResult.DurationMs}
			compileSpan.End()
			teardown(failResult, wire.JobStateError)
			return
		}
		// Compile succeeded (exit 0) — fall through to StagePhaseRunning.
		compileSpan.End()
	}

	// 5b. On start (or after successful compile): publish "running" stage + write running status.
	if err := w.pub.Stage(jobID, wire.StagePhaseRunning); err != nil {
		log.Warn("worker: publish running stage failed", "err", err)
	}
	if w.store != nil {
		if err := w.store.WriteStatus(ctx, wire.JobStatus{
			JobId:    jobID,
			Language: spec.Language,
			Version:  spec.Version,
			Channel:  spec.Channel,
			State:    wire.JobStateRunning,
		}); err != nil {
			log.Warn("worker: WriteStatus running failed", "err", err)
		}
	}

	// 6. Derive CPUUsageFunc from the sandbox if it supports it.
	// Use runner.CPUUsageFunc (a type alias) to match *dockerSandbox.CPUReader()'s
	// return type; the alias is assignable to session.CPUUsageFunc because they
	// share the same underlying function signature.
	var cpuFn runner.CPUUsageFunc
	if ds, ok := sb.(DockerSandbox); ok {
		cpuFn = ds.CPUReader()
	} else {
		cpuFn = func(_ context.Context) (int, error) { return 0, nil }
	}

	// 7. stdin_close guard: sb.Stdin().Close() must only be called ONCE.
	var stdinCloseOnce sync.Once
	closeStdin := func() {
		stdinCloseOnce.Do(func() {
			if err := sb.Stdin().Close(); err != nil {
				log.Warn("worker: close stdin failed", "err", err)
			}
		})
	}

	// 8. Feed stdin chunks and handle kill/stdin_close in a goroutine while
	//    session.RunInteractive runs in this goroutine.
	sessionDone := make(chan struct{})

	// stdinActivity signals the session's idle clock that interactive input
	// arrived. A process blocked on input() produces no output, so without this
	// the idle clock (driven solely by stdout/stderr) would kill it while the
	// user is typing. Buffered + non-blocking sends below so feeding stdin never
	// blocks on a slow idle-clock consumer.
	stdinActivity := make(chan struct{}, 64)

	go func() {
		for {
			select {
			case <-sessionDone:
				return
			case chunk, ok := <-stdinCh:
				if !ok {
					return
				}
				// Full-write loop — never a bare Write (PITFALLS §6 partial-write).
				if _, err := writeAll(sb.Stdin(), chunk); err != nil {
					// Stdin pipe closed (process exited) — stop forwarding.
					return
				}
				// Count this input as idle-clock activity (see stdinActivity).
				select {
				case stdinActivity <- struct{}{}:
				default:
				}
			case msg := <-ctrlCh:
				switch msg.Type {
				case wire.ControlTypeStdinClose:
					// Deliver EOF exactly once (STDIN-02).
					closeStdin()
				case wire.ControlTypeKill:
					// Kill the container; session.RunInteractive will return.
					if err := sb.Kill(ctx); err != nil {
						log.Warn("worker: Kill failed", "err", err)
					}
				default:
					// Duplicate start or unknown — ignore.
				}
			}
		}
	}()

	// 9. Run the session. This blocks until the process terminates (normal,
	//     kill, wall clock, idle clock, CPU clock, or context cancel).
	//     The session owns the output pipes; the worker NEVER reads Stdout()/Stderr().
	sinks := session.Sinks{
		Stdout: func(b []byte) {
			if collectOutput {
				outputMu.Lock()
				stdoutBuf.Write(b)
				outputMu.Unlock()
			}
			if pubErr := w.pub.Stdout(jobID, string(b)); pubErr != nil {
				log.Warn("worker: publish Stdout failed", "err", pubErr)
			}
		},
		Stderr: func(b []byte) {
			if collectOutput {
				outputMu.Lock()
				stderrBuf.Write(b)
				outputMu.Unlock()
			}
			if pubErr := w.pub.Stderr(jobID, string(b)); pubErr != nil {
				log.Warn("worker: publish Stderr failed", "err", pubErr)
			}
		},
		// BeforeCleanup runs inside the session's terminate() AFTER the process
		// terminates but BEFORE the container is killed/removed — the only window
		// in which CopyFromContainer can read /workspace (D-07). Read here, upload
		// + persist in the worker teardown below. Best-effort: a read failure is
		// logged and yields zero artifacts; it never fails the job.
		BeforeCleanup: func(hookCtx context.Context) {
			if !collectOutput {
				return
			}
			ds, ok := sb.(DockerSandbox)
			if !ok || w.cfg.Artifacts == nil {
				return
			}
			// Exclude set (D-05/R4): input file basenames + ".compile_ready" + the
			// compile-output binary basename, so a compiled-language job never
			// returns its binary as an artifact.
			captured, rdErr := ds.ReadArtifacts(hookCtx, buildArtifactExcludeSet(spec))
			if rdErr != nil {
				log.Warn("worker: ReadArtifacts failed", "err", rdErr)
				return
			}
			capturedMu.Lock()
			capturedArtifacts = captured
			capturedMu.Unlock()
		},
		// Interactive input resets the idle clock (the stdin goroutine above
		// signals this on each chunk written to the sandbox).
		StdinActivity: stdinActivity,
	}

	runCtx, runSpan := tracer().Start(ctx, "run")
	result, _ := session.RunInteractive(runCtx, sb, spec.Limits, cpuFn, sinks)
	runSpan.End()

	// Signal the stdin goroutine to stop.
	close(sessionDone)

	// Determine terminal job state.
	state := wire.JobStateDone
	if result.TimedOut || result.IdleTimedOut {
		state = wire.JobStateError
	}

	// 10. Single teardown: publish result, write status, cleanup sandbox, release slot.
	teardown(result, state)
}

// releaseSlot returns one capacity unit to the semaphore.
func (w *Worker) releaseSlot() {
	w.slots <- struct{}{}
}

// WriteMetrics renders this worker's scrape metrics in Prometheus text format:
//
//   - code_runner_slots_used  — live sandboxes on THIS node (in-flight work).
//   - code_runner_slots_max   — this node's WORKER_MAX_SANDBOXES.
//   - code_runner_queue_depth — global LLEN jobs:queue (omitted on Redis error,
//     so a blip never publishes a misleading 0).
//
// An autoscaler scales on IN-FLIGHT load = sum(code_runner_slots_used) +
// max(code_runner_queue_depth). Because slots_used only drops when a session
// actually ends, scaling DOWN never stops a node with a running session — unlike
// scaling on queue depth alone.
func (w *Worker) WriteMetrics(ctx context.Context, out io.Writer) {
	used := w.cfg.MaxSandboxes - len(w.slots)
	if used < 0 {
		used = 0
	}
	fmt.Fprintf(out, "# HELP code_runner_slots_used Live sandboxes on this worker node.\n"+
		"# TYPE code_runner_slots_used gauge\ncode_runner_slots_used %d\n", used)
	fmt.Fprintf(out, "# HELP code_runner_slots_max Configured max sandboxes on this worker node.\n"+
		"# TYPE code_runner_slots_max gauge\ncode_runner_slots_max %d\n", w.cfg.MaxSandboxes)

	if w.store != nil {
		cctx, cancel := context.WithTimeout(ctx, gaugeCallbackTimeout)
		defer cancel()
		if depth, err := w.store.QueueDepth(cctx); err == nil {
			fmt.Fprintf(out, "# HELP code_runner_queue_depth Jobs waiting in jobs:queue (LLEN).\n"+
				"# TYPE code_runner_queue_depth gauge\ncode_runner_queue_depth %d\n", depth)
		}
	}
}

// MetricsHandler is the http.HandlerFunc serving WriteMetrics at /metrics. The
// worker binary mounts it on WORKER_METRICS_PORT so the platform's Prometheus
// (e.g. Fly's [metrics] block) can scrape it. Unauthenticated — it exposes only
// low-cardinality counts (no job_id, no payloads).
func (w *Worker) MetricsHandler() http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteMetrics(r.Context(), rw)
	}
}

// publishError publishes an error result when job setup fails before session start.
func (w *Worker) publishError(ctx context.Context, jobID string, spec wire.JobSpec) {
	_ = w.pub.Result(jobID, wire.ResultEvent{})
	if w.store != nil {
		_ = w.store.WriteStatus(ctx, wire.JobStatus{
			JobId:    jobID,
			Language: spec.Language,
			Version:  spec.Version,
			Channel:  spec.Channel,
			State:    wire.JobStateError,
		})
	}
}

// assembleRunResult builds the pullable RunResult from the terminal runner.Result
// plus the accumulated stdout/stderr (D-08: same within-budget bytes streamed to
// soketi, so runResult.Truncated mirrors the soketi stream's truncation). The
// Artifacts slice is initialised non-nil so the persisted JSON serialises an
// empty array (not null) when zero artifacts are captured (R4/D-04).
func assembleRunResult(r runner.Result, stdout, stderr string) wire.RunResult {
	return wire.RunResult{
		ExitCode:           wire.RunResultExitCode(r.ExitCode),
		Signal:             wire.RunResultSignal(r.Signal),
		TimedOut:           r.TimedOut,
		IdleTimedOut:       r.IdleTimedOut,
		Truncated:          r.Truncated,
		DurationMs:         r.DurationMs,
		Stdout:             stdout,
		Stderr:             stderr,
		Artifacts:          []wire.Artifact{},
		ArtifactsTruncated: false,
	}
}

// buildArtifactExcludeSet computes the set of paths that workspace-diff capture
// must NOT return as artifacts (D-05/R4): the input file names (by full
// sanitized relative path so a subdir input like "data/in.csv" is correctly
// excluded, not just its basename), the ".compile_ready" bridge marker, and
// (for a compiled language) the compile-output binary basename. ReadArtifacts
// also defensively excludes the marker, but we include it here so the contract
// is explicit. ReadArtifacts compares on the same full-relative-path form.
func buildArtifactExcludeSet(spec wire.JobSpec) map[string]bool {
	exclude := make(map[string]bool, len(spec.Files)+2)
	for _, f := range spec.Files {
		// Use the same sanitizer the runner uses to materialize the file, so the
		// exclude key matches the path the artifact reader sees. Skip names that
		// fail to sanitize (the runner rejects them before run anyway).
		rel, err := runner.SanitizeWorkspacePath(f.Name)
		if err != nil {
			continue
		}
		exclude[rel] = true
	}
	exclude[".compile_ready"] = true
	if out := compileOutputBasename(spec); out != "" {
		exclude[out] = true
	}
	return exclude
}

// compileOutputBasename derives the basename of the binary produced by a
// compiled-language compile step, so it is never returned as an artifact (R4).
// It returns "" for interpreted languages (no compile step).
//
// Two derivation sources, in order:
//  1. The "-o <target>" token in the compile argv (the conventional output flag
//     for gcc/clang/rustc/go build/etc.). The basename of <target> is the binary.
//  2. The first token of the run argv when no "-o" is present — for languages
//     whose run argv directly invokes the produced executable (e.g.
//     ["/workspace/prog"] or ["./app"]). A token that looks like an interpreter
//     path is not special-cased here because a non-nil spec.Compile already
//     signals a compiled language; the run target IS the binary.
func compileOutputBasename(spec wire.JobSpec) string {
	if spec.Compile == nil {
		return ""
	}
	compileArgv := []string(*spec.Compile)
	for i := 0; i < len(compileArgv)-1; i++ {
		if compileArgv[i] == "-o" {
			return filepath.Base(compileArgv[i+1])
		}
	}
	if len(spec.Run) > 0 {
		return filepath.Base(spec.Run[0])
	}
	return ""
}

// toResultEvent maps a runner.Result to the wire.ResultEvent published to
// soketi clients.
func toResultEvent(r runner.Result) wire.ResultEvent {
	return wire.ResultEvent{
		ExitCode:     wire.ResultEventExitCode(r.ExitCode),
		Signal:       wire.ResultEventSignal(r.Signal),
		TimedOut:     r.TimedOut,
		IdleTimedOut: r.IdleTimedOut,
		Truncated:    r.Truncated,
		DurationMs:   r.DurationMs,
	}
}

// writeAll performs a full-write loop over p into w, mirroring io.Copy's
// approach to partial writes (PITFALLS §6). Returns the total bytes written
// and any error.
func writeAll(w io.Writer, p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n, err := w.Write(p)
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
