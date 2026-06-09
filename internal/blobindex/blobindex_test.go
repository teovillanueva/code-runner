package blobindex

import (
	"strings"
	"testing"
)

// TestTouchScriptShape pins the invariants of the Lua touch script without a
// live Redis: it is monotonic (only extends), records metadata once (HSETNX),
// and never shrinks an existing longer TTL. This is a guard against an edit that
// accidentally turns the monotonic extend into an unconditional PEXPIRE.
func TestTouchScriptShape(t *testing.T) {
	// Monotonic guard: the conditional that decides whether to extend.
	if !strings.Contains(touchScript, "reqTtl > cur") {
		t.Error("touch script lost its monotonic guard (reqTtl > cur)")
	}
	// First-write-only metadata: HSETNX, never HSET (re-touch must not rewrite).
	if !strings.Contains(touchScript, "HSETNX") {
		t.Error("touch script must use HSETNX so re-touch never rewrites metadata")
	}
	if strings.Contains(touchScript, "HSET ") || strings.Contains(touchScript, "'HSET'") {
		t.Error("touch script must NOT use plain HSET (would clobber original metadata)")
	}
	// Index membership is maintained.
	if !strings.Contains(touchScript, "SADD") {
		t.Error("touch script must SADD the hash into the index set")
	}
	// PEXPIRE (ms granularity) is used, not EXPIRE (whole seconds).
	if !strings.Contains(touchScript, "PEXPIRE") {
		t.Error("touch script must use PEXPIRE for ms-granular monotonic TTL")
	}
}
