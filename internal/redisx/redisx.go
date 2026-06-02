// Package redisx provides a shared go-redis client constructor for the
// code-runner worker. It encapsulates URL parsing and client configuration so
// that all packages that need a Redis connection share the same setup logic.
//
// CFG-04: only native Redis TCP connections are supported. Upstash REST and
// similar API-only endpoints are NOT valid (they lack SUBSCRIBE/BRPOP support).
package redisx

import (
	"github.com/redis/go-redis/v9"
	"github.com/teovillanueva/code-runner/internal/config"
)

// New parses cfg.RedisURL via redis.ParseURL and returns a configured
// *redis.Client. The client is not yet connected — no Ping is performed so
// construction is cheap and testable without a live Redis instance.
//
// Returns an error if cfg.RedisURL is malformed or cannot be parsed.
func New(cfg config.Config) (*redis.Client, error) {
	return NewFromURL(cfg.RedisURL)
}

// NewFromURL parses rawURL via redis.ParseURL and returns a configured
// *redis.Client. Useful in tests where a config.Config is not available.
//
// Returns an error if rawURL is malformed or cannot be parsed.
func NewFromURL(rawURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opts), nil
}
