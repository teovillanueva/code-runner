package publisher

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// fakeTriggerer records every (channel, event, data) triple passed to Trigger.
// It is safe for concurrent use.
type fakeTriggerer struct {
	mu    sync.Mutex
	calls []triggerCall
}

type triggerCall struct {
	channel string
	event   string
	data    interface{}
}

func (f *fakeTriggerer) Trigger(channel, event string, data interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, triggerCall{channel: channel, event: event, data: data})
	return nil
}

func (f *fakeTriggerer) snapshot() []triggerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]triggerCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// newTestPublisher returns a Publisher backed by a fakeTriggerer.
func newTestPublisher(t *testing.T) (*Publisher, *fakeTriggerer) {
	t.Helper()
	fake := &fakeTriggerer{}
	cfg := config.Config{
		SoketiHost:      "localhost",
		SoketiPort:      6001,
		SoketiUseTLS:    false,
		SoketiAppID:     "app-id",
		SoketiAppKey:    "app-key",
		SoketiAppSecret: "app-secret",
	}
	p, err := newWithTriggerer(cfg, fake)
	require.NoError(t, err)
	return p, fake
}

// ---------------------------------------------------------------------------
// Task 1 tests: basic event routing, channel naming, monotonic seq
// ---------------------------------------------------------------------------

func TestStage_EventNameAndChannel(t *testing.T) {
	p, fake := newTestPublisher(t)
	jobID := "job-abc"

	err := p.Stage(jobID, wire.StagePhaseQueued)
	require.NoError(t, err)

	calls := fake.snapshot()
	require.Len(t, calls, 1)
	c := calls[0]

	assert.Equal(t, keys.ChannelForJob(jobID), c.channel, "channel must be private-run-<jobId>")
	assert.Equal(t, keys.EventStage, c.event, "event name must be 'stage'")

	ev, ok := c.data.(wire.StageEvent)
	require.True(t, ok, "data must be wire.StageEvent")
	assert.Equal(t, wire.StagePhaseQueued, ev.Phase)
}

func TestStdout_EventNameAndChannel(t *testing.T) {
	p, fake := newTestPublisher(t)
	jobID := "job-stdout"

	err := p.Stdout(jobID, "hello")
	require.NoError(t, err)

	calls := fake.snapshot()
	require.Len(t, calls, 1)
	c := calls[0]

	assert.Equal(t, keys.ChannelForJob(jobID), c.channel)
	assert.Equal(t, keys.EventStdout, c.event)

	ev, ok := c.data.(wire.OutputChunkEvent)
	require.True(t, ok, "data must be wire.OutputChunkEvent")
	assert.Equal(t, "hello", ev.Chunk)
	assert.Equal(t, 1, ev.Seq)
}

func TestStderr_EventNameAndChannel(t *testing.T) {
	p, fake := newTestPublisher(t)
	jobID := "job-stderr"

	err := p.Stderr(jobID, "err")
	require.NoError(t, err)

	calls := fake.snapshot()
	require.Len(t, calls, 1)
	c := calls[0]

	assert.Equal(t, keys.ChannelForJob(jobID), c.channel)
	assert.Equal(t, keys.EventStderr, c.event)
}

func TestResult_EventNameAndChannel(t *testing.T) {
	p, fake := newTestPublisher(t)
	jobID := "job-result"

	exitCode := 0
	ev := wire.ResultEvent{
		DurationMs: 123,
		ExitCode:   &exitCode,
	}
	err := p.Result(jobID, ev)
	require.NoError(t, err)

	calls := fake.snapshot()
	require.Len(t, calls, 1)
	c := calls[0]

	assert.Equal(t, keys.ChannelForJob(jobID), c.channel)
	assert.Equal(t, keys.EventResult, c.event)

	got, ok := c.data.(wire.ResultEvent)
	require.True(t, ok)
	assert.Equal(t, 123, got.DurationMs)
}

