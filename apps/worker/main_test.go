package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRun_ValidLanguagesDir exercises run against the real languages/ directory
// (relative to the repo root). The test asserts that the boot output contains
// the python language, which is the single manifest present in Phase 1.
func TestRun_ValidLanguagesDir(t *testing.T) {
	// The languages directory lives at ../../languages relative to apps/worker.
	var out bytes.Buffer
	err := run("../../languages", &out)
	if err != nil {
		t.Fatalf("run returned unexpected error: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "python") {
		t.Errorf("boot output does not mention python; got:\n%s", output)
	}
}

// TestRun_MissingDir asserts that run returns a non-nil error when the
// languages directory does not exist.
func TestRun_MissingDir(t *testing.T) {
	var out bytes.Buffer
	err := run("/nonexistent/languages/dir", &out)
	if err == nil {
		t.Fatal("expected non-nil error for missing directory, got nil")
	}
}
