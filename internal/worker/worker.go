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
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

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
	slots := make(chan struct{}, cfg.MaxSandboxes)
	for i := 0; i < cfg.MaxSandboxes; i++ {
		slots <- struct{}{}
	}
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
	if w.store != nil {
		w.startHeartbeat(ctx)
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

		// Handle the job in a goroutine. The slot is released inside teardown
		// (or on early-return paths) inside runJobFromSpec — NOT here.
		go func(id string) {
			w.runJob(ctx, id, w.releaseSlot)
		}(jobID)
	}
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

	// 3. Create the sandbox. The container is started by Create (docker.go calls
	//    ContainerStart inside Create) but the process reads from stdin so it will
	//    block waiting for input. The start-handshake below gates when we begin
	//    the session clocks.
	//
	//    sandbox.create is a REAL child span: we start it here and pass its ctx
	//    into Create, so docker.go's ContainerCreate/ContainerStart execute WITHIN
	//    this span. (The create-latency histogram is wired separately in 08-04
	//    inside the same Create function — span here, histogram there, no overlap.)
	createCtx, createSpan := tracer().Start(ctx, "sandbox.create")
	sb, err := w.runner.Create(createCtx, spec)
	createSpan.End()
	if err != nil {
		log.Error("worker: Create sandbox failed", "err", err)
		stdinSub.Close() //nolint:errcheck
		ctrlSub.Close()  //nolint:errcheck
		w.publishError(ctx, jobID, spec)
		recordTerminal(ctx, terminalError)
		releaseSlot() // early return — sandbox never occupied a slot
		return
	}

	// Record ownership of this job in Redis (best-effort — log on error).
	if w.store != nil {
		if err := w.store.AddOwnedJob(ctx, w.workerID, jobID); err != nil {
			log.Warn("worker: AddOwnedJob failed", "err", err)
		}
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
			if pubErr := w.pub.Stderr(jobID, string(b)); pubErr != nil {
				log.Warn("worker: publish compile stderr failed", "err", pubErr)
			}
		})

		// On compile failure (non-zero exit OR infrastructure error), publish a
		// terminal result and return — the run argv MUST NOT execute.
		compileExitCode := compileResult.ExitCode
		if compileErr != nil || compileExitCode != 0 {
			if compileErr != nil {
				// Infrastructure failure (Docker exec error, context cancelled):
				// treat as non-zero exit.
				log.Warn("worker: compile step error", "err", compileErr)
				if compileExitCode == 0 {
					compileExitCode = 1 // normalise infrastructure errors to exit 1
				}
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
			if pubErr := w.pub.Stdout(jobID, string(b)); pubErr != nil {
				log.Warn("worker: publish Stdout failed", "err", pubErr)
			}
		},
		Stderr: func(b []byte) {
			if pubErr := w.pub.Stderr(jobID, string(b)); pubErr != nil {
				log.Warn("worker: publish Stderr failed", "err", pubErr)
			}
		},
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