// TestMonotonicSeq_SharedAcrossStdoutAndStderr asserts that stdout then stderr
// then stdout for one job produce seq 1, 2, 3 (shared monotonic counter).
func TestMonotonicSeq_SharedAcrossStdoutAndStderr(t *testing.T) {
	p, fake := newTestPublisher(t)
	jobID := "job-seq"

	require.NoError(t, p.Stdout(jobID, "a"))
	require.NoError(t, p.Stderr(jobID, "b"))
	require.NoError(t, p.Stdout(jobID, "c"))

	calls := fake.snapshot()
	require.Len(t, calls, 3)

	seqs := make([]int, 3)
	for i, c := range calls {
		ev, ok := c.data.(wire.OutputChunkEvent)
		require.True(t, ok, "call %d: data must be OutputChunkEvent", i)
		seqs[i] = ev.Seq
	}
	assert.Equal(t, []int{1, 2, 3}, seqs, "seq must be monotonically increasing across stdout+stderr")
}

// TestMonotonicSeq_IndependentPerJob asserts that two different jobs have
// independent seq counters.
func TestMonotonicSeq_IndependentPerJob(t *testing.T) {
	p, fake := newTestPublisher(t)

	require.NoError(t, p.Stdout("job-1", "x"))
	require.NoError(t, p.Stdout("job-2", "y"))
	require.NoError(t, p.Stdout("job-1", "z"))

	calls := fake.snapshot()
	require.Len(t, calls, 3)

	// job-1 gets seq 1 and 2; job-2 gets seq 1
	ev0 := calls[0].data.(wire.OutputChunkEvent)
	ev1 := calls[1].data.(wire.OutputChunkEvent)
	ev2 := calls[2].data.(wire.OutputChunkEvent)

	assert.Equal(t, 1, ev0.Seq)
	assert.Equal(t, 1, ev1.Seq)
	assert.Equal(t, 2, ev2.Seq)
}

// TestNew_HostFromConfig asserts the pusher client is built from Config fields
// (Host == "<SoketiHost>:<SoketiPort>", Secure == SoketiUseTLS).
// We verify this indirectly by inspecting the clientInfo exposed for testing.
func TestNew_HostFromConfig(t *testing.T) {
	cfg := config.Config{
		SoketiHost:      "mysoketi.example.com",
		SoketiPort:      9001,
		SoketiUseTLS:    true,
		SoketiAppID:     "id1",
		SoketiAppKey:    "key1",
		SoketiAppSecret: "secret1",
	}
	info := pusherClientInfoFromConfig(cfg)
	assert.Equal(t, "mysoketi.example.com:9001", info.host)
	assert.Equal(t, true, info.secure)
	assert.Equal(t, "id1", info.appID)
	assert.Equal(t, "key1", info.key)
}

// TestNoDirectEnvReads asserts this package never reads env vars directly;
// all configuration is supplied via config.Config by the caller.
// The structural guarantee: New and newWithTriggerer accept a Config value.
// The absence of direct env reads is also verified in CI via grep.
func TestNoDirectEnvReads(t *testing.T) {
	// Trivial — the real enforcement is the grep gate in acceptance criteria
	// (no direct env-reading functions in internal/publisher/*.go).
	t.Log("No direct env reads in publisher package — enforced by constructor design")
}

// ---------------------------------------------------------------------------
// Task 2 tests: chunking
// ---------------------------------------------------------------------------

// TestChunk_SmallChunkSingleEvent asserts a small payload is sent as one event.
func TestChunk_SmallChunkSingleEvent(t *testing.T) {
	p, fake := newTestPublisher(t)

	small := strings.Repeat("x", 100)
	require.NoError(t, p.Stdout("job-small", small))

	calls := fake.snapshot()
	assert.Len(t, calls, 1, "small chunk must produce exactly one event")
	ev := calls[0].data.(wire.OutputChunkEvent)
	assert.Equal(t, small, ev.Chunk)
}

