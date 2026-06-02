package redisx_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/redisx"
)

// DialTestRedis constructs a *redis.Client pointed at TEST_REDIS_URL
// (defaulting to redis://localhost:6379) and skips the test if Redis is
// unreachable. This implements the two-gate pattern:
//
//  1. Parse the URL — skip if malformed (env misconfiguration).
//  2. Ping the server — skip if no daemon is running.
//
// The helper is exported so that other test packages (stdintransport, jobstore)
// can reuse the same skip guard without duplicating the logic.
func DialTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6379"
	}

	client, err := redisx.NewFromURL(rawURL)
	if err != nil {
		t.Skipf("redisx.DialTestRedis: could not parse TEST_REDIS_URL %q: %v (skipping live Redis tests)", rawURL, err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("redisx.DialTestRedis: Redis unreachable at %q: %v (skipping live Redis tests)", rawURL, err)
		return nil
	}

	return client
}

// TestNewFromURL_MalformedURL verifies that a syntactically invalid Redis URL
// returns a parse error rather than a nil client.
func TestNewFromURL_MalformedURL(t *testing.T) {
	_, err := redisx.NewFromURL("not-a-redis-url://???")
	if err == nil {
		t.Fatal("expected an error for malformed URL, got nil")
	}
}

// TestNewFromURL_ValidURL verifies that a well-formed Redis URL yields a
// non-nil client without requiring a live Redis connection (no Ping is done
// in the constructor).
func TestNewFromURL_ValidURL(t *testing.T) {
	client, err := redisx.NewFromURL("redis://localhost:6379")
	if err != nil {
		t.Fatalf("unexpected error for valid URL: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client for valid URL")
	}
	// Close the client (no connection was made, but be tidy).
	_ = client.Close()
}

// TestNew_ValidConfig verifies that New(cfg) correctly delegates to
// NewFromURL with cfg.RedisURL.
func TestNew_ValidConfig(t *testing.T) {
	cfg := config.Default()
	client, err := redisx.New(cfg)
	if err != nil {
		t.Fatalf("redisx.New with default config: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	_ = client.Close()
}

// TestLiveRedis_Ping exercises the live connection path. Skips when Redis is
// not reachable (no TEST_REDIS_URL env or daemon not running).
func TestLiveRedis_Ping(t *testing.T) {
	client := DialTestRedis(t)
	defer client.Close() //nolint:errcheck

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Ping failed on live Redis: %v", err)
	}
}
