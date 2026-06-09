// Package main is the worker binary entrypoint. It loads configuration from
// environment variables, constructs the real infrastructure stack (Redis,
// Docker, soketi), and runs the worker job loop until a termination signal is
// received.
//
// The worker is stateless and horizontally scalable — N replicas share one
// Redis job queue and publish to the same soketi server.
//
// Trust boundary (WRK-04): the worker talks ONLY to Redis (job queue +
// stdin/ctrl pub-sub) and soketi (output events). It makes NO HTTP calls to
// the API. Coupling is Redis + soketi only.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/docker/docker/client"

	"github.com/teovillanueva/code-runner/internal/artifactstore"
	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/logging"
	"github.com/teovillanueva/code-runner/internal/manifest"
	"github.com/teovillanueva/code-runner/internal/otelinit"
	"github.com/teovillanueva/code-runner/internal/publisher"
	"github.com/teovillanueva/code-runner/internal/reaper"
	"github.com/teovillanueva/code-runner/internal/redisx"
	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/stdintransport"
	"github.com/teovillanueva/code-runner/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("worker exited with error", "error", err)
			os.Exit(1)
		}
	}
	slog.Info("worker shutdown complete")
}

// run is the factored-out boot sequence. It loads config, constructs the real
// infrastructure stack, and runs the worker loop until ctx is cancelled.
func run(ctx context.Context) error {
	// ── OpenTelemetry bootstrap (env-gated no-op when OTEL_* unset, OBS-01) ────
	// otelinit.Init installs trace/metric/log providers + the W3C propagator ONLY
	// when OTEL is configured; otherwise it is a true no-op (no exporter, no port,
	// no startup regression). The returned shutdown is always non-nil.
	otelShutdown, err := otelinit.Init(ctx)
	if err != nil {
		// Telemetry must never block the worker from running — log and proceed
		// with whatever (possibly no-op) shutdown was returned.
		slog.Warn("otel: init returned error; continuing", "err", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if sErr := otelShutdown(shutdownCtx); sErr != nil {
			slog.Warn("otel: shutdown error", "err", sErr)
		}
	}()

	// ── Structured stdout logging with trace correlation (D-03 stdout-always) ──
	// The custom stdout handler injects trace_id/span_id/job_id from ctx into
	// every JSON line and is installed unconditionally (valid JSON in all states).
	// We fan out to the otelslog bridge as well, so logs ALSO go to OTLP when
	// configured — the bridge is a no-op against the SDK's no-op LoggerProvider
	// when OTEL is off (RESEARCH Pitfall 4: the bridge feeds OTLP only, never stdout).
	stdoutHandler := logging.NewCtxHandler(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(slog.New(logging.NewFanout(stdoutHandler, otelinit.OTLPLogHandler())))

	cfg := configFromEnv()

	// ── Config fail-fast (R15 ordering invariant, threat T-09-07) ──────────────
	// Validate cross-field invariants BEFORE constructing the runner/worker so a
	// broken TTL ordering (a presigned URL that would outlive its object) stops
	// the boot rather than producing silently-dangling URLs at runtime.
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// ── Load language manifests ──────────────────────────────────────────────
	langDir := os.Getenv("LANGUAGES_DIR")
	if langDir == "" {
		// Resolve relative to the binary location (or project root in dev).
		_, thisFile, _, _ := runtime.Caller(0)
		projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
		langDir = filepath.Join(projectRoot, "languages")
	}

	reg, err := manifest.Load(langDir)
	if err != nil {
		return fmt.Errorf("load manifests from %q: %w", langDir, err)
	}

	languages := reg.List()
	slog.Info("worker starting", "available_languages", len(languages))
	for _, info := range languages {
		slog.Info("language available",
			"language", info.Language,
			"version", info.Version,
			"aliases", info.Aliases,
			"interactive", info.Interactive,
		)
	}

	// ── Redis client ─────────────────────────────────────────────────────────
	redisClient, err := redisx.New(cfg)
	if err != nil {
		return fmt.Errorf("redis: parse URL %q: %w", cfg.RedisURL, err)
	}
	defer redisClient.Close() //nolint:errcheck

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		return fmt.Errorf("redis: ping %q: %w", cfg.RedisURL, err)
	}
	slog.Info("redis connected", "url", cfg.RedisURL)

	// ── Jobstore + transport ──────────────────────────────────────────────────
	store := jobstore.New(redisClient)
	transport := stdintransport.NewRedis(redisClient)

	// ── Docker runner ─────────────────────────────────────────────────────────
	seccompPath := os.Getenv("SECCOMP_PROFILE_PATH")
	if seccompPath == "" {
		_, thisFile, _, _ := runtime.Caller(0)
		projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
		absPath, absErr := filepath.Abs(filepath.Join(projectRoot, "profiles", "seccomp", "runner.json"))
		if absErr == nil {
			seccompPath = absPath
		}
	}

	dockerRunner, err := runner.NewDockerSocketRunner(cfg, seccompPath)
	if err != nil {
		return fmt.Errorf("docker: create runner: %w", err)
	}
	slog.Info("docker runner initialized", "seccomp_profile", seccompPath)

	// ── Runner selection: Docker tier always; Zygote tier behind ZYGOTE_ENABLED ─
	// Default (ZYGOTE_ENABLED unset/false): the worker uses DockerSocketRunner for
	// every job — zero behavior change (TIER-04). When ZYGOTE_ENABLED is set (only
	// on the Fly worker, where the Firecracker microVM is the host boundary and the
	// privileged warm-pool container is safe), build the ZygoteRunner and wrap both
	// behind a TieredRunner. Routing is manifest-driven (manifest.ZygoteEligible via
	// the already-loaded registry) — NO language-name branching. The zygote runner
	// gets a deferred Close (pool reaper + warm-container teardown) on shutdown.
	var jobRunner runner.Runner = dockerRunner
	if cfg.ZygoteEnabled {
		zygoteRunner, zErr := runner.NewZygoteRunner(cfg)
		if zErr != nil {
			return fmt.Errorf("zygote: create runner: %w", zErr)
		}
		defer func() {
			if cErr := zygoteRunner.Close(); cErr != nil {
				slog.Warn("zygote: runner close error", "err", cErr)
			}
		}()
		jobRunner = runner.NewTieredRunner(
			dockerRunner,
			zygoteRunner,
			runner.ZygoteEligibleFromRegistry(reg),
		)
		slog.Info("zygote tier enabled — tiered runner active (eligible languages route to warm pool)",
			"relay_port", cfg.ZygoteRelayPort,
			"pool_idle_ms", cfg.ZygotePoolIdleMs,
			"uid_base", cfg.ZygoteUIDBase,
			"pool_memory_mb", cfg.ZygotePoolMemoryMb,
		)
	} else {
		slog.Info("zygote tier disabled — DockerSocketRunner for all languages (safe default)")
	}

	// ── Publisher ─────────────────────────────────────────────────────────────
	pub, err := publisher.New(cfg)
	if err != nil {
		return fmt.Errorf("publisher: create: %w", err)
	}
	slog.Info("publisher initialized",
		"soketi_host", cfg.SoketiHost,
		"soketi_port", cfg.SoketiPort,
		"soketi_use_tls", cfg.SoketiUseTLS,
	)

	// ── Artifact store (Phase 9, D-04) ─────────────────────────────────────────
	// When S3 is configured (bucket + endpoint present) construct the S3Store and
	// ensure its bucket + lifecycle rule exist. A construction error is a BOOT
	// error (a present-but-misconfigured S3 must fail fast). EnsureLifecycle is
	// best-effort: a failure is logged (slog.Warn) but does NOT block boot — it
	// is a cleanup optimization, not an upload prerequisite. When S3 is NOT
	// configured the store stays a nil artifactstore.ArtifactStore: capture is
	// disabled but output pull (stdout/stderr from Redis) stays active (D-04).
	// Credentials are never logged (threat T-09-05).
	var artifactStore artifactstore.ArtifactStore
	if cfg.S3Bucket != "" && cfg.S3Endpoint != "" {
		s3Store, s3Err := artifactstore.NewS3Store(cfg)
		if s3Err != nil {
			return fmt.Errorf("artifactstore: create S3 store: %w", s3Err)
		}
		if lcErr := s3Store.EnsureLifecycle(ctx); lcErr != nil {
			slog.Warn("artifactstore: EnsureLifecycle failed; capture continues, lifecycle/bucket may be incomplete",
				"err", lcErr,
				"bucket", cfg.S3Bucket,
				"endpoint", cfg.S3Endpoint,
			)
		}
		artifactStore = s3Store
		slog.Info("artifact capture enabled",
			"bucket", cfg.S3Bucket,
			"endpoint", cfg.S3Endpoint,
			"presigned_url_ttl", cfg.PresignedURLTTL,
			"object_ttl", cfg.S3ObjectTTL,
		)
	} else {
		slog.Info("artifact capture disabled (S3 unconfigured); output pull active (D-04)")
	}

	// ── Worker ────────────────────────────────────────────────────────────────
	workerCfg := worker.Config{
		MaxSandboxes:        cfg.MaxSandboxes,
		WarmupMs:            cfg.WarmupMs,
		ClaimTimeout:        5 * time.Second,
		HeartbeatIntervalMs: cfg.HeartbeatIntervalMs,
		HeartbeatTTLMs:      cfg.HeartbeatTTLMs,
		Artifacts:           artifactStore,
		RunResultTTL:        cfg.RunResultTTL,
	}

	w := worker.New(store, transport, jobRunner, pub, workerCfg)

	// ── Prometheus scrape endpoint (autoscaling) ───────────────────────────────
	// When WORKER_METRICS_PORT is set, serve /metrics with slots_used/_max +
	// queue_depth so the platform's Prometheus can scrape it and an autoscaler can
	// scale on in-flight load (running + queued). Off by default (no port, no
	// server) — purely additive, like the OTEL path.
	if v := os.Getenv("WORKER_METRICS_PORT"); v != "" {
		if port, perr := strconv.Atoi(v); perr == nil && port > 0 {
			mux := http.NewServeMux()
			mux.HandleFunc("/metrics", w.MetricsHandler())
			metricsSrv := &http.Server{
				Addr:              fmt.Sprintf(":%d", port),
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
			}
			go func() {
				slog.Info("worker metrics server listening", "addr", metricsSrv.Addr)
				if sErr := metricsSrv.ListenAndServe(); sErr != nil && !errors.Is(sErr, http.ErrServerClosed) {
					slog.Warn("worker metrics server error", "err", sErr)
				}
			}()
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = metricsSrv.Shutdown(shutdownCtx)
			}()
		} else {
			slog.Warn("invalid WORKER_METRICS_PORT, metrics server disabled", "value", v)
		}
	}

	// ── Observable gauges (OBS-06) ─────────────────────────────────────────────
	// Register the queue-depth + slots-used/max observable gauges against the
	// MeterProvider installed by otelinit.Init above. When OTEL is unconfigured
	// this resolves to the no-op provider (the callback is never invoked). The
	// deregister func is deferred so the callback detaches on shutdown.
	if deregister, mErr := w.RegisterMetrics(); mErr != nil {
		slog.Warn("worker: could not register observable gauges", "err", mErr)
	} else {
		defer func() { _ = deregister() }()
	}

	// ── Reaper ────────────────────────────────────────────────────────────────
	// The reaper runs alongside the worker and periodically sweeps the host for
	// containers whose owning worker has died.  It uses its own Docker client so
	// the runner interface does not need to be widened.
	reaperDockerCli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		slog.Warn("reaper: could not create Docker client, reaper disabled", "err", err)
	} else {
		// Sweep interval: heartbeat TTL + a small buffer so a dead worker's key
		// has reliably expired before the first sweep evaluates it.
		reaperInterval := time.Duration(cfg.HeartbeatTTLMs)*time.Millisecond + 5*time.Second
		r := reaper.New(reaperDockerCli, store, reaperInterval)
		go r.Run(ctx)
		slog.Info("reaper started", "interval", reaperInterval)
	}

	slog.Info("worker loop starting",
		"max_sandboxes", cfg.MaxSandboxes,
		"warmup_ms", cfg.WarmupMs,
		"heartbeat_interval_ms", cfg.HeartbeatIntervalMs,
		"heartbeat_ttl_ms", cfg.HeartbeatTTLMs,
	)

	w.Run(ctx)

	// Graceful drain (scale-down / deploy safety): Run returned because the
	// signal context was cancelled (SIGTERM), which stops claiming NEW jobs but
	// leaves in-flight sandboxes running. Wait for them to finish so a Fly
	// scale-down or deploy doesn't kill a student mid-session. Fly's fly.toml
	// kill_timeout must be ≥ this drain timeout or the Machine is SIGKILLed first.
	drainTimeout := 120 * time.Second
	if v := os.Getenv("WORKER_DRAIN_TIMEOUT_MS"); v != "" {
		if ms, perr := strconv.Atoi(v); perr == nil && ms > 0 {
			drainTimeout = time.Duration(ms) * time.Millisecond
		}
	}
	slog.Info("worker: shutdown signal received — draining in-flight sandboxes", "drain_timeout", drainTimeout)
	w.Drain(drainTimeout)
	return ctx.Err()
}