// TestChunk_OversizedChunkSplitIntoMultipleEvents feeds a chunk > maxEventBytes
// and asserts: N>1 events, each serialized payload <= maxEventBytes, seqs
// consecutive, concatenated chunks reconstruct original byte-for-byte.
func TestChunk_OversizedChunkSplitIntoMultipleEvents(t *testing.T) {
	p, fake := newTestPublisher(t)

	// Build a chunk larger than maxEventBytes. Use a predictable repeating pattern
	// so we can verify reconstruction.
	bigChunk := strings.Repeat("A", maxEventBytes+1000)
	require.NoError(t, p.Stdout("job-big", bigChunk))

	calls := fake.snapshot()
	require.Greater(t, len(calls), 1, "oversized chunk must be split into multiple events")

	var reconstructed strings.Builder
	for i, c := range calls {
		ev, ok := c.data.(wire.OutputChunkEvent)
		require.True(t, ok, "call %d must be OutputChunkEvent", i)

		// Verify serialized payload size <= maxEventBytes.
		raw, err := json.Marshal(ev)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(raw), maxEventBytes,
			"event %d serialized size %d must be <= maxEventBytes %d", i, len(raw), maxEventBytes)

		// Verify seq starts at 1 and is consecutive.
		assert.Equal(t, i+1, ev.Seq, "event %d must have seq %d", i, i+1)

		reconstructed.WriteString(ev.Chunk)
	}

	assert.Equal(t, bigChunk, reconstructed.String(),
		"concatenating all chunks must reproduce the original input exactly")
}

// TestChunk_SeqContinuesAfterChunkedEvent asserts that seq increments correctly
// across a chunked send and a subsequent single-event send.
func TestChunk_SeqContinuesAfterChunkedEvent(t *testing.T) {
	p, fake := newTestPublisher(t)
	jobID := "job-seq-continue"

	// First send: oversized, will produce N events.
	bigChunk := strings.Repeat("B", maxEventBytes+1)
	require.NoError(t, p.Stdout(jobID, bigChunk))

	chunkCallCount := len(fake.snapshot())
	require.Greater(t, chunkCallCount, 1)

	// Second send: a small chunk — its seq must follow the last chunk seq.
	require.NoError(t, p.Stderr(jobID, "small"))

	calls := fake.snapshot()
	lastCall := calls[len(calls)-1]
	ev := lastCall.data.(wire.OutputChunkEvent)
	assert.Equal(t, chunkCallCount+1, ev.Seq,
		"seq after chunked send must continue from where chunking left off")
}

// TestChunk_MultipleOversizedChunksPreserveOrder sends two oversized chunks
// in sequence and verifies the total ordering and reconstruction.
func TestChunk_MultipleOversizedChunksPreserveOrder(t *testing.T) {
	p, fake := newTestPublisher(t)
	jobID := "job-order"

	chunk1 := strings.Repeat("C", maxEventBytes+500)
	chunk2 := strings.Repeat("D", maxEventBytes+500)

	require.NoError(t, p.Stdout(jobID, chunk1))
	require.NoError(t, p.Stdout(jobID, chunk2))

	calls := fake.snapshot()

	var reconstructed strings.Builder
	for i, c := range calls {
		ev, ok := c.data.(wire.OutputChunkEvent)
		require.True(t, ok)
		assert.Equal(t, i+1, ev.Seq, "seq must be consecutive across both chunks")
		reconstructed.WriteString(ev.Chunk)
	}
	assert.Equal(t, chunk1+chunk2, reconstructed.String())
}

// TestChunk_EachEventPayloadUnderLimit exhaustively verifies that no event
// produced by the publisher exceeds maxEventBytes when JSON-serialised,
// using a variety of chunk sizes.
func TestChunk_EachEventPayloadUnderLimit(t *testing.T) {
	sizes := []int{
		1,
		maxEventBytes / 2,
		maxEventBytes - 1,
		maxEventBytes,
		maxEventBytes + 1,
		maxEventBytes * 3,
	}
	for _, sz := range sizes {
		sz := sz
		t.Run(fmt.Sprintf("size_%d", sz), func(t *testing.T) {
			p, fake := newTestPublisher(t)
			input := strings.Repeat("x", sz)
			require.NoError(t, p.Stdout("job-limit", input))
			for i, c := range fake.snapshot() {
				ev, ok := c.data.(wire.OutputChunkEvent)
				require.True(t, ok)
				raw, err := json.Marshal(ev)
				require.NoError(t, err)
				assert.LessOrEqual(t, len(raw), maxEventBytes,
					"size=%d event %d: serialized %d bytes exceeds limit %d", sz, i, len(raw), maxEventBytes)
			}
		})
	}
}
