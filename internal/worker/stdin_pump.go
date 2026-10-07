package worker

import (
	"io"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// stdinPump is the run-phase consumer of a job's stdin and control channels:
// it writes stdin chunks into the sandbox and applies stdin_close and kill.
//
// Ordering: chunks and control messages travel on separate channels, and a
// select with both ready picks one at random. A batch client sends
// start → stdin → stdin_close back to back, so both channels are routinely
// ready at once — always for a compiled language, where all three land while
// compiling (the compile watch holds the close and hands it back). If the
// close won, the program got EOF before its input and the chunk was lost.
// So a stdin_close first drains every chunk already queued, in arrival order,
// and only then delivers EOF.
type stdinPump struct {
	stdinCh <-chan []byte
	ctrlCh  <-chan wire.ControlMessage
	// done is closed when the session ends; the pump returns.
	done <-chan struct{}
	// stdin is the sandbox's stdin. Writes block while the program does not
	// read and its pipe is full, exactly as before the drain existed; the
	// session's clocks end the process, which fails the write.
	stdin io.Writer
	// closeStdin delivers EOF (idempotent); kill kills the sandbox.
	closeStdin func()
	kill       func()
	// activity is signalled (non-blocking) on each chunk written, so the
	// session's idle clock counts interactive input.
	activity chan<- struct{}

	eof bool // EOF delivered; later chunks have nowhere to go
}

// run consumes both channels until done is closed or the sandbox's stdin is
// gone (the program exited).
func (p *stdinPump) run() {
	for {
		select {
		case <-p.done:
			return
		case chunk, ok := <-p.stdinCh:
			if !ok || !p.write(chunk) {
				return
			}
		case msg := <-p.ctrlCh:
			switch msg.Type {
			case wire.ControlTypeStdinClose:
				if !p.drain() {
					return
				}
				// Deliver EOF exactly once (STDIN-02).
				p.closeStdin()
				p.eof = true
			case wire.ControlTypeKill:
				// Kill the container; session.RunInteractive will return.
				p.kill()
			default:
				// Duplicate start or unknown — ignore.
			}
		}
	}
}

// drain writes every chunk already queued on stdinCh, in order, without
// waiting for more. It reports false when the pump must stop.
func (p *stdinPump) drain() bool {
	for {
		select {
		case <-p.done:
			return false
		default:
		}
		select {
		case chunk, ok := <-p.stdinCh:
			if !ok || !p.write(chunk) {
				return false
			}
		default:
			return true
		}
	}
}

// write forwards one chunk. It reports false when the sandbox's stdin is gone.
func (p *stdinPump) write(chunk []byte) bool {
	if p.eof {
		// A chunk published after stdin_close: EOF was already delivered, so
		// drop it rather than write to the closed pipe — that write would fail
		// and end the pump, and with it the handling of a later kill.
		return true
	}
	// Full-write loop — never a bare Write (PITFALLS §6 partial-write).
	if _, err := writeAll(p.stdin, chunk); err != nil {
		// Stdin pipe closed (process exited) — stop forwarding.
		return false
	}
	select {
	case p.activity <- struct{}{}:
	default:
	}
	return true
}
