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
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/docker/docker/client"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/manifest"
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
	cfg := configFromEnv()

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

	// ── Worker ────────────────────────────────────────────────────────────────
	workerCfg := worker.Config{
		MaxSandboxes:        cfg.MaxSandboxes,
		WarmupMs:            cfg.WarmupMs,
		ClaimTimeout:        5 * time.Second,
		HeartbeatIntervalMs: cfg.HeartbeatIntervalMs,
		HeartbeatTTLMs:      cfg.HeartbeatTTLMs,
	}

	w := worker.New(store, transport, dockerRunner, pub, workerCfg)

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

	return cfg
}
