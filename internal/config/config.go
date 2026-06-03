// Package config defines the worker configuration model. Configuration is
// loaded entirely from environment variables (12-factor) — no config files,
// no service endpoints. Full env-parsing wiring is deferred to Phase 2/3; this
// package encodes the constraints that must be established in Phase 1.
package config

import (
	"fmt"
	"time"
)

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

	// HeartbeatIntervalMs is how often the worker writes its heartbeat key to
	// Redis (in milliseconds).  Env: WORKER_HEARTBEAT_INTERVAL_MS (default: 5000)
	HeartbeatIntervalMs int

	// HeartbeatTTLMs is the TTL (in milliseconds) applied to the heartbeat key
	// each time it is written.  It should be several times the interval so that
	// one or two missed beats do not falsely trigger the reaper.
	// Env: WORKER_HEARTBEAT_TTL_MS (default: 20000)
	HeartbeatTTLMs int

	// ── Artifacts / object storage (Phase 9, D-02/D-03) ──────────────────────
	// All S3 settings enter via these fields; the artifactstore package NEVER
	// reads os.Getenv directly (it takes a Config). Empty S3 fields mean the
	// artifacts feature is DISABLED — the worker constructs no S3Store and
	// artifact capture is off, but output pull (stdout/stderr from Redis) still
	// works (D-04). Validate() does NOT require S3 to be set.
	//
	// Standard AWS_* env is read first, with optional ARTIFACT_S3_* overrides so
	// Fly/Tigris (fly storage create) wiring is zero-translation (D-03).

	// S3Endpoint is the S3-compatible endpoint URL (e.g. http://minio:9000).
	// Env: AWS_ENDPOINT_URL_S3 (override: ARTIFACT_S3_ENDPOINT)
	S3Endpoint string

	// S3Bucket is the bucket artifacts are uploaded into under the artifacts/
	// key prefix. Env: BUCKET_NAME (override: ARTIFACT_S3_BUCKET)
	S3Bucket string

	// S3AccessKeyID / S3SecretAccessKey are the S3 credentials. They are read
	// only from config and MUST never be logged (threat T-09-05).
	// Env: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
	// (overrides: ARTIFACT_S3_ACCESS_KEY_ID / ARTIFACT_S3_SECRET_ACCESS_KEY)
	S3AccessKeyID     string
	S3SecretAccessKey string

	// S3Region is the bucket region (e.g. us-east-1; MinIO accepts any value).
	// Env: AWS_REGION (override: ARTIFACT_S3_REGION)
	S3Region string

	// ── Retention TTLs (Phase 9, D-11) — three independent env-managed clocks ─

	// RunResultTTL is the Redis TTL applied to the persisted RunResult key
	// (job:<id>:output). Default 600s. Env: RUN_RESULT_TTL (seconds).
	RunResultTTL time.Duration

	// PresignedURLTTL is the expiry on the presigned GET URLs returned for each
	// artifact. Default 24h. Env: PRESIGNED_URL_TTL or ARTIFACT_S3_PRESIGN_TTL
	// (seconds).
	PresignedURLTTL time.Duration

	// S3ObjectTTL is the provider-side bucket lifecycle expiration applied to
	// the artifacts/ prefix. Default 72h (3 days). S3 lifecycle granularity is
	// 1 DAY minimum (R15 caveat), so this is rounded UP to whole days when the
	// lifecycle rule is set. MUST be >= PresignedURLTTL (enforced by Validate)
	// so a live presigned URL never points at an already-expired object.
	// Env: ARTIFACT_S3_OBJECT_TTL (days).
	S3ObjectTTL time.Duration
}

// Validate enforces the cross-field config invariants and fails fast at boot.
//
// R15 ordering invariant (threat T-09-07): the S3 object lifecycle TTL MUST be
// at least as long as the presigned-URL expiry. Otherwise a presigned URL could
// still be valid (un-expired) while the object it points at has already been
// deleted by the bucket lifecycle rule — a confusing, hard-to-debug failure.
//
// Validate does NOT require S3 to be configured (empty S3 fields = artifacts
// disabled, D-04); it only checks the TTL ordering, which always holds for the
// shipped defaults.
func (c Config) Validate() error {
	if c.S3ObjectTTL < c.PresignedURLTTL {
		return fmt.Errorf(
			"S3ObjectTTL (%s) must be >= PresignedURLTTL (%s): a live presigned URL must never outlive the object it points at (R15)",
			c.S3ObjectTTL, c.PresignedURLTTL,
		)
	}
	return nil
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
		RedisURL:            "redis://localhost:6379",
		SoketiHost:          "soketi",
		SoketiPort:          6001,
		SoketiUseTLS:        false,
		MaxSandboxes:        8,
		DockerHost:          "unix:///var/run/docker.sock",
		WarmupMs:            30000,
		HeartbeatIntervalMs: 5000,
		HeartbeatTTLMs:      20000,
		// Retention TTLs (D-11). S3ObjectTTL (72h) > PresignedURLTTL (24h)
		// satisfies the Validate() ordering invariant out of the box.
		RunResultTTL:    600 * time.Second,
		PresignedURLTTL: 24 * time.Hour,
		S3ObjectTTL:     72 * time.Hour,
	}
}
