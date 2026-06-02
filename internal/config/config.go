// Package config defines the worker configuration model. Configuration is
// loaded entirely from environment variables (12-factor) — no config files,
// no service endpoints. Full env-parsing wiring is deferred to Phase 2/3; this
// package encodes the constraints that must be established in Phase 1.
package config

// Config holds the runtime configuration for the code-runner worker. All
// fields correspond to environment variables defined in .env.example.
//
// CFG-04 — native Redis requirement: the worker uses blocking Redis operations
// (SUBSCRIBE, BRPOP, XREAD BLOCK) that require a native TCP connection to
// Redis. API-only serverless Redis implementations (e.g. Upstash REST API)
// speak only HTTP and do NOT support these blocking commands. See
// docs/redis-constraint.md for the full rationale and deployment guidance.
type Config struct {
	// RedisURL is the connection URL for the Redis server (e.g.
	// redis://redis:6379). MUST point to a native Redis / Valkey instance.
	// Upstash REST and similar API-only endpoints are NOT valid here.
	//
	// Env: REDIS_URL (default: redis://localhost:6379)
	RedisURL string

	// SoketiHost is the hostname of the soketi (Pusher-compatible) server.
	// Env: SOKETI_HOST
	SoketiHost string

	// SoketiPort is the port on which soketi listens for HTTP trigger calls.
	// Env: SOKETI_PORT (default: 6001)
	SoketiPort int

	// SoketiUseTLS controls whether the worker connects to soketi over TLS.
	// Env: SOKETI_USE_TLS (default: false)
	SoketiUseTLS bool

	// SoketiAppID, SoketiAppKey, SoketiAppSecret are the Pusher credentials
	// the worker uses to trigger events on soketi channels.
	// Env: SOKETI_APP_ID, SOKETI_APP_KEY, SOKETI_APP_SECRET
	SoketiAppID     string
	SoketiAppKey    string
	SoketiAppSecret string

	// MaxSandboxes is the maximum number of concurrent live sandboxes this
	// worker instance will hold. Requests beyond this cap are 429'd at the
	// API layer. Env: WORKER_MAX_SANDBOXES (default: 8)
	MaxSandboxes int

	// DockerHost is the Docker socket endpoint the worker talks to. Defaults
	// to the host socket (no Docker-in-Docker). Env: DOCKER_HOST
	DockerHost string

	// SandboxRuntime is an optional container runtime override passed to the
	// Docker daemon (e.g. "runsc" for gVisor). Empty string uses the daemon
	// default (runc). Env: SANDBOX_RUNTIME
	SandboxRuntime string

	// WarmupMs is the warm-up grace period in milliseconds. If /start is not
	// received within this window after /execute, the slot is reclaimed and
	// the container is removed. Env: WORKER_WARMUP_MS (default: 30000)
	WarmupMs int
}

// RequiresNativeRedis returns true unconditionally, encoding the CFG-04
// constraint that the worker requires a native Redis connection with support
// for blocking commands:
//
//   - SUBSCRIBE / UNSUBSCRIBE — pub/sub for stdin delivery (MVP)
//   - BRPOP / BLPOP / LMOVE — blocking job dequeue from Redis Lists
//   - XREAD BLOCK / XREADGROUP — blocking Stream reads (Streams upgrade)
//
// An API-only serverless Redis (Upstash, Momento, etc.) that speaks only
// HTTP/REST is NOT viable for the worker. It is acceptable for the API
// (stateless HTTP requests only). The recommended deployment is a single
// native managed Redis / Valkey shared by both the API and the worker.
//
// See docs/redis-constraint.md for the full rationale and deployment guidance.
func (c Config) RequiresNativeRedis() bool {
	return true
}

// Default returns a Config populated with the development defaults documented
// in .env.example. In production, override these via environment variables.
// Full env-var parsing (e.g. via caarlos0/env) is wired in Phase 2/3.
func Default() Config {
	return Config{
		RedisURL:     "redis://localhost:6379",
		SoketiHost:   "soketi",
		SoketiPort:   6001,
		SoketiUseTLS: false,
		MaxSandboxes: 8,
		DockerHost:   "unix:///var/run/docker.sock",
		WarmupMs:     30000,
	}
}
