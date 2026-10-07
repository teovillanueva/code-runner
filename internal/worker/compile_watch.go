package worker

import "github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"

// compileCtrlWatch consumes the job's control channel while the compile step
// runs. The run-phase consumer (the stdin/ctrl goroutine in runJobFromSpec)
// only starts after the compile step returns, so without this a "kill" sent
// during a long compile would sit in the channel until the compiler finished.
//
// A kill calls onKill (which cancels the compile context). Every other message
// (stdin_close, a duplicate start) is held and handed back to the channel by
// Stop, in arrival order, so the run phase still sees it.
type compileCtrlWatch struct {
	ctrlCh  chan wire.ControlMessage
	stop    chan struct{}
	done    chan struct{}
	killed  bool
	pending []wire.ControlMessage
}

func watchCompileCtrl(ctrlCh chan wire.ControlMessage, onKill func()) *compileCtrlWatch {
	cw := &compileCtrlWatch{
		ctrlCh: ctrlCh,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(cw.done)
		for {
			select {
			case <-cw.stop:
				return
			case msg := <-ctrlCh:
				if msg.Type == wire.ControlTypeKill {
					if !cw.killed {
						cw.killed = true
						onKill()
					}
					continue
				}
				cw.pending = append(cw.pending, msg)
			}
		}
	}()
	return cw
}

// Stop ends the watch, puts the held messages back on the channel and reports
// whether a kill arrived while compiling. dropped counts held messages that did
// not fit back (the channel is buffered; a full channel already means the
// subscriber is dropping messages, so this is best-effort like it).
func (cw *compileCtrlWatch) Stop() (killed bool, dropped int) {
	close(cw.stop)
	<-cw.done // happens-before: killed/pending are safe to read below
	for _, msg := range cw.pending {
		select {
		case cw.ctrlCh <- msg:
		default:
			dropped++
		}
	}
	return cw.killed, dropped
}
