package stdintransport_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/teovillanueva/code-runner/internal/redisx"
	"github.com/teovillanueva/code-runner/internal/stdintransport"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// dialOrSkip returns a live Redis client or skips the test. Implements the
// two-gate pattern: (1) parse URL, (2) Ping. Skips cleanly without infra.
func dialOrSkip(t *testing.T) *redis.Client {
	t.Helper()

	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6379"
	}

	client, err := redisx.NewFromURL(rawURL)
	if err != nil {
		t.Skipf("dialOrSkip: could not parse TEST_REDIS_URL %q: %v (skipping live Redis tests)", rawURL, err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("dialOrSkip: Redis unreachable at %q: %v (skipping live Redis tests)", rawURL, err)
		return nil
	}

	return client
}

// TestRedisTransport_RoundTrip verifies that a chunk published via
// RedisTransport.Publish is delivered to a handler registered via Subscribe.
func TestRedisTransport_RoundTrip(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	tr := stdintransport.NewRedis(client)
	ctx := context.Background()

	received := make(chan []byte, 1)
	sub, err := tr.Subscribe(ctx, "job-redis-1", func(chunk []byte) {
		cp := make([]byte, len(chunk))
		copy(cp, chunk)
		received <- cp
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	// Brief wait for the subscription to be established before publishing.
	time.Sleep(50 * time.Millisecond)

	want := []byte("hello redis stdin\n")
	if err := tr.Publish(ctx, "job-redis-1", want); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case got := <-received:
		if string(got) != string(want) {
			t.Errorf("handler received %q; want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler was not called within 3s")
	}
}

// TestRedisTransport_CloseStopsDelivery verifies that Close() on the
// Subscription prevents further handler invocations.
func TestRedisTransport_CloseStopsDelivery(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	tr := stdintransport.NewRedis(client)
	ctx := context.Background()

	calls := make(chan struct{}, 10)
	sub, err := tr.Subscribe(ctx, "job-redis-2", func(_ []byte) {
		calls <- struct{}{}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if err := tr.Publish(ctx, "job-redis-2", []byte("before-close")); err != nil {
		t.Fatalf("Publish before close: %v", err)
	}

	select {
	case <-calls:
		// good — first chunk delivered
	case <-time.After(3 * time.Second):
		t.Fatal("first chunk not delivered within 3s")
	}

	if err := sub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Allow the goroutine to drain and close.
	time.Sleep(100 * time.Millisecond)

	// A second publish must NOT reach the handler.
	if err := tr.Publish(ctx, "job-redis-2", []byte("after-close")); err != nil {
		t.Fatalf("Publish after close: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if len(calls) != 0 {
		t.Errorf("handler called after Close; extra calls = %d", len(calls))
	}
}

// TestRedisTransport_CloseIdempotent verifies that calling Close multiple
// times does not panic or error.
func TestRedisTransport_CloseIdempotent(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	tr := stdintransport.NewRedis(client)
	sub, err := tr.Subscribe(context.Background(), "job-redis-3", func(_ []byte) {})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	for i := range 5 {
		if err := sub.Close(); err != nil {
			t.Fatalf("Close call %d returned error: %v", i+1, err)
		}
	}
}

// TestRedisTransport_PublishNoSubscriber verifies that publishing to a job
// with no active subscriber does not error (fire-and-forget semantics).
func TestRedisTransport_PublishNoSubscriber(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	tr := stdintransport.NewRedis(client)
	if err := tr.Publish(context.Background(), "no-subscriber-job", []byte("data")); err != nil {
		t.Fatalf("Publish to unsubscribed job returned error: %v", err)
	}
}

// TestRedisTransport_SubscribeControl verifies the SubscribeControl method
// decodes a wire.ControlMessage published on ctrl:<jobId>.
func TestRedisTransport_SubscribeControl(t *testing.T) {
	client := dialOrSkip(t)
	defer client.Close() //nolint:errcheck

	tr := stdintransport.NewRedis(client)
	ctx := context.Background()

	received := make(chan wire.ControlMessage, 1)
	sub, err := tr.SubscribeControl(ctx, "job-redis-ctrl-1", func(msg wire.ControlMessage) {
		received <- msg
	})
	if err != nil {
		t.Fatalf("SubscribeControl: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	time.Sleep(50 * time.Millisecond)

	want := wire.ControlMessage{Type: wire.ControlTypeStart}
	payload, _ := json.Marshal(want)
	if err := client.Publish(ctx, "ctrl:job-redis-ctrl-1", payload).Err(); err != nil {
		t.Fatalf("PUBLISH ctrl: %v", err)
	}

	select {
	case got := <-received:
		if got.Type != want.Type {
			t.Errorf("SubscribeControl got type %q; want %q", got.Type, want.Type)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ControlMessage not delivered within 3s")
	}
}

// TestRedisTransport_CompileAssertions is a compile-time only check; it will
// never run but ensures the types satisfy the interfaces.
func TestRedisTransport_CompileAssertions(t *testing.T) {
	// The actual assertions are var _ in redis.go; this test is here to
	// document that compile-time checks exist.
	t.Log("compile assertions verified at build time in redis.go")
}
