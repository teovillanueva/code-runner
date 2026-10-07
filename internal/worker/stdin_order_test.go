// Package worker_test — stdin chunks published before stdin_close reach the
// program before the EOF.
//
// A batch client runs start → stdin → stdin_close back to back. For a compiled
// language all three land while the worker compiles: the chunk waits in the
// stdin channel and the stdin_close is held by the compile watch and handed
// back to the control channel. When the run phase starts, both channels are
// ready at once, and Go's select picks one at random: if it took the close
// first, the program saw EOF with no input and the chunk was lost.
//
// No Docker required; runs under plain `go test ./...`.
package worker_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// stdinCatSandbox is a compiled-language sandbox whose program reads its stdin
// until EOF and exits 0, reporting what it read on got.
type stdinCatSandbox struct {
	*slowCompileSandbox
	got chan string
}

func newStdinCatSandbox(compileDelay time.Duration) *stdinCatSandbox {
	return &stdinCatSandbox{
		slowCompileSandbox: newSlowCompileSandbox(compileDelay, 0),
		got:                make(chan string, 1),
	}
}

func (s *stdinCatSandbox) Wait(ctx context.Context) (runner.Result, error) {
	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(s.inner.stdinPR) // returns at EOF (stdin closed)
		read <- string(b)
	}()
	select {
	case in := <-read:
		s.got <- in
		return runner.Result{ExitCode: intPtr(0)}, nil
	case <-ctx.Done():
		_ = s.inner.stdinPR.Close()
		return runner.Result{}, ctx.Err()
	}
}

func TestWorker_Compile_StdinThenCloseWhileCompiling_InputReachesProgram(t *testing.T) {
	// Each round puts the chunk and the close in their channels before the run
	// phase starts. Without the fix every round is a coin toss, so 16 rounds
	// pass by luck with probability 2^-16.
	const rounds = 16
	for i := 0; i < rounds; i++ {
		sb := newStdinCatSandbox(40 * time.Millisecond)
		spec := compileSpec(fmt.Sprintf("compile-stdin-order-%03d", i), []string{"/usr/bin/compile-tool", "main.src"})

		_, inMem, done := startCompileJob(t, spec, sb)
		// Compiling now (start was just sent): the batch client's input and EOF.
		require.NoError(t, inMem.Publish(context.Background(), spec.JobId, []byte("5\n")))
		inMem.PublishControl(context.Background(), spec.JobId, wire.ControlMessage{Type: wire.ControlTypeStdinClose})
		waitJob(t, done, 3*time.Second)

		select {
		case got := <-sb.got:
			require.Equal(t, "5\n", got, "round %d: the chunk sent before stdin_close must reach the program before EOF", i)
		default:
			t.Fatalf("round %d: the program never ran to EOF", i)
		}
	}
}
