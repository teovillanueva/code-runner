package worker

import (
	"testing"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// TestBuildArtifactExcludeSet_RelativePaths is a pure unit test (no Redis) that
// proves the exclude set keys input files by their FULL sanitized relative path
// (FILES-05), so a subdir input is excluded by "data/in.csv" — not by basename.
func TestBuildArtifactExcludeSet_RelativePaths(t *testing.T) {
	t.Parallel()
	spec := wire.JobSpec{
		Files: []wire.FileInput{
			{Name: "main.py", Content: "x"},
			{Name: "data/in.csv", Content: "a,b"},
			{Name: "a/b/c.bin", Content: "AAA=", Encoding: wire.FileInputEncodingBase64},
			{Name: "../escape", Content: "ignored"}, // bad path: skipped, not panicked
		},
	}
	got := buildArtifactExcludeSet(spec)

	mustExclude := []string{"main.py", "data/in.csv", "a/b/c.bin", ".compile_ready"}
	for _, k := range mustExclude {
		if !got[k] {
			t.Fatalf("expected exclude[%q]=true, set=%v", k, got)
		}
	}
	// Basename-only keys must NOT be present for subdir inputs.
	for _, k := range []string{"in.csv", "c.bin"} {
		if got[k] {
			t.Fatalf("did not expect basename key %q in exclude set %v", k, got)
		}
	}
	// The escaping path is silently skipped (the runner rejects it before run).
	if got["escape"] || got["../escape"] {
		t.Fatalf("escaping input must not appear in exclude set: %v", got)
	}
}

func TestBuildArtifactExcludeSet_CompileBinary(t *testing.T) {
	t.Parallel()
	compile := wire.JobSpecCompile([]string{"gcc", "-o", "app", "main.c"})
	spec := wire.JobSpec{
		Compile: &compile,
		Run:     []string{"/workspace/app"},
		Files:   []wire.FileInput{{Name: "main.c", Content: "int main(){}"}},
	}
	got := buildArtifactExcludeSet(spec)
	if !got["app"] {
		t.Fatalf("compile-output binary must be excluded, set=%v", got)
	}
	if !got["main.c"] {
		t.Fatalf("input must be excluded, set=%v", got)
	}
}
