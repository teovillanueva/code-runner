package worker

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// opLog records what the pump did to the sandbox, in order: "w:<chunk>" per
// write, "eof" for the close, "kill" for a kill.
type opLog struct {
	mu  sync.Mutex
	ops []string
	// failAfterEOF makes a write after the close fail, like a closed pipe.
	failAfterEOF bool
	eof          bool
}

func (l *opLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.eof && l.failAfterEOF {
		return 0, io.ErrClosedPipe
	}
	l.ops = append(l.ops, "w:"+string(p))
	return len(p), nil
}

func (l *opLog) record(op string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if op == "eof" {
		l.eof = true
	}
	l.ops = append(l.ops, op)
}

func (l *opLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ops...)
}

type pumpHarness struct {
	stdinCh  chan []byte
	ctrlCh   chan wire.ControlMessage
	done     chan struct{}
	activity chan struct{}
	log      *opLog
	eof      chan struct{} // closed by the first closeStdin
	killed   chan struct{} // receives one value per kill
	pump     *stdinPump
}

func newPumpHarness(w io.Writer, log *opLog) *pumpHarness {
	h := &pumpHarness{
		stdinCh:  make(chan []byte, 256),
		ctrlCh:   make(chan wire.ControlMessage, 32),
		done:     make(chan struct{}),
		activity: make(chan struct{}, 64),
		log:      log,
		eof:      make(chan struct{}),
		killed:   make(chan struct{}, 4),
	}
	var once sync.Once
	h.pump = &stdinPump{
		stdinCh: h.stdinCh,
		ctrlCh:  h.ctrlCh,
		done:    h.done,
		stdin:   w,
		closeStdin: func() {
			once.Do(func() {
				log.record("eof")
				close(h.eof)
			})
		},
		kill: func() {
			log.record("kill")
			h.killed <- struct{}{}
		},
		activity: h.activity,
	}
	return h
}

// start runs the pump and returns a channel closed when run returns.
func (h *pumpHarness) start() <-chan struct{} {
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		h.pump.run()
	}()
	return finished
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// The regression: chunks and the close are both queued before the pump
// starts (what a compiled-language batch run looks like when the compile
// ends). Before the fix the select picked at random, so the close came first
// in most rounds (with three chunks, all three won only 1 round in 8).
func TestStdinPump_ChunksQueuedBeforeCloseAreWrittenFirst(t *testing.T) {
	const rounds = 500
	for i := 0; i < rounds; i++ {
		log := &opLog{}
		h := newPumpHarness(log, log)
		h.stdinCh <- []byte("a")
		h.stdinCh <- []byte("b")
		h.stdinCh <- []byte("c")
		h.ctrlCh <- wire.ControlMessage{Type: wire.ControlTypeStdinClose}

		finished := h.start()
		waitClosed(t, h.eof, "EOF")
		close(h.done)
		waitClosed(t, finished, "pump to return")

		require.Equal(t, []string{"w:a", "w:b", "w:c", "eof"}, log.snapshot(), "round %d", i)
	}
}

func TestStdinPump_InteractiveChunksInOrderWithActivity(t *testing.T) {
	log := &opLog{}
	h := newPumpHarness(log, log)
	finished := h.start()

	for _, c := range []string{"1\n", "2\n", "3\n"} {
		h.stdinCh <- []byte(c)
		select {
		case <-h.activity:
		case <-time.After(2 * time.Second):
			t.Fatalf("no idle-clock activity for chunk %q", c)
		}
	}
	h.ctrlCh <- wire.ControlMessage{Type: wire.ControlTypeStdinClose}
	waitClosed(t, h.eof, "EOF")
	close(h.done)
	waitClosed(t, finished, "pump to return")

	assert.Equal(t, []string{"w:1\n", "w:2\n", "w:3\n", "eof"}, log.snapshot())
}

// After EOF a late chunk is dropped instead of written to the closed pipe:
// that write would fail and end the pump, so a later kill would be ignored.
func TestStdinPump_ChunkAfterCloseIsDroppedAndKillStillApplies(t *testing.T) {
	// The late chunk and the kill are both ready, so the select order varies:
	// repeat so the chunk-first order (the one that used to drop the kill) runs.
	const rounds = 50
	for i := 0; i < rounds; i++ {
		log := &opLog{failAfterEOF: true}
		h := newPumpHarness(log, log)
		finished := h.start()

		h.ctrlCh <- wire.ControlMessage{Type: wire.ControlTypeStdinClose}
		waitClosed(t, h.eof, "EOF")
		h.stdinCh <- []byte("late")
		h.ctrlCh <- wire.ControlMessage{Type: wire.ControlTypeKill}

		select {
		case <-h.killed:
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: kill after a late chunk was not applied", i)
		}
		close(h.done)
		waitClosed(t, finished, "pump to return")
		require.Equal(t, []string{"eof", "kill"}, log.snapshot(), "round %d", i)
	}
}

type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// The program exited before reading its input: the write fails (in the drain
// or the plain chunk path) and the pump returns instead of hanging.
func TestStdinPump_WriteFailureEndsPump(t *testing.T) {
	log := &opLog{}
	h := newPumpHarness(brokenPipe{}, log)
	h.stdinCh <- []byte("x")
	h.ctrlCh <- wire.ControlMessage{Type: wire.ControlTypeStdinClose}

	waitClosed(t, h.start(), "pump to return")
}

// A session that ends while chunks are still queued stops the drain.
func TestStdinPump_DoneStopsDrain(t *testing.T) {
	log := &opLog{}
	h := newPumpHarness(log, log)
	h.stdinCh <- []byte("x")
	close(h.done)
	assert.False(t, h.pump.drain(), "drain must report stop once the session is done")
	assert.Empty(t, log.snapshot())
}
