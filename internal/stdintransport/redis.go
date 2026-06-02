package stdintransport

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/redis/go-redis/v9"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// Compile-time assertions: RedisTransport must implement StdinTransport;
// redisSubscription must implement Subscription. Mirror stub.go.
var _ StdinTransport = (*RedisTransport)(nil)
var _ Subscription = (*redisSubscription)(nil)

// RedisTransport implements StdinTransport using Redis pub/sub. Publish maps
// to PUBLISH stdin:<jobId>; Subscribe maps to SUBSCRIBE stdin:<jobId>.
//
// Thread safety: RedisTransport is safe for concurrent use. Each Subscribe
// call creates an independent *redis.PubSub connection from the provided
// client, so subscriptions do not share state.
//
// Handler contract: handlers passed to Subscribe and SubscribeControl are
// called synchronously in the delivery goroutine. They must not block; callers
// that need async processing must buffer internally (see transport.go).
type RedisTransport struct {
	client *redis.Client
}

// NewRedis returns a *RedisTransport backed by the provided *redis.Client.
// The client must point to a native Redis instance (CFG-04); Upstash REST
// is not supported.
func NewRedis(client *redis.Client) *RedisTransport {
	return &RedisTransport{client: client}
}

// Publish sends chunk to all active subscribers on keys.StdinChannel(jobID).
// Maps to PUBLISH stdin:<jobId> chunk. Fire-and-forget: returns nil even when
// zero subscribers are active, matching pub/sub semantics and PITFALLS §7.
func (t *RedisTransport) Publish(ctx context.Context, jobID string, chunk []byte) error {
	return t.client.Publish(ctx, keys.StdinChannel(jobID), chunk).Err()
}

// Subscribe registers handler to receive stdin chunks for jobID by
// SUBSCRIBEing keys.StdinChannel(jobID). A goroutine reads the channel and
// invokes handler(chunk) per message. The returned Subscription's Close()
// unsubscribes and stops the goroutine; Close is idempotent (sync.Once).
func (t *RedisTransport) Subscribe(ctx context.Context, jobID string, handler func(chunk []byte)) (Subscription, error) {
	pubsub := t.client.Subscribe(ctx, keys.StdinChannel(jobID))

	sub := &redisSubscription{
		pubsub:  pubsub,
		handler: func(payload string) { handler([]byte(payload)) },
	}
	sub.start(ctx)
	return sub, nil
}

// SubscribeControl subscribes to keys.ControlChannel(jobID) (ctrl:<jobId>)
// and invokes handler with each JSON-decoded wire.ControlMessage. This is how
// the worker receives start/kill/stdin_close signals (STDIN-03).
//
// SubscribeControl is a RedisTransport-specific method (NOT on StdinTransport)
// because the control channel carries structured ControlMessage payloads rather
// than raw bytes.
//
// The handler must not block; malformed JSON payloads are silently dropped.
func (t *RedisTransport) SubscribeControl(ctx context.Context, jobID string, handler func(wire.ControlMessage)) (Subscription, error) {
	pubsub := t.client.Subscribe(ctx, keys.ControlChannel(jobID))

	sub := &redisSubscription{
		pubsub: pubsub,
		handler: func(payload string) {
			var msg wire.ControlMessage
			if err := json.Unmarshal([]byte(payload), &msg); err != nil {
				// Malformed payload — drop silently. Logging deferred to the
				// worker run loop layer (Plan 02).
				return
			}
			handler(msg)
		},
	}
	sub.start(ctx)
	return sub, nil
}

// redisSubscription is a live pub/sub subscription managed by a background
// goroutine. Close is idempotent via sync.Once.
type redisSubscription struct {
	pubsub    *redis.PubSub
	handler   func(payload string)
	closeOnce sync.Once
	cancel    context.CancelFunc
}

// start spawns the background goroutine that reads from pubsub.Channel() and
// invokes sub.handler. The goroutine exits when sub.cancel() is called or the
// pubsub channel is closed.
func (s *redisSubscription) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel

	ch := s.pubsub.Channel()
	go func() {
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				s.handler(msg.Payload)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Close unregisters the subscription and stops the delivery goroutine. Safe
// to call multiple times; subsequent calls are no-ops (sync.Once).
func (s *redisSubscription) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		// Cancel the goroutine's context first so it exits its select loop.
		if s.cancel != nil {
			s.cancel()
		}
		// Close the PubSub connection, which also closes the channel.
		closeErr = s.pubsub.Close()
	})
	return closeErr
}
