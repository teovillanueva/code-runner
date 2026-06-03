package config_test

import (
	"testing"
	"time"

	"github.com/teovillanueva/code-runner/internal/config"
)

// TestDefaultValidate asserts the shipped defaults satisfy the TTL ordering
// invariant (S3ObjectTTL >= PresignedURLTTL), so a vanilla worker boots without
// a config error (R15).
func TestDefaultValidate(t *testing.T) {
	if err := config.Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v, want nil", err)
	}
}

// TestDefaultTTLs documents the three env-managed retention TTLs and their
// shipped defaults (D-11): RunResult Redis TTL ~600s, presigned-URL 24h, S3
// object lifecycle 72h (> URL expiry, day-granular per R15).
func TestDefaultTTLs(t *testing.T) {
	c := config.Default()
	if c.RunResultTTL != 600*time.Second {
		t.Errorf("RunResultTTL = %v, want 600s", c.RunResultTTL)
	}
	if c.PresignedURLTTL != 24*time.Hour {
		t.Errorf("PresignedURLTTL = %v, want 24h", c.PresignedURLTTL)
	}
	if c.S3ObjectTTL != 72*time.Hour {
		t.Errorf("S3ObjectTTL = %v, want 72h", c.S3ObjectTTL)
	}
}

// TestValidateFailsFastOnBadOrdering asserts Validate() returns a non-nil error
// when the object TTL is shorter than the presigned-URL expiry — a live URL
// would otherwise outlive the object it points at (R15 ordering invariant,
// threat T-09-07).
func TestValidateFailsFastOnBadOrdering(t *testing.T) {
	c := config.Default()
	c.S3ObjectTTL = 1 * time.Hour
	c.PresignedURLTTL = 24 * time.Hour
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() = nil, want non-nil error for S3ObjectTTL < PresignedURLTTL")
	}
}

// TestValidateAllowsEqualTTLs asserts the boundary case (objectTTL ==
// presignedURLTTL) is valid — the invariant is >=, not >.
func TestValidateAllowsEqualTTLs(t *testing.T) {
	c := config.Default()
	c.S3ObjectTTL = 24 * time.Hour
	c.PresignedURLTTL = 24 * time.Hour
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with equal TTLs = %v, want nil", err)
	}
}
