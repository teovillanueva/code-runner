package runner

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// Compile-time assertions: stubRunner must implement Runner; stubSandbox must
// implement Sandbox. If either interface changes these lines fail to compile,
// surfacing the mismatch immediately.
var _ Runner = (*stubRunner)(nil)
var _ Sandbox = (*stubSandbox)(nil)

// stubRunner is a no-op Runner that returns a stubSandbox from Create.
// It holds no resources and performs no I/O.
type stubRunner struct{}

// NewStub returns a Runner that creates in-memory no-op sandboxes. It is
// intended for unit tests and as the compile-time proof that the Runner/Sandbox
// seam is implementable without the moby/Docker SDK.
func NewStub() Runner {
	return &stubRunner{}
}

// Create returns a no-op sandbox. The spec is accepted but ignored.
func (r *stubRunner) Create(_ context.Context, _ wire.JobSpec) (Sandbox, error) {
	stdin := &syncBuffer{}
	return &stubSandbox{
		stdin:  stdin,
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}, nil
}

// stubSandbox is a no-op Sandbox. Pipes are backed by in-memory buffers.
// Kill is a no-op. Cleanup is idempotent via sync.Once.
type stubSandbox struct {
	stdin      *syncBuffer
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	cleanupOnce sync.Once
}

func (s *stubSandbox) Stdin() io.WriteCloser { return s.stdin }
func (s *stubSandbox) Stdout() io.Reader     { return s.stdout }
func (s *stubSandbox) Stderr() io.Reader     { return s.stderr }

// Wait returns immediately with a zero Result and no error.
func (s *stubSandbox) Wait(_ context.Context) (Result, error) {
	return Result{}, nil
}

// Kill is a no-op for the stub.
func (s *stubSandbox) Kill(_ context.Context) error {
	return nil
}

// Cleanup is idempotent. Multiple calls are safe.
func (s *stubSandbox) Cleanup() error {
	s.cleanupOnce.Do(func() {
		// No resources to release in the stub.
	})
	return nil
}

// syncBuffer wraps bytes.Buffer with a mutex to satisfy io.WriteCloser safely.
type syncBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
}

func (sb *syncBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.closed {
		return 0, io.ErrClosedPipe
	}
	return sb.buf.Write(p)
}

func (sb *syncBuffer) Close() error {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.closed = true
	return nil
}
