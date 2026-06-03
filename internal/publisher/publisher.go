package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	pusher "github.com/pusher/pusher-http-go/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// instrumentationName is the meter scope for publisher-emitted soketi metrics.
const instrumentationName = "code-runner-worker"

// publishDuration / publishErrors resolve the soketi-publish instruments from the
// CURRENT global MeterProvider on each call (lazy resolution — see worker.go).
// A MeterProvider installed after package init (otelinit.Init at boot, or a
// ManualReader in tests) is honoured. The no-op provider returns no-op
// instruments at zero cost.
//
// publishDuration (histogram, unit "s") times every soketi Trigger; publishErrors
// (counter) increments once per non-nil Trigger error. Both carry NO attributes
// — there is nothing low-cardinality to add at the trigger chokepoint, and
// job_id/channel must NEVER become metric dimensions (RESEARCH anti-pattern:
// high cardinality).
func publishDuration() metric.Float64Histogram {
	h, _ := otel.Meter(instrumentationName).Float64Histogram(
		"code_runner.publish.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Wall time of a single soketi Trigger call, in seconds."),
	)
	return h
}

func publishErrors() metric.Int64Counter {
	c, _ := otel.Meter(instrumentationName).Int64Counter(
		"code_runner.publish.errors",
		metric.WithUnit("{error}"),
		metric.WithDescription("Count of soketi Trigger calls that returned a non-nil error."),
	)
	return c
}

// instrumentedTriggerer wraps any triggerer and records the publish-latency
// histogram + publish-error counter around every Trigger call. It is the single
// chokepoint for soketi publish metrics: both the production pusherTriggerer and
// any test fake are measured when wrapped here.
type instrumentedTriggerer struct {
	inner triggerer
}

func (it *instrumentedTriggerer) Trigger(channel, event string, data interface{}) error {
	start := time.Now()
	err := it.inner.Trigger(channel, event, data)
	publishDuration().Record(context.Background(), time.Since(start).Seconds())
	if err != nil {
		publishErrors().Add(context.Background(), 1)
	}
	return err
}

// maxEventBytes is the maximum serialised size (in bytes) of an
// OutputChunkEvent payload that the publisher will emit.
//
// soketi (and Pusher) reject events whose data field exceeds ~10 KB.
// We set the cap to 8 KB to leave ~2 KB of headroom for the JSON envelope
// (event name, channel, socket_id, auth timestamp) and the Seq field so the
// on-wire event always stays comfortably under the 10 KB limit.
//
// Reference: PITFALLS pitfall 8 — "no event exceeds 10 KB".
const maxEventBytes = 8 * 1024 // 8 192 bytes

// triggerer is the interface that wraps the single method the publisher
// needs from pusher.Client. Using an interface here allows tests to inject
// a fake without a live soketi instance.
type triggerer interface {
	Trigger(channel, event string, data interface{}) error
}

// pusherTriggerer adapts *pusher.Client to the triggerer interface.
type pusherTriggerer struct {
	c *pusher.Client
}

func (p *pusherTriggerer) Trigger(channel, event string, data interface{}) error {
	return p.c.Trigger(channel, event, data)
}

// Publisher publishes contract events to the soketi output channel for a job.
// It is safe for concurrent use from multiple goroutines.
type Publisher struct {
	t   triggerer
	mu  sync.Mutex
	seq map[string]int // per-job monotonic sequence counter
}

// clientInfo holds the fields used to construct a pusher.Client.
// Exported only for test assertions.
type clientInfo struct {
	host   string
	secure bool
	appID  string
	key    string
}

// pusherClientInfoFromConfig derives the pusher.Client fields from cfg.
// It is package-level so tests can assert the mapping without a live client.
func pusherClientInfoFromConfig(cfg config.Config) clientInfo {
	return clientInfo{
		host:   fmt.Sprintf("%s:%d", cfg.SoketiHost, cfg.SoketiPort),
		secure: cfg.SoketiUseTLS,
		appID:  cfg.SoketiAppID,
		key:    cfg.SoketiAppKey,
	}
}

// New constructs a Publisher from the soketi credentials in cfg.
// Credentials are taken from cfg only; this package never reads env vars directly.
func New(cfg config.Config) (*Publisher, error) {
	info := pusherClientInfoFromConfig(cfg)
	c := &pusher.Client{
		AppID:  info.appID,
		Key:    info.key,
		Secret: cfg.SoketiAppSecret,
		Host:   info.host,
		Secure: info.secure,
	}
	return newWithTriggerer(cfg, &pusherTriggerer{c: c})
}

// newWithTriggerer is an internal constructor used by tests to inject a fake.
// The supplied triggerer is wrapped in instrumentedTriggerer so every soketi
// Trigger (production OR test fake) is timed and its errors counted at the single
// publish chokepoint (code_runner.publish.duration / .errors).
func newWithTriggerer(_ config.Config, t triggerer) (*Publisher, error) {
	return &Publisher{
		t:   &instrumentedTriggerer{inner: t},
		seq: make(map[string]int),
	}, nil
}

// nextSeq returns and increments the per-job sequence counter.
// The caller must NOT hold p.mu when calling this.
func (p *Publisher) nextSeq(jobID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq[jobID]++
	return p.seq[jobID]
}

