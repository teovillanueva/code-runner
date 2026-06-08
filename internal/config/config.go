// Package config defines the worker configuration model. Configuration is
// loaded entirely from environment variables (12-factor) — no config files,
// no service endpoints. Full env-parsing wiring is deferred to Phase 2/3; this
// package encodes the constraints that must be established in Phase 1.
package config

import (
	"fmt"
	"strconv"
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

	// S3PublicEndpoint, when non-empty, is the endpoint used to SIGN presigned
	// GET URLs — distinct from S3Endpoint (used to connect/upload). This solves
	// the split-horizon case where the worker reaches the object store at an
	// internal address (e.g. http://minio:9000) but clients/browsers must fetch
	// from a different public address (e.g. http://localhost:9000 in dev, or a
	// CDN/public domain in prod). SigV4 signs the host, so a presigned URL must
	// be signed with the SAME host the client will request. Empty → presign with
	// S3Endpoint (the common single-host case).
	// Env: ARTIFACT_S3_PUBLIC_ENDPOINT
	S3PublicEndpoint string

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

	// ── Zygote runner (Phase 12, ZDEP-01..03) ────────────────────────────────
	// These knobs configure the ZygoteRunner + warm parent pool. They are parsed
	// and validated here but DO NOT change which runner the worker builds — the
	// worker's runner selection (TieredRunner wiring behind ZygoteEnabled) lands
	// in Phase 13. Default = OFF, so a vanilla worker is unaffected.

	// ZygoteEnabled gates the ZygoteRunner on. When false (the default) the
	// worker uses DockerSocketRunner for everything. Only set true on the Fly
	// worker where the Firecracker microVM is the real host boundary (the pool
	// container runs privileged). Env: ZYGOTE_ENABLED (default false).
	ZygoteEnabled bool

	// ZygoteRelayPort is the TCP port the zygote agent listens on inside each
	// pool container; the worker dials the pool container's Docker-network IP on
	// this port. Must match the agent's ZYGOTE_RELAY_PORT. Env: ZYGOTE_RELAY_PORT
	// (default 7000).
	ZygoteRelayPort int

	// ZygotePoolIdleMs is the idle window (in ms): after no jobs for this long a
	// warm pool container is torn down to reclaim RAM (POOL-03). Env:
	// ZYGOTE_POOL_IDLE_MS (default 300000 = 5 min).
	ZygotePoolIdleMs int

	// ZygoteUIDBase is the base UID for per-child UID assignment inside the pool
	// container (child uid = base + n). Must match the agent's ZYGOTE_UID_BASE.
	// Env: ZYGOTE_UID_BASE (default 100000).
	ZygoteUIDBase int

	// ZygotePoolMemoryMb is the memory cap (MiB) applied to each warm pool
	// container itself (the shared parent). Per-child memory.max is derived from
	// each job's Limits.MemoryMb independently. Env: ZYGOTE_POOL_MEMORY_MB
	// (default 1024).
	ZygotePoolMemoryMb int
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

	// Zygote knobs are only meaningful when the runner is enabled; validate them
	// then so a vanilla (Docker-only) worker never trips on zygote settings.
	if c.ZygoteEnabled {
		if c.ZygoteRelayPort <= 0 || c.ZygoteRelayPort > 65535 {
			return fmt.Errorf("ZygoteRelayPort (%d) must be in 1..65535 when ZygoteEnabled", c.ZygoteRelayPort)
		}
		if c.ZygotePoolIdleMs <= 0 {
			return fmt.Errorf("ZygotePoolIdleMs (%d) must be > 0 when ZygoteEnabled", c.ZygotePoolIdleMs)
		}
		if c.ZygoteUIDBase <= 0 {
			return fmt.Errorf("ZygoteUIDBase (%d) must be > 0 when ZygoteEnabled", c.ZygoteUIDBase)
		}
		if c.ZygotePoolMemoryMb <= 0 {
			return fmt.Errorf("ZygotePoolMemoryMb (%d) must be > 0 when ZygoteEnabled", c.ZygotePoolMemoryMb)
		}
	}
	return nil
}

// ApplyZygoteEnv overlays the zygote knobs from environment variables onto c,
// leaving Default() values in place when a var is unset or unparsable. It is a
// standalone overlay (the worker's configFromEnv calls it in Phase 13) so the
// parsing is unit-testable here without importing apps/worker. It does NOT
// change which runner the worker builds; runner selection is Phase 13.
//
// Env: ZYGOTE_ENABLED, ZYGOTE_RELAY_PORT, ZYGOTE_POOL_IDLE_MS, ZYGOTE_UID_BASE,
// ZYGOTE_POOL_MEMORY_MB.
func (c Config) ApplyZygoteEnv(getenv func(string) string) Config {
	if v := getenv("ZYGOTE_ENABLED"); v == "true" || v == "1" {
		c.ZygoteEnabled = true
	}
	if v := getenv("ZYGOTE_RELAY_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ZygoteRelayPort = n
		}
	}
	if v := getenv("ZYGOTE_POOL_IDLE_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ZygotePoolIdleMs = n
		}
	}
	if v := getenv("ZYGOTE_UID_BASE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ZygoteUIDBase = n
		}
	}
	if v := getenv("ZYGOTE_POOL_MEMORY_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ZygotePoolMemoryMb = n
		}
	}
	return c
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
		// Zygote runner (Phase 12) — OFF by default; safe default = Docker for
		// everything. The knobs carry sane values so an operator only has to flip
		// ZYGOTE_ENABLED=true on the Fly worker.
		ZygoteEnabled:      false,
		ZygoteRelayPort:    7000,
		ZygotePoolIdleMs:   300000,
		ZygoteUIDBase:      100000,
		ZygotePoolMemoryMb: 1024,
	}
}
