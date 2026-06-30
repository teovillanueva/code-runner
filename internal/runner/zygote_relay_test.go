package runner

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// TestFrameRoundTrip encodes each frame type with writeFrame and decodes it back
// with frameDecoder, asserting type + payload survive byte-for-byte. This is the
// core wire-format contract that MUST match zygote_agent.py.
func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		ftype   byte
		payload []byte
	}{
		{"hello-json", frameHello, []byte(`{"jobId":"j1","entrypoint":"main.py"}`)},
		{"stdin-raw", frameStdin, []byte("some stdin bytes\n")},
		{"stdin-close-empty", frameStdinClose, nil},
		{"kill-empty", frameKill, nil},
		{"stdout-raw", frameStdout, []byte("hello stdout")},
		{"stderr-raw", frameStderr, []byte("err\x00\x01\x02 binary")},
		{"cpu-json", frameCPU, []byte(`{"cpuMs":42}`)},
		{"exit-json", frameExit, []byte(`{"exitCode":0,"signal":null}`)},
		{"empty-payload", frameStdout, []byte{}},
		{"large-payload", frameStdout, bytes.Repeat([]byte("x"), 70000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			var mu sync.Mutex
			if err := writeFrame(&buf, &mu, tc.ftype, tc.payload); err != nil {
				t.Fatalf("writeFrame: %v", err)
			}
			dec := newFrameDecoder(&buf)
			gotType, gotPayload, err := dec.next()
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if gotType != tc.ftype {
				t.Errorf("type = %#x, want %#x", gotType, tc.ftype)
			}
			want := tc.payload
			if want == nil {
				want = []byte{}
			}
			if !bytes.Equal(gotPayload, want) {
				t.Errorf("payload mismatch: got %d bytes, want %d bytes", len(gotPayload), len(want))
			}
		})
	}
}

