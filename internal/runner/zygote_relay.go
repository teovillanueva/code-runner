// Package runner — zygote relay client.
//
// This file implements the Go side of the worker↔agent framed relay protocol
// (see .planning/decisions/ZYGOTE-PRODUCTION-DESIGN.md "Worker ↔ agent relay
// protocol" and languages/python-3.12/zygote_agent.py). It MUST match the
// agent's wire format byte-for-byte:
//
//	Frame = [1 byte type][4 byte big-endian length][payload]
//
//	Worker → agent: HELLO=0x01 STDIN=0x02 STDIN_CLOSE=0x03 KILL=0x04
//	agent → worker: STARTED=0x10 STDOUT=0x11 STDERR=0x12 CPU=0x13 EXIT=0x14
//
// A single reader goroutine demuxes STDOUT/STDERR into two io.Pipes (mirroring
// how dockerSandbox demuxes Docker's stdcopy framing), captures the latest CPU
// value, and resolves Wait on the terminal EXIT frame. Stdin() returns an
// io.WriteCloser that emits STDIN frames and a final STDIN_CLOSE on Close.
package runner

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

// Frame type bytes — must match zygote_agent.py exactly.
const (
	// Worker → agent.
	frameHello      byte = 0x01
	frameStdin      byte = 0x02
	frameStdinClose byte = 0x03
	frameKill       byte = 0x04
	// agent → worker.
	frameStarted byte = 0x10
	frameStdout  byte = 0x11
	frameStderr  byte = 0x12
	frameCPU     byte = 0x13
	frameExit    byte = 0x14
)

// frameHeaderLen is the fixed prefix length: 1 type byte + 4 length bytes.
const frameHeaderLen = 5

// maxFramePayload caps a single inbound frame's declared length to guard against
// a corrupt/hostile length prefix causing an unbounded allocation. The agent
// never sends a frame larger than its 64 KiB recv chunks plus JSON, so 16 MiB is
// generous headroom while still bounded.
const maxFramePayload = 16 * 1024 * 1024

// helloPayload is the JSON body of the HELLO frame. Field names match the keys
// the agent reads in handle_job (jobId, entrypoint, files, uid, memMaxBytes,
// pidsMax, tmpfsBytes).
type helloPayload struct {
	JobID       string      `json:"jobId"`
	Entrypoint  string      `json:"entrypoint"`
	Files       []helloFile `json:"files"`
	UID         int         `json:"uid"`
	MemMaxBytes int64       `json:"memMaxBytes"`
	PidsMax     int         `json:"pidsMax"`
	TmpfsBytes  int64       `json:"tmpfsBytes"`
}

// helloFile mirrors the agent's {name, content} file shape.
type helloFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// startedPayload is the JSON body of the STARTED frame ({realpid}).
type startedPayload struct {
	RealPID int `json:"realpid"`
}

// cpuPayload is the JSON body of a CPU frame ({cpuMs}).
type cpuPayload struct {
	CPUMs int `json:"cpuMs"`
}

// exitPayload is the JSON body of the terminal EXIT frame. exitCode/signal are
// pointers because the agent sends JSON null for whichever does not apply; an
// optional error string is set when the agent fails after STARTED.
type exitPayload struct {
	ExitCode *int   `json:"exitCode"`
	Signal   *int   `json:"signal"`
	Error    string `json:"error,omitempty"`
}

// writeFrame encodes one frame onto w under mu (sendall semantics). Concurrent
// writers (stdin pump, KILL, the HELLO send) are serialized so frames never
// interleave on the wire.
func writeFrame(w io.Writer, mu *sync.Mutex, ftype byte, payload []byte) error {
	if len(payload) > maxFramePayload {
		return fmt.Errorf("zygote: outbound frame payload too large: %d", len(payload))
	}
	var hdr [frameHeaderLen]byte
	hdr[0] = ftype
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))

	mu.Lock()
	defer mu.Unlock()
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// frameDecoder reads complete frames from an io.Reader. It handles partial
// reads transparently: io.ReadFull blocks until the full header / payload is
// available, and a mid-frame conn close surfaces as io.ErrUnexpectedEOF (or
// io.EOF on a clean boundary).
type frameDecoder struct {
	r io.Reader
}

