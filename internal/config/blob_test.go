package config_test

import (
	"testing"
	"time"

	"github.com/teovillanueva/code-runner/internal/config"
)

// TestBlobDefaults pins the CAS blob store defaults (Phase 16): 24h idle TTL,
// 10m GC interval, 30m GC grace. BlobS3Bucket is empty in Default() (it is
// resolved to S3Bucket in configFromEnv, not in Default()).
func TestBlobDefaults(t *testing.T) {
	c := config.Default()
	if c.BlobIdleTTL != 24*time.Hour {
		t.Errorf("BlobIdleTTL = %v, want 24h", c.BlobIdleTTL)
	}
	if c.BlobGCInterval != 10*time.Minute {
		t.Errorf("BlobGCInterval = %v, want 10m", c.BlobGCInterval)
	}
	if c.BlobGCGrace != 30*time.Minute {
		t.Errorf("BlobGCGrace = %v, want 30m", c.BlobGCGrace)
	}
}

// TestBlobDefaultsValidate: the shipped blob defaults satisfy Validate().
func TestBlobDefaultsValidate(t *testing.T) {
	if err := config.Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v, want nil", err)
	}
}

// TestBlobValidateRejectsBadKnobs: non-positive TTL/interval and a negative
// grace each fail fast. A zero grace is allowed (delete-when-collectable).
func TestBlobValidateRejectsBadKnobs(t *testing.T) {
	t.Run("zero idle TTL", func(t *testing.T) {
		c := config.Default()
		c.BlobIdleTTL = 0
		if err := c.Validate(); err == nil {
			t.Fatal("Validate() = nil; want error for BlobIdleTTL=0")
		}
	})
	t.Run("zero GC interval", func(t *testing.T) {
		c := config.Default()
		c.BlobGCInterval = 0
		if err := c.Validate(); err == nil {
			t.Fatal("Validate() = nil; want error for BlobGCInterval=0")
		}
	})
	t.Run("negative GC grace", func(t *testing.T) {
		c := config.Default()
		c.BlobGCGrace = -time.Second
		if err := c.Validate(); err == nil {
			t.Fatal("Validate() = nil; want error for negative BlobGCGrace")
		}
	})
	t.Run("zero GC grace allowed", func(t *testing.T) {
		c := config.Default()
		c.BlobGCGrace = 0
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate() = %v; want nil for BlobGCGrace=0", err)
		}
	})
}
