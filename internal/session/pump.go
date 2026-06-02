// Package session provides the testable safety logic for sandboxed execution:
// the three independent clocks (wall, idle, CPU), the byte-capped output pump,
// and the single sync.Once idempotent teardown. It is intentionally decoupled
// from any Docker or container SDK so it can be unit-tested with a fake Sandbox.
package session

import (
	"io"
	"sync/atomic"
)

// Pump copies bytes from an io.Reader to a sink callback, enforcing a SHARED
// combined-byte budget (passed as *atomic.Int64 so multiple pumps can share one
// cap across stdout and stderr). Once the budget is exhausted the pump marks the
// shared truncated flag and continues reading-and-discarding the source to EOF
// so the upstream writer never blocks (anti-deadlock invariant, PITFALLS §6).
//
// An optional activity channel (non-nil) receives an empty struct on every chunk
// forwarded to the sink, allowing the idle clock to reset on output activity.
type Pump struct {
	r          io.Reader
	budget     *atomic.Int64 // shared across stdout+stderr pumps; counts down
	truncated  *atomic.Bool  // shared flag set when budget hits 0
	sink       func([]byte)  // called for each forwarded chunk (at most budget bytes total)
	activity   chan<- struct{} // optional; non-nil signals the idle clock on each chunk
}

// NewPump creates a Pump. The caller must call Run() to start pumping.
//
//   - r: the reader to drain (e.g. Sandbox.Stdout() or Sandbox.Stderr())
//   - budget: shared atomic counter initialised to the cap in bytes (OutputKb*1024);
//     decremented as bytes are forwarded; multiple Pump instances share one budget.
//   - truncated: shared flag; set to true when budget first reaches 0.
//   - sink: called with each forwarded chunk; must not block.
//   - activity: optional channel; a struct{}{} is sent (non-blocking) on every chunk
//     forwarded so the idle clock can reset. May be nil.
func NewPump(r io.Reader, budget *atomic.Int64, truncated *atomic.Bool, sink func([]byte), activity chan<- struct{}) *Pump {
	return &Pump{
		r:         r,
		budget:    budget,
		truncated: truncated,
		sink:      sink,
		activity:  activity,
	}
}

// Run blocks until the reader reaches EOF or returns an error. It is safe to
// call from a dedicated goroutine (the idiomatic usage is one goroutine per
// Sandbox output pipe).
//
// Invariants guaranteed by Run:
//  1. The reader is always read to completion (never abandoned mid-stream).
//  2. At most budget bytes are forwarded to the sink.
//  3. When budget is exceeded, truncated is set to true (exactly once; CAS is
//     used so concurrent pumps sharing the flag don't double-set).
//  4. The activity channel (if non-nil) is signalled for each forwarded chunk
//     using a non-blocking send so Run never stalls on a slow reader.
func (p *Pump) Run() error {
	buf := make([]byte, 32*1024) // 32 KiB read buffer

	for {
		n, readErr := p.r.Read(buf)
		if n > 0 {
			chunk := buf[:n]

			// How many of these bytes are still within budget?
			remaining := p.budget.Add(-int64(n)) + int64(n) // old value before decrement

			if remaining > 0 {
				// At least some bytes are within budget.
				forward := n
				if int64(forward) > remaining {
					forward = int(remaining)
					// Budget just crossed zero with this chunk.
					p.truncated.CompareAndSwap(false, true)
				}
				p.sink(chunk[:forward])
				p.signalActivity()

				// If budget ran out mid-chunk, mark truncated.
				if int64(forward) < int64(n) {
					p.truncated.CompareAndSwap(false, true)
				}
			} else {
				// Budget already exhausted before this read; mark truncated and
				// keep draining (don't forward anything).
				p.truncated.CompareAndSwap(false, true)
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

// signalActivity sends a non-blocking signal on the activity channel (if set).
func (p *Pump) signalActivity() {
	if p.activity == nil {
		return
	}
	select {
	case p.activity <- struct{}{}:
	default:
	}
}