func newFrameDecoder(r io.Reader) *frameDecoder { return &frameDecoder{r: r} }

// next returns the next frame's type and payload, or an error. io.EOF is
// returned (clean) only when the stream ends exactly on a frame boundary.
func (d *frameDecoder) next() (byte, []byte, error) {
	var hdr [frameHeaderLen]byte
	if _, err := io.ReadFull(d.r, hdr[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(hdr[1:])
	if length > maxFramePayload {
		return 0, nil, fmt.Errorf("zygote: inbound frame length %d exceeds cap %d", length, maxFramePayload)
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(d.r, payload); err != nil {
			// A mid-frame close is a protocol-level truncation, not a clean EOF.
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return 0, nil, err
		}
	}
	return hdr[0], payload, nil
}

// relayConn wraps one TCP connection for one job's full session. It owns the
// reader goroutine that demuxes agent→worker frames into the two output pipes,
// the latest CPU value, and the terminal exit result.
type relayConn struct {
	conn net.Conn

	// writeMu serializes all outbound frames on conn.
	writeMu sync.Mutex

	// Demuxed output streams handed to the sandbox as Stdout()/Stderr().
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter

	// latestCPUMs holds the most recent cumulative cpuMs pushed by the agent.
	latestCPUMs atomic.Int64

	// exit carries the terminal EXIT result; closed by the reader goroutine.
	exitOnce sync.Once
	exitCh   chan relayExit
}

// relayExit is the terminal outcome surfaced from the reader goroutine: the
// exit code/signal from an EXIT frame, or err set when the connection broke
// before a clean EXIT (→ surfaced as an execution failure, not a leak).
type relayExit struct {
	exitCode *int
	signal   *int
	err      error
}

// dialRelay connects to the agent at addr (host:port). The caller then sends
// HELLO via sendHello and waits for STARTED via readStarted before starting the
// demux loop (startReader). The two-phase setup mirrors the agent's accept →
// HELLO → fork → STARTED handshake.
func dialRelay(dialer func(string) (net.Conn, error), addr string) (*relayConn, error) {
	conn, err := dialer(addr)
	if err != nil {
		return nil, fmt.Errorf("zygote: dial agent %s: %w", addr, err)
	}
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	return &relayConn{
		conn:    conn,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
		exitCh:  make(chan relayExit, 1),
	}, nil
}

// sendHello writes the first frame. Must be called before startReader.
func (rc *relayConn) sendHello(h helloPayload) error {
	body, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("zygote: marshal HELLO: %w", err)
	}
	return writeFrame(rc.conn, &rc.writeMu, frameHello, body)
}

// readStarted reads frames until the STARTED frame is seen, returning the
// session's real (root-ns) pid. Any STDOUT/STDERR/CPU before STARTED would be
// out of protocol; an early EXIT means spawn failed. It uses a dedicated
// decoder which is then handed to startReader so no frame is lost between the
// handshake and the demux loop.
func (rc *relayConn) readStarted(dec *frameDecoder) (int, error) {
	for {
		ftype, payload, err := dec.next()
		if err != nil {
			return 0, fmt.Errorf("zygote: read STARTED: %w", err)
		}
		switch ftype {
		case frameStarted:
			var s startedPayload
			if err := json.Unmarshal(payload, &s); err != nil {
				return 0, fmt.Errorf("zygote: decode STARTED: %w", err)
			}
			return s.RealPID, nil
		case frameExit:
			var e exitPayload
			_ = json.Unmarshal(payload, &e)
			if e.Error != "" {
				return 0, fmt.Errorf("zygote: job failed before start: %s", e.Error)
			}
			return 0, errors.New("zygote: agent exited before STARTED")
		default:
			// Ignore any stray pre-STARTED frame (defensive; agent does not send
			// output before STARTED).
		}
	}
}

// startReader launches the single demux goroutine. It consumes agent→worker
// frames from dec, writing STDOUT/STDERR into the pipes, recording CPU, and
// resolving the exit channel on EXIT or connection error. The same decoder used
// for the handshake is passed in so its internal buffer state carries over.
func (rc *relayConn) startReader(dec *frameDecoder) {
	go func() {
		for {
			ftype, payload, err := dec.next()
			if err != nil {
				// Connection broke (or closed) before a clean EXIT. Surface as an
				// execution error so the slot is released cleanly — no leak.
				rc.closeOutputs(err)
				rc.resolveExit(relayExit{err: rc.normalizeConnErr(err)})
				return
			}
			switch ftype {
			case frameStdout:
				if len(payload) > 0 {
					_, _ = rc.stdoutW.Write(payload)
				}
			case frameStderr:
				if len(payload) > 0 {
					_, _ = rc.stderrW.Write(payload)
				}
			case frameCPU:
				var c cpuPayload
				if json.Unmarshal(payload, &c) == nil {
					rc.latestCPUMs.Store(int64(c.CPUMs))
				}
			case frameExit:
				var e exitPayload
				_ = json.Unmarshal(payload, &e)
				rc.closeOutputs(io.EOF)
				if e.Error != "" {
					rc.resolveExit(relayExit{
						exitCode: e.ExitCode,
						signal:   e.Signal,
						err:      fmt.Errorf("zygote: agent reported error: %s", e.Error),
					})
				} else {
					rc.resolveExit(relayExit{exitCode: e.ExitCode, signal: e.Signal})
				}
				return
			default:
				// Unknown frame type — ignore (forward-compat).
			}
		}
	}()
}

// normalizeConnErr maps a clean EOF on the reader into a terminal error: the
// agent ALWAYS sends an EXIT frame before closing, so a bare EOF without EXIT
// means the connection (or pool container) died mid-job.
func (rc *relayConn) normalizeConnErr(err error) error {
	if errors.Is(err, io.EOF) {
		return errors.New("zygote: connection closed before EXIT frame (agent/pool died)")
	}
	return fmt.Errorf("zygote: relay read: %w", err)
}

// closeOutputs closes both demuxed pipe writers exactly via CloseWithError so
// any reader blocked on Stdout()/Stderr() unblocks with the right terminal
// error (nil → io.EOF).
func (rc *relayConn) closeOutputs(err error) {
	if err == io.EOF {
		err = nil
	}
	_ = rc.stdoutW.CloseWithError(err)
	_ = rc.stderrW.CloseWithError(err)
}

// resolveExit delivers the terminal result exactly once.
func (rc *relayConn) resolveExit(e relayExit) {
	rc.exitOnce.Do(func() {
		rc.exitCh <- e
		close(rc.exitCh)
	})
}

// sendStdin emits a STDIN frame.
func (rc *relayConn) sendStdin(p []byte) error {
	return writeFrame(rc.conn, &rc.writeMu, frameStdin, p)
}

// sendStdinClose emits a STDIN_CLOSE frame (deliver EOF to the child).
func (rc *relayConn) sendStdinClose() error {
	return writeFrame(rc.conn, &rc.writeMu, frameStdinClose, nil)
}

// sendKill emits a KILL frame (agent cgroup.kills the whole child tree).
func (rc *relayConn) sendKill() error {
	return writeFrame(rc.conn, &rc.writeMu, frameKill, nil)
}

// close tears down the connection. Closing the conn also signals an implicit
// KILL to the agent (it treats conn-close as KILL).
func (rc *relayConn) close() error {
	return rc.conn.Close()
}

// ── stdin writer ──────────────────────────────────────────────────────────────

// relayStdin is the io.WriteCloser handed to callers via Sandbox.Stdin(). Each
// Write emits a STDIN frame; Close emits a single STDIN_CLOSE frame (idempotent).
type relayStdin struct {
	rc        *relayConn
	closeOnce sync.Once
	closed    atomic.Bool
}

func (w *relayStdin) Write(p []byte) (int, error) {
	if w.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.rc.sendStdin(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *relayStdin) Close() error {
	var err error
	w.closeOnce.Do(func() {
		w.closed.Store(true)
		err = w.rc.sendStdinClose()
	})
	return err
}
