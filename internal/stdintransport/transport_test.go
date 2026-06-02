package stdintransport_test

import (
	"context"
	"testing"
	"time"

	"github.com/teovillanueva/code-runner/internal/stdintransport"
)

// TestStubRoundTrip verifies that a published chunk reaches a subscribed handler.
func TestStubRoundTrip(t *testing.T) {
	tr := stdintransport.NewStub()
	ctx := context.Background()

	received := make(chan []byte, 1)
	sub, err := tr.Subscribe(ctx, "job-1", func(chunk []byte) {
		// Copy the slice to avoid aliasing issues across goroutines.
		cp := make([]byte, len(chunk))
		copy(cp, chunk)
		received <- cp
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	want := []byte("hello stdin\n")
	if err := tr.Publish(ctx, "job-1", want); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case got := <-received:
		if string(got) != string(want) {
			t.Errorf("handler received %q; want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called within 1s")
	}
}

// TestStubPublishNoSubscriber verifies that Publish to a job with no active
// subscriber does not error (fire-and-forget semantics of the pub/sub MVP).
func TestStubPublishNoSubscriber(t *testing.T) {
	tr := stdintransport.NewStub()
	if err := tr.Publish(context.Background(), "no-such-job", []byte("data")); err != nil {
		t.Fatalf("Publish to unsubscribed jobID returned error: %v", err)
	}
}

// TestStubCloseStopsDelivery verifies that chunks published after Close() do
// not reach the handler.
func TestStubCloseStopsDelivery(t *testing.T) {
	tr := stdintransport.NewStub()
	ctx := context.Background()

	calls := 0
	sub, err := tr.Subscribe(ctx, "job-2", func(_ []byte) {
		calls++
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Deliver one chunk before closing.
	if err := tr.Publish(ctx, "job-2", []byte("before")); err != nil {
		t.Fatalf("Publish before close: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before Close, got %d", calls)
	}

	if err := sub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Chunk published after close must not reach the handler.
	if err := tr.Publish(ctx, "job-2", []byte("after")); err != nil {
		t.Fatalf("Publish after close: %v", err)
	}
	if calls != 1 {
		t.Errorf("handler was called after Close; total calls = %d, want 1", calls)
	}
}

// TestStubCloseIdempotent verifies that calling Close multiple times does not
// panic or error.
func TestStubCloseIdempotent(t *testing.T) {
	tr := stdintransport.NewStub()
	sub, err := tr.Subscribe(context.Background(), "job-3", func(_ []byte) {})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	for i := range 5 {
		if err := sub.Close(); err != nil {
			t.Fatalf("Close call %d returned error: %v", i+1, err)
		}
	}
}

// TestStubMultipleSubscribers verifies that all active subscribers for a jobID
// receive the published chunk.
func TestStubMultipleSubscribers(t *testing.T) {
	tr := stdintransport.NewStub()
	ctx := context.Background()

	const n = 3
	received := make([]chan []byte, n)
	subs := make([]stdintransport.Subscription, n)
	for i := range n {
		ch := make(chan []byte, 1)
		received[i] = ch
		sub, err := tr.Subscribe(ctx, "job-multi", func(chunk []byte) {
			cp := make([]byte, len(chunk))
			copy(cp, chunk)
			ch <- cp
		})
		if err != nil {
			t.Fatalf("Subscribe %d: %v", i, err)
		}
		subs[i] = sub
	}
	defer func() {
		for _, s := range subs {
			s.Close() //nolint:errcheck
		}
	}()

	want := []byte("broadcast")
	if err := tr.Publish(ctx, "job-multi", want); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	for i, ch := range received {
		select {
		case got := <-ch:
			if string(got) != string(want) {
				t.Errorf("subscriber %d: got %q, want %q", i, got, want)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d: not called within 1s", i)
		}
	}
}