// TestFrameHeaderFormat asserts the exact on-the-wire header layout:
// [1 byte type][4 byte big-endian length], matching the agent's struct.pack(">BI").
func TestFrameHeaderFormat(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	payload := []byte("abcd")
	if err := writeFrame(&buf, &mu, frameStdout, payload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	raw := buf.Bytes()
	if len(raw) != 5+len(payload) {
		t.Fatalf("frame length = %d, want %d", len(raw), 5+len(payload))
	}
	if raw[0] != frameStdout {
		t.Errorf("type byte = %#x, want %#x", raw[0], frameStdout)
	}
	gotLen := binary.BigEndian.Uint32(raw[1:5])
	if gotLen != uint32(len(payload)) {
		t.Errorf("length prefix = %d, want %d", gotLen, len(payload))
	}
	if !bytes.Equal(raw[5:], payload) {
		t.Errorf("payload bytes mismatch")
	}
}

// TestDecoderMultipleFrames decodes several concatenated frames in order.
func TestDecoderMultipleFrames(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	_ = writeFrame(&buf, &mu, frameStdout, []byte("one"))
	_ = writeFrame(&buf, &mu, frameStderr, []byte("two"))
	_ = writeFrame(&buf, &mu, frameExit, []byte(`{"exitCode":7,"signal":null}`))

	dec := newFrameDecoder(&buf)
	expect := []struct {
		ftype   byte
		payload string
	}{
		{frameStdout, "one"},
		{frameStderr, "two"},
		{frameExit, `{"exitCode":7,"signal":null}`},
	}
	for i, e := range expect {
		ft, pl, err := dec.next()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if ft != e.ftype || string(pl) != e.payload {
			t.Errorf("frame %d = (%#x,%q), want (%#x,%q)", i, ft, pl, e.ftype, e.payload)
		}
	}
	if _, _, err := dec.next(); err != io.EOF {
		t.Errorf("after last frame: err = %v, want io.EOF", err)
	}
}

// chunkReader feeds bytes one at a time to simulate partial reads on the wire.
type chunkReader struct {
	data []byte
	pos  int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	p[0] = c.data[c.pos]
	c.pos++
	return 1, nil
}

// TestDecoderPartialReads proves the decoder reassembles a frame delivered one
// byte at a time (io.ReadFull handles the partial reads).
func TestDecoderPartialReads(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	payload := []byte("partial-frame-payload-spanning-many-reads")
	_ = writeFrame(&buf, &mu, frameStdout, payload)

	dec := newFrameDecoder(&chunkReader{data: buf.Bytes()})
	ft, pl, err := dec.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if ft != frameStdout || !bytes.Equal(pl, payload) {
		t.Errorf("reassembled frame mismatch")
	}
}

// TestDecoderTruncatedMidFrame surfaces a mid-frame connection close as
// io.ErrUnexpectedEOF (a protocol truncation), not a clean io.EOF.
func TestDecoderTruncatedMidFrame(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	_ = writeFrame(&buf, &mu, frameStdout, []byte("complete payload"))
	raw := buf.Bytes()
	// Cut the buffer mid-payload (keep header + 3 payload bytes).
	truncated := raw[:5+3]

	dec := newFrameDecoder(bytes.NewReader(truncated))
	_, _, err := dec.next()
	if err != io.ErrUnexpectedEOF {
		t.Errorf("truncated mid-frame: err = %v, want io.ErrUnexpectedEOF", err)
	}
}

// TestDecoderOversizeLengthRejected rejects a frame whose declared length
// exceeds the cap, guarding against a hostile/corrupt length prefix.
func TestDecoderOversizeLengthRejected(t *testing.T) {
	var hdr [5]byte
	hdr[0] = frameStdout
	binary.BigEndian.PutUint32(hdr[1:], maxFramePayload+1)
	dec := newFrameDecoder(bytes.NewReader(hdr[:]))
	if _, _, err := dec.next(); err == nil {
		t.Errorf("oversize length: err = nil, want non-nil")
	}
}

// ── End-to-end relayConn behaviour over an in-memory net.Pipe ──────────────────

// pipePair returns a relayConn whose conn is one end of net.Pipe, plus the other
// end (the "agent" side) for the test to drive.
func newTestRelay(t *testing.T) (*relayConn, net.Conn) {
	t.Helper()
	workerEnd, agentEnd := net.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	rc := &relayConn{
		conn:    workerEnd,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
		exitCh:  make(chan relayExit, 1),
	}
	return rc, agentEnd
}

// TestRelayHandshakeAndDemux drives the agent side of a full session: HELLO is
// read, STARTED sent, then STDOUT/STDERR/CPU/EXIT, and asserts the worker side
// demuxes outputs, records CPU, and resolves the exit.
func TestRelayHandshakeAndDemux(t *testing.T) {
	rc, agent := newTestRelay(t)
	var amu sync.Mutex

	// Worker: send HELLO.
	helloDone := make(chan error, 1)
	go func() {
		helloDone <- rc.sendHello(helloPayload{JobID: "j1", Entrypoint: "main.py"})
	}()

	// Agent: read the HELLO frame.
	agentDec := newFrameDecoder(agent)
	ft, payload, err := agentDec.next()
	if err != nil || ft != frameHello {
		t.Fatalf("agent read HELLO: ft=%#x err=%v", ft, err)
	}
	var h helloPayload
	if err := json.Unmarshal(payload, &h); err != nil || h.JobID != "j1" {
		t.Fatalf("HELLO decode: %v %+v", err, h)
	}
	if err := <-helloDone; err != nil {
		t.Fatalf("sendHello: %v", err)
	}

	// Agent: send STARTED (in a goroutine — unbuffered pipe blocks until the
	// worker's readStarted below consumes it).
	startedBody, _ := json.Marshal(startedPayload{RealPID: 4242})
	go func() { _ = writeFrame(agent, &amu, frameStarted, startedBody) }()

	// Worker: read STARTED, then start the demux reader.
	dec := newFrameDecoder(rc.conn)
	realpid, _, err := rc.readStarted(dec)
	if err != nil || realpid != 4242 {
		t.Fatalf("readStarted: pid=%d err=%v", realpid, err)
	}
	rc.startReader(dec)

	// Agent: emit stdout, stderr, cpu, then exit — in a goroutine because the
	// net.Pipe is unbuffered (writes block until the worker reader consumes them,
	// and the worker reads happen below in this goroutine).
	go func() {
		_ = writeFrame(agent, &amu, frameStdout, []byte("hello world\n"))
		_ = writeFrame(agent, &amu, frameStderr, []byte("a warning\n"))
		cpuBody, _ := json.Marshal(cpuPayload{CPUMs: 123})
		_ = writeFrame(agent, &amu, frameCPU, cpuBody)
		exitCode := 0
		exitBody, _ := json.Marshal(exitPayload{ExitCode: &exitCode})
		_ = writeFrame(agent, &amu, frameExit, exitBody)
	}()

	// Worker: read demuxed stdout/stderr.
	gotOut := readN(t, rc.stdoutR, len("hello world\n"))
	if gotOut != "hello world\n" {
		t.Errorf("stdout = %q", gotOut)
	}
	gotErr := readN(t, rc.stderrR, len("a warning\n"))
	if gotErr != "a warning\n" {
		t.Errorf("stderr = %q", gotErr)
	}

	// Worker: exit resolved.
	select {
	case e := <-rc.exitCh:
		if e.err != nil {
			t.Fatalf("exit err = %v", e.err)
		}
		if e.exitCode == nil || *e.exitCode != 0 {
			t.Errorf("exitCode = %v, want 0", e.exitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exit")
	}

	// CPU recorded (the final EXIT path may race the cpu frame; allow either the
	// 123 sample or a later one — must be > 0).
	if cpu := rc.latestCPUMs.Load(); cpu < 123 {
		t.Errorf("latestCPUMs = %d, want >= 123", cpu)
	}
}

// TestRelayConnCloseBeforeExit asserts that a connection that closes WITHOUT an
// EXIT frame resolves the exit channel with an error (so the slot is released,
// no leak) and unblocks the output readers.
func TestRelayConnCloseBeforeExit(t *testing.T) {
	rc, agent := newTestRelay(t)
	var amu sync.Mutex

	// Send STARTED then close the agent side abruptly.
	startedBody, _ := json.Marshal(startedPayload{RealPID: 1})
	go func() {
		_ = writeFrame(agent, &amu, frameStarted, startedBody)
	}()
	dec := newFrameDecoder(rc.conn)
	if _, _, err := rc.readStarted(dec); err != nil {
		t.Fatalf("readStarted: %v", err)
	}
	rc.startReader(dec)

	// Abrupt close (no EXIT frame).
	_ = agent.Close()

	select {
	case e := <-rc.exitCh:
		if e.err == nil {
			t.Errorf("expected error on conn close before EXIT, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exit error")
	}

	// Stdout reader unblocks with an error (not a hang).
	buf := make([]byte, 8)
	if _, err := rc.stdoutR.Read(buf); err == nil {
		t.Errorf("stdout read after close: err = nil, want non-nil")
	}
}

// TestRelayStdinWriter asserts the stdin WriteCloser emits STDIN frames and a
// single STDIN_CLOSE on Close (idempotent).
func TestRelayStdinWriter(t *testing.T) {
	rc, agent := newTestRelay(t)
	w := &relayStdin{rc: rc}
	agentDec := newFrameDecoder(agent)

	go func() {
		_, _ = w.Write([]byte("line1\n"))
		_ = w.Close()
		_ = w.Close() // second close must not emit another frame
	}()

	// First frame: STDIN with the bytes.
	ft, pl, err := agentDec.next()
	if err != nil || ft != frameStdin || string(pl) != "line1\n" {
		t.Fatalf("STDIN frame: ft=%#x pl=%q err=%v", ft, pl, err)
	}
	// Second frame: STDIN_CLOSE, empty.
	ft, pl, err = agentDec.next()
	if err != nil || ft != frameStdinClose || len(pl) != 0 {
		t.Fatalf("STDIN_CLOSE frame: ft=%#x pl=%q err=%v", ft, pl, err)
	}

	// Writes after close fail.
	if _, err := w.Write([]byte("x")); err == nil {
		t.Errorf("write after close: err = nil, want non-nil")
	}
}

// TestRelayReadStartedEarlyExit surfaces an EXIT-before-STARTED (spawn failure)
// as an error from readStarted.
func TestRelayReadStartedEarlyExit(t *testing.T) {
	rc, agent := newTestRelay(t)
	var amu sync.Mutex
	go func() {
		body, _ := json.Marshal(exitPayload{Error: "spawn failed"})
		_ = writeFrame(agent, &amu, frameExit, body)
	}()
	dec := newFrameDecoder(rc.conn)
	if _, _, err := rc.readStarted(dec); err == nil {
		t.Errorf("readStarted after early EXIT: err = nil, want non-nil")
	}
}

// artifactFramePayload builds an ARTIFACT frame payload: [4 BE nameLen][name][data].
func artifactFramePayload(name string, data []byte) []byte {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(name)))
	out := append([]byte{}, hdr[:]...)
	out = append(out, []byte(name)...)
	out = append(out, data...)
	return out
}

// TestDecodeArtifactFrame covers the ARTIFACT payload parse: a well-formed frame
// recovers name + data; malformed frames (too short / lying name length) error.
func TestDecodeArtifactFrame(t *testing.T) {
	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01}
	a, err := decodeArtifactFrame(artifactFramePayload("out/plot.png", data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.Name != "out/plot.png" {
		t.Errorf("name = %q, want out/plot.png", a.Name)
	}
	if !bytes.Equal(a.Data, data) {
		t.Errorf("data mismatch: got %v", a.Data)
	}
	if a.MimeType != "" {
		t.Errorf("MimeType must be empty (filled in ReadArtifacts), got %q", a.MimeType)
	}

	// Empty-data artifact is valid (a zero-byte file).
	if a0, err := decodeArtifactFrame(artifactFramePayload("empty.txt", nil)); err != nil || a0.Name != "empty.txt" || len(a0.Data) != 0 {
		t.Errorf("empty-data artifact: a=%+v err=%v", a0, err)
	}

	// Too short to hold the 4-byte name length.
	if _, err := decodeArtifactFrame([]byte{0x00, 0x01}); err == nil {
		t.Errorf("short frame: err = nil, want non-nil")
	}
	// Name length exceeds the remaining payload.
	bad := []byte{0x00, 0x00, 0x00, 0x10, 'a'} // claims 16-byte name, has 1
	if _, err := decodeArtifactFrame(bad); err == nil {
		t.Errorf("oversize name length: err = nil, want non-nil")
	}
}

// TestRelayArtifactAccumulationAndReadArtifacts drives the agent side emitting
// ARTIFACT frames then EXIT(artifactsTruncated=true), and asserts the worker
// accumulates them, that zygoteSandbox.ReadArtifacts applies the exclude set +
// marker filter + MIME tagging (Docker-tier parity), and that the truncation flag
// surfaces.
func TestRelayArtifactAccumulationAndReadArtifacts(t *testing.T) {
	rc, agent := newTestRelay(t)
	var amu sync.Mutex

	// STARTED, then start the demux reader.
	startedBody, _ := json.Marshal(startedPayload{RealPID: 7})
	go func() { _ = writeFrame(agent, &amu, frameStarted, startedBody) }()
	dec := newFrameDecoder(rc.conn)
	if _, _, err := rc.readStarted(dec); err != nil {
		t.Fatalf("readStarted: %v", err)
	}
	rc.startReader(dec)

	png := []byte{0x89, 0x50, 0x4e, 0x47}
	go func() {
		// plot.png is a real artifact; main.py is an input (excluded by the caller);
		// .compile_ready is the bridge marker (defensively excluded).
		_ = writeFrame(agent, &amu, frameArtifact, artifactFramePayload("plot.png", png))
		_ = writeFrame(agent, &amu, frameArtifact, artifactFramePayload("main.py", []byte("print(1)")))
		_ = writeFrame(agent, &amu, frameArtifact, artifactFramePayload(".compile_ready", []byte("x")))
		exitCode := 0
		exitBody, _ := json.Marshal(exitPayload{ExitCode: &exitCode, ArtifactsTruncated: true})
		_ = writeFrame(agent, &amu, frameExit, exitBody)
	}()

	// Block on EXIT — all prior ARTIFACT frames are processed in order first.
	select {
	case e := <-rc.exitCh:
		if e.err != nil {
			t.Fatalf("exit err = %v", e.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exit")
	}

	sb := &zygoteSandbox{rc: rc}
	arts, err := sb.ReadArtifacts(context.Background(), map[string]bool{"main.py": true})
	if err != nil {
		t.Fatalf("ReadArtifacts: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d artifacts, want 1 (plot.png; main.py + .compile_ready excluded): %+v", len(arts), arts)
	}
	if arts[0].Name != "plot.png" {
		t.Errorf("name = %q, want plot.png", arts[0].Name)
	}
	if arts[0].MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", arts[0].MimeType)
	}
	if !bytes.Equal(arts[0].Data, png) {
		t.Errorf("data mismatch")
	}
	if !sb.ArtifactsTruncated() {
		t.Errorf("ArtifactsTruncated = false, want true")
	}

	// takeArtifacts cleared the slice: a second read returns nothing.
	if again, _ := sb.ReadArtifacts(context.Background(), nil); len(again) != 0 {
		t.Errorf("second ReadArtifacts = %d, want 0 (drained once)", len(again))
	}
}

// readN reads exactly n bytes from r (test helper) with a timeout guard via the
// caller's overall test timeout.
func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("readN: %v", err)
	}
	return string(buf)
}

// silence unused-import guard for context in case future edits remove its use.
var _ = context.Background