// configFromEnv builds a Config from environment variables, falling back to
// the defaults defined in config.Default() for any missing values.
func configFromEnv() config.Config {
	cfg := config.Default()

	if v := os.Getenv("REDIS_URL"); v != "" {
		cfg.RedisURL = v
	}
	if v := os.Getenv("SOKETI_HOST"); v != "" {
		cfg.SoketiHost = v
	}
	if v := os.Getenv("SOKETI_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.SoketiPort = n
		}
	}
	if v := os.Getenv("SOKETI_USE_TLS"); v == "true" || v == "1" {
		cfg.SoketiUseTLS = true
	}
	if v := os.Getenv("SOKETI_APP_ID"); v != "" {
		cfg.SoketiAppID = v
	}
	if v := os.Getenv("SOKETI_APP_KEY"); v != "" {
		cfg.SoketiAppKey = v
	}
	if v := os.Getenv("SOKETI_APP_SECRET"); v != "" {
		cfg.SoketiAppSecret = v
	}
	if v := os.Getenv("WORKER_MAX_SANDBOXES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxSandboxes = n
		}
	}
	if v := os.Getenv("DOCKER_HOST"); v != "" {
		cfg.DockerHost = v
	}
	if v := os.Getenv("SANDBOX_RUNTIME"); v != "" {
		cfg.SandboxRuntime = v
	}
	if v := os.Getenv("WORKER_WARMUP_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.WarmupMs = n
		}
	}
	if v := os.Getenv("WORKER_HEARTBEAT_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.HeartbeatIntervalMs = n
		}
	}
	if v := os.Getenv("WORKER_HEARTBEAT_TTL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.HeartbeatTTLMs = n
		}
	}

	// ── Artifacts / object storage (Phase 9, D-03) ───────────────────────────
	// Standard AWS_* env first, each then overridable by ARTIFACT_S3_* (override
	// applies only when non-empty). Credentials are NEVER logged (threat T-09-05).
	if v := os.Getenv("AWS_ENDPOINT_URL_S3"); v != "" {
		cfg.S3Endpoint = v
	}
	if v := os.Getenv("ARTIFACT_S3_ENDPOINT"); v != "" {
		cfg.S3Endpoint = v
	}
	// Optional public endpoint for presigned URLs (split-horizon: connect via
	// S3Endpoint, sign client-fetchable URLs against this one).
	if v := os.Getenv("ARTIFACT_S3_PUBLIC_ENDPOINT"); v != "" {
		cfg.S3PublicEndpoint = v
	}
	if v := os.Getenv("BUCKET_NAME"); v != "" {
		cfg.S3Bucket = v
	}
	if v := os.Getenv("ARTIFACT_S3_BUCKET"); v != "" {
		cfg.S3Bucket = v
	}
	if v := os.Getenv("AWS_ACCESS_KEY_ID"); v != "" {
		cfg.S3AccessKeyID = v
	}
	if v := os.Getenv("ARTIFACT_S3_ACCESS_KEY_ID"); v != "" {
		cfg.S3AccessKeyID = v
	}
	if v := os.Getenv("AWS_SECRET_ACCESS_KEY"); v != "" {
		cfg.S3SecretAccessKey = v
	}
	if v := os.Getenv("ARTIFACT_S3_SECRET_ACCESS_KEY"); v != "" {
		cfg.S3SecretAccessKey = v
	}
	if v := os.Getenv("AWS_REGION"); v != "" {
		cfg.S3Region = v
	}
	if v := os.Getenv("ARTIFACT_S3_REGION"); v != "" {
		cfg.S3Region = v
	}

	// ── Retention TTLs (D-11) ────────────────────────────────────────────────
	// RUN_RESULT_TTL and PRESIGNED_URL_TTL (or ARTIFACT_S3_PRESIGN_TTL) are
	// expressed in SECONDS; ARTIFACT_S3_OBJECT_TTL is expressed in DAYS (S3
	// lifecycle granularity is whole days — R15 caveat). All use the n > 0 guard.
	if v := os.Getenv("RUN_RESULT_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RunResultTTL = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("PRESIGNED_URL_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.PresignedURLTTL = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("ARTIFACT_S3_PRESIGN_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.PresignedURLTTL = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("ARTIFACT_S3_OBJECT_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.S3ObjectTTL = time.Duration(n) * 24 * time.Hour
		}
	}

	// ── Content-addressed blob store (Phase 16, BLOB-01..) ────────────────────
	// Blobs REUSE the artifact S3 endpoint/creds/region resolved above. The bucket
	// defaults to S3Bucket (blobs and artifacts share one bucket, distinct
	// prefixes) unless BLOB_S3_BUCKET overrides it. TTL/GC knobs are in SECONDS.
	cfg.BlobS3Bucket = cfg.S3Bucket
	if v := os.Getenv("BLOB_S3_BUCKET"); v != "" {
		cfg.BlobS3Bucket = v
	}
	if v := os.Getenv("BLOB_IDLE_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BlobIdleTTL = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("BLOB_GC_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BlobGCInterval = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("BLOB_GC_GRACE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.BlobGCGrace = time.Duration(n) * time.Second
		}
	}

	// ── Zygote runner (Phase 13, ZDEP-01..03) ────────────────────────────────
	// Overlay the ZYGOTE_* knobs (ZYGOTE_ENABLED + relay port / idle / uid base /
	// pool memory). Default = OFF, so a vanilla worker is unaffected; only the Fly
	// worker sets ZYGOTE_ENABLED=true (its fly.toml). The parse/overlay lives in
	// config.ApplyZygoteEnv so it stays unit-testable in the config package.
	cfg = cfg.ApplyZygoteEnv(os.Getenv)

	return cfg
}
