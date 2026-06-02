// Package stdintransport defines the StdinTransport interface that abstracts
// how stdin chunks are routed from the API to the owning worker sandbox.
//
// MVP implementation: Redis pub/sub on channel "stdin:<jobID>" (see
// packages/contract/src/index.ts stdinChannel convention). The channel name
// used by the pub/sub impl must match what the API publishes to.
//
// Planned upgrade (Redis Streams): swap the concrete impl for one that uses
// XADD / XREAD BLOCK + consumer groups for at-least-once, persistent, ordered
// delivery — without changing any caller that holds a StdinTransport. This swap
// is the sole reason the interface exists.
package stdintransport

import "context"

// StdinTransport routes stdin chunks between the API (publisher) and the
// worker sandbox pipe writer (subscriber). Callers are decoupled from the
// underlying transport mechanism (pub/sub vs. Streams).
//
// Channel-name convention (mirroring @code-runner/contract):
//
//	stdin:<jobID>  — carries StdinMessage chunks to the owning worker
//	ctrl:<jobID>   — carries ControlMessage (kill/start/stdin_close) — NOT this transport
type StdinTransport interface {
	// Publish sends a stdin chunk to all active subscribers for jobID. In the
	// Redis pub/sub impl this maps to PUBLISH stdin:<jobID> chunk. In the
	// Streams impl this maps to XADD stdin:<jobID> MAXLEN ~ <cap>.
	//
	// Publish is fire-and-forget in the pub/sub variant: if no subscriber is
	// active at call time the chunk is silently dropped. The start-handshake
	// (queued → subscribe → start) guarantees a subscriber exists before the
	// API can receive stdin, making this safe for MVP.
	Publish(ctx context.Context, jobID string, chunk []byte) error

	// Subscribe registers handler to receive stdin chunks for jobID. The
	// returned Subscription is live until Close() is called.
	//
	// The handler is called synchronously in the delivery goroutine; it must
	// not block or it will stall subsequent chunks. Callers that need async
	// processing must buffer internally.
	Subscribe(ctx context.Context, jobID string, handler func(chunk []byte)) (Subscription, error)
}

// Subscription represents a live stdin subscription. Callers must call Close
// when the sandbox terminates to release the underlying transport resources
// (Redis connection, in-memory handler entry, etc.).
type Subscription interface {
	// Close unregisters the handler and releases resources. It is safe to
	// call multiple times; subsequent calls are no-ops.
	Close() error
}
