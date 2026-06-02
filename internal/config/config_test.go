package config_test

import (
	"testing"

	"github.com/teovillanueva/code-runner/internal/config"
)

// TestRequiresNativeRedis verifies that the native-Redis constraint is encoded
// as true for every Config value (CFG-04).
func TestRequiresNativeRedis(t *testing.T) {
	// Zero value
	var zero config.Config
	if !zero.RequiresNativeRedis() {
		t.Error("zero Config: RequiresNativeRedis() = false; want true (CFG-04)")
	}

	// Default values
	cfg := config.Default()
	if !cfg.RequiresNativeRedis() {
		t.Error("Default Config: RequiresNativeRedis() = false; want true (CFG-04)")
	}
}

// TestDefaultValues verifies that Default() populates fields with the
// expected development defaults from .env.example.
func TestDefaultValues(t *testing.T) {
	cfg := config.Default()

	if cfg.RedisURL == "" {
		t.Error("Default RedisURL is empty")
	}
	if cfg.MaxSandboxes <= 0 {
		t.Errorf("Default MaxSandboxes = %d; want > 0", cfg.MaxSandboxes)
	}
	if cfg.SoketiPort <= 0 {
		t.Errorf("Default SoketiPort = %d; want > 0", cfg.SoketiPort)
	}
	if cfg.WarmupMs <= 0 {
		t.Errorf("Default WarmupMs = %d; want > 0", cfg.WarmupMs)
	}
	if cfg.DockerHost == "" {
		t.Error("Default DockerHost is empty")
	}
}
