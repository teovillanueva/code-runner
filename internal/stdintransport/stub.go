package stdintransport

import (
	"context"
	"sync"
)

// Compile-time assertions: stubTransport must implement StdinTransport;
// stubSubscription must implement Subscription.
var _ StdinTransport = (*stubTransport)(nil)
var _ Subscription = (*stubSubscription)(nil)

// stubTransport is an in-memory StdinTransport for unit tests. It routes
// Publish calls to all registered handlers for the same jobID without any
// network I/O. No Redis client dependency is introduced in Phase 1.
type stubTransport struct {
	mu       sync.Mutex
	handlers map[string][]*stubSubscription
}

// NewStub returns a StdinTransport backed by in-memory maps. Suitable for
// unit tests and as the compile-time proof that the interface seam holds
// without a Redis client dependency.
func NewStub() StdinTransport {
	return &stubTransport{
		handlers: make(map[string][]*stubSubscription),
	}
}

// Publish delivers chunk to every active subscriber registered for jobID.
func (t *stubTransport) Publish(_ context.Context, jobID string, chunk []byte) error {
	t.mu.Lock()
	subs := make([]*stubSubscription, len(t.handlers[jobID]))
	copy(subs, t.handlers[jobID])
	t.mu.Unlock()

	for _, s := range subs {
		s.deliver(chunk)
	}
	return nil
}

// Subscribe registers handler for jobID. Returns a Subscription whose Close
// unregisters the handler.
func (t *stubTransport) Subscribe(_ context.Context, jobID string, handler func(chunk []byte)) (Subscription, error) {
	sub := &stubSubscription{
		transport: t,
		jobID:     jobID,
		handler:   handler,
	}

	t.mu.Lock()
	t.handlers[jobID] = append(t.handlers[jobID], sub)
	t.mu.Unlock()

	return sub, nil
}

// remove deregisters sub from t.handlers[jobID].
func (t *stubTransport) remove(jobID string, sub *stubSubscription) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.handlers[jobID]
	for i, s := range list {
		if s == sub {
			t.handlers[jobID] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// stubSubscription is a live handler registration. Close is idempotent.
type stubSubscription struct {
	transport *stubTransport
	jobID     string
	handler   func(chunk []byte)
	closeOnce sync.Once
	closed    bool
	mu        sync.Mutex
}

// deliver calls the handler if the subscription is still open.
func (s *stubSubscription) deliver(chunk []byte) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	s.handler(chunk)
}

// Close unregisters the handler. Safe to call multiple times.
func (s *stubSubscription) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.transport.remove(s.jobID, s)
	})
	return nil
}
