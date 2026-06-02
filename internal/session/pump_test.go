package session_test

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/session"
)

// TestPumpUnderCap verifies that when input is under the cap, all bytes are
// forwarded to the sink, truncated stays false, and bytes arrive in order.
func TestPumpUnderCap(t *testing.T) {
	data := []byte("hello world")
	capBytes := int64(1024) // 1 KiB cap — data is far under cap

	sharedBudget := &atomic.Int64{}
	sharedBudget.Store(capBytes)
	truncated := &atomic.Bool{}

	var received []byte
	var mu sync.Mutex
	sink := func(chunk []byte) {
		mu.Lock()
		received = append(received, chunk...)
		mu.Unlock()
	}

	pump := session.NewPump(bytes.NewReader(data), sharedBudget, truncated, sink, nil)
	err := pump.Run()
	require.NoError(t, err)

	assert.Equal(t, data, received, "all bytes must be forwarded when under cap")
	assert.False(t, truncated.Load(), "truncated must be false when under cap")
}

// TestPumpOverCapCapsAndDrains verifies that when input exceeds the cap:
//   - The sink receives exactly cap bytes
//   - truncated is set to true
//   - The reader is fully drained (no goroutine leak / no block)
func TestPumpOverCapCapsAndDrains(t *testing.T) {
	capKiB := 2
	capBytes := int64(capKiB * 1024)
	extra := 512 // extra bytes beyond cap

	// Fill with deterministic data.
	data := make([]byte, int(capBytes)+extra)
	for i := range data {
		data[i] = byte(i % 251)
	}

	// blockingReader wraps a pipe; writing goroutine only proceeds after
	// the pump has fully drained it — proving the pump keeps reading past cap.
	pr, pw := io.Pipe()
	var writerDone atomic.Bool

	// Write data asynchronously.
	go func() {
		if _, err := pw.Write(data); err != nil {
			return
		}
		pw.Close()
		writerDone.Store(true)
	}()

	sharedBudget := &atomic.Int64{}
	sharedBudget.Store(capBytes)
	truncated := &atomic.Bool{}

	var received []byte
	var mu sync.Mutex
	sink := func(chunk []byte) {
		mu.Lock()
		received = append(received, chunk...)
		mu.Unlock()
	}

	pump := session.NewPump(pr, sharedBudget, truncated, sink, nil)
	err := pump.Run()
	require.NoError(t, err)

	mu.Lock()
	receivedLen := len(received)
	mu.Unlock()

	assert.EqualValues(t, capBytes, receivedLen, "sink must receive exactly cap bytes")
	assert.True(t, truncated.Load(), "truncated must be true when cap is exceeded")
	assert.True(t, writerDone.Load(), "reader must be drained to EOF so writer never blocks")
}

// TestPumpSharedBudget verifies that two pumps (stdout + stderr) share one
// combined budget: the SUM is capped at outputKb, not each stream independently.
func TestPumpSharedBudget(t *testing.T) {
	capBytes := int64(1024) // 1 KiB combined cap

	halfData := make([]byte, 600) // 600 bytes each — combined 1200 > cap
	for i := range halfData {
		halfData[i] = byte(i % 127)
	}

	sharedBudget := &atomic.Int64{}
	sharedBudget.Store(capBytes)
	truncated := &atomic.Bool{}

	var total atomic.Int64
	sink := func(chunk []byte) { total.Add(int64(len(chunk))) }

	// Run two pumps sequentially with shared budget/truncated.
	p1 := session.NewPump(bytes.NewReader(halfData), sharedBudget, truncated, sink, nil)
	require.NoError(t, p1.Run())

	p2 := session.NewPump(bytes.NewReader(halfData), sharedBudget, truncated, sink, nil)
	require.NoError(t, p2.Run())

	assert.EqualValues(t, capBytes, total.Load(),
		"combined output from both pumps must equal cap, not 2×cap")
	assert.True(t, truncated.Load(), "truncated must be true — combined exceeded cap")
}

// TestPumpActivitySignal verifies that the pump emits an activity signal on
// every forwarded chunk so the idle clock can reset.
func TestPumpActivitySignal(t *testing.T) {
	data := []byte("some output chunk")
	capBytes := int64(1024)

	sharedBudget := &atomic.Int64{}
	sharedBudget.Store(capBytes)
	truncated := &atomic.Bool{}
	activity := make(chan struct{}, 16)

	sink := func(_ []byte) {}
	pump := session.NewPump(bytes.NewReader(data), sharedBudget, truncated, sink, activity)
	require.NoError(t, pump.Run())

	select {
	case <-activity:
		// got at least one activity signal
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected at least one activity signal but got none")
	}
}

// TestPumpBlockingReaderDrained proves that a reader which blocks forever
// if not fully consumed does not cause Run() to hang when cap is exceeded.
// The test uses a real goroutine and a timeout to detect hangs.
func TestPumpBlockingReaderDrained(t *testing.T) {
	capBytes := int64(512)
	totalBytes := 1024 // double the cap

	pr, pw := io.Pipe()

	// Writer: writes totalBytes then closes. If the pump stops reading before
	// totalBytes are consumed, pw.Write will block (pipe is full / reader gone).
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := make([]byte, totalBytes)
		pw.Write(chunk) //nolint:errcheck
		pw.Close()
	}()

	sharedBudget := &atomic.Int64{}
	sharedBudget.Store(capBytes)
	truncated := &atomic.Bool{}
	sink := func(_ []byte) {}

	pumpDone := make(chan error, 1)
	go func() {
		p := session.NewPump(pr, sharedBudget, truncated, sink, nil)
		pumpDone <- p.Run()
	}()

	select {
	case err := <-pumpDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("pump.Run() blocked — reader was not fully drained past cap")
	}

	select {
	case <-done:
		// writer finished cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("writer goroutine blocked — pump did not drain the pipe")
	}

	assert.True(t, truncated.Load())
}
