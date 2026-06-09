package blobindex

import (
	"testing"
	"time"
)

// TestNewGCDefaultsLockTTL: a non-positive lock TTL falls back to a sane default
// so a crashed sweep's lock cannot be held forever.
func TestNewGCDefaultsLockTTL(t *testing.T) {
	gc := NewGC(nil, nil, 30*time.Minute, 0)
	if gc.lockTTL <= 0 {
		t.Fatalf("lockTTL = %v; want a positive default", gc.lockTTL)
	}
	if gc.grace != 30*time.Minute {
		t.Fatalf("grace = %v; want 30m", gc.grace)
	}
}