// Stage triggers a "stage" event on the job's channel.
func (p *Publisher) Stage(jobID string, phase wire.StagePhase) error {
	ev := wire.StageEvent{Phase: phase}
	return p.t.Trigger(keys.ChannelForJob(jobID), keys.EventStage, ev)
}

// Stdout triggers one or more "stdout" events for the given chunk.
// If the JSON-serialised OutputChunkEvent for the full chunk would exceed
// maxEventBytes the chunk is split into multiple events, each below the limit.
// Each event carries its own monotonically increasing seq for the job.
func (p *Publisher) Stdout(jobID string, chunk string) error {
	return p.triggerOutput(jobID, keys.EventStdout, chunk)
}

// Stderr triggers one or more "stderr" events for the given chunk.
// The seq counter is shared with Stdout so the client sees one ordered stream.
func (p *Publisher) Stderr(jobID string, chunk string) error {
	return p.triggerOutput(jobID, keys.EventStderr, chunk)
}

// Result triggers the terminal "result" event on the job's channel.
func (p *Publisher) Result(jobID string, ev wire.ResultEvent) error {
	return p.t.Trigger(keys.ChannelForJob(jobID), keys.EventResult, ev)
}

// triggerOutput handles the chunking logic for Stdout/Stderr.
// It splits the chunk into pieces whose JSON-serialised form stays within
// maxEventBytes, assigns a monotonic seq to each piece, and triggers them
// in byte order.
func (p *Publisher) triggerOutput(jobID, eventName, chunk string) error {
	pieces := splitChunk(chunk)
	for _, piece := range pieces {
		seq := p.nextSeq(jobID)
		ev := wire.OutputChunkEvent{Chunk: piece, Seq: seq}
		if err := p.t.Trigger(keys.ChannelForJob(jobID), eventName, ev); err != nil {
			return err
		}
	}
	return nil
}

// splitChunk splits chunk into sub-strings such that the JSON-serialised
// OutputChunkEvent for each sub-string fits within maxEventBytes.
//
// We compute the overhead introduced by the JSON envelope around the Chunk
// field with a large (but known) Seq, then use the remaining capacity to
// determine the maximum safe UTF-8 chunk size per event.
//
// Splitting is done on byte boundaries which is safe because the chunk is
// treated as opaque bytes (the client reconstructs by concatenation).
func splitChunk(chunk string) []string {
	if len(chunk) == 0 {
		return []string{chunk}
	}

	// Compute the JSON overhead for the envelope:
	//   {"chunk":"","seq":9999999}  — worst-case Seq is a 7-digit number.
	// This gives us a conservative fixed overhead to subtract from the budget.
	const seqOverhead = 7 // max digits in a realistic seq number
	// overhead = len(`{"chunk":"","seq":`) + seqOverhead + len(`}`)
	//          = 18 + 7 + 1 = 26 bytes
	// Plus quotes around the chunk value: 2 bytes.
	// Plus JSON string escaping: we account for this by computing per-piece.
	const baseOverhead = 18 + seqOverhead + 1 + 2 // = 28 bytes

	// Maximum bytes available for the raw chunk content in one event.
	// We subtract baseOverhead plus a small buffer for JSON escaping of
	// special characters inside the chunk (each special char expands to \uXXXX
	// = 6 bytes; worst case is every byte needing escaping, but that would mean
	// a binary payload — for text output the escape ratio is very low).
	// We use a conservative escape budget: 10 % of maxEventBytes.
	const escapeBudget = maxEventBytes / 10
	maxChunkBytes := maxEventBytes - baseOverhead - escapeBudget

	if maxChunkBytes <= 0 {
		// Safety net: should never happen with the chosen constants.
		maxChunkBytes = 64
	}

	b := []byte(chunk)
	if len(b) <= maxChunkBytes {
		// Fast path: single event, no split needed.
		// Still verify the final serialised size to guard against heavy escaping.
		ev := wire.OutputChunkEvent{Chunk: chunk, Seq: 1}
		raw, err := json.Marshal(ev)
		if err == nil && len(raw) <= maxEventBytes {
			return []string{chunk}
		}
		// Fall through to the byte-splitting loop below.
	}

	var pieces []string
	for len(b) > 0 {
		end := maxChunkBytes
		if end > len(b) {
			end = len(b)
		}
		// Adjust end to a valid UTF-8 rune boundary by backing up if needed.
		for end > 0 && end < len(b) && b[end]&0xC0 == 0x80 {
			end--
		}
		if end == 0 {
			end = maxChunkBytes // give up on boundary safety for non-UTF-8 data
			if end > len(b) {
				end = len(b)
			}
		}
		piece := string(b[:end])

		// Verify the serialised size with a dummy seq to ensure we're within
		// the limit even after JSON escaping.
		ev := wire.OutputChunkEvent{Chunk: piece, Seq: 9999999}
		raw, err := json.Marshal(ev)
		if err == nil && len(raw) > maxEventBytes && len(piece) > 1 {
			// The piece is still too large after escaping — halve and retry.
			end = end / 2
			if end == 0 {
				end = 1
			}
			piece = string(b[:end])
		}

		pieces = append(pieces, piece)
		b = b[end:]
	}
	return pieces
}
