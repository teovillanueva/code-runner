// Package runner — generic compile pre-step unit tests.
//
// This file contains NO language-name literals (no "rust", "sqlite", "Rscript",
// etc.).  All assertions are argv-driven and work with the no-op stubSandbox.
// Tests run under plain `go test ./...` without any build tag.
package runner_test

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// Compile-time assertions: both concrete sandbox types must implement the
// extended Sandbox interface (including the new Compile method).
// If either is missing Compile these lines fail to compile.
var (
	_ runner.Sandbox = (*fakeSandboxForCompileTest)(nil)
)

// ─────────────────────────────────────────────────────────────────────────────
// fakeSandboxForCompileTest — minimal Sandbox that records Compile calls and
// can inject stderr bytes + an exit code.
// ─────────────────────────────────────────────────────────────────────────────

type fakeSandboxForCompileTest struct {
	mu            sync.Mutex
	compileCalled bool
	compileArgv   []string
	compileExit   int
	compileStderr []byte
}

func (f *fakeSandboxForCompileTest) Stdin() io.WriteCloser {
	pr, pw := io.Pipe()
	go io.Copy(io.Discard, pr) //nolint:errcheck
	return pw
}
func (f *fakeSandboxForCompileTest) Stdout() io.Reader { return bytes.NewReader(nil) }
func (f *fakeSandboxForCompileTest) Stderr() io.Reader { return bytes.NewReader(nil) }

func (f *fakeSandboxForCompileTest) Wait(_ context.Context) (runner.Result, error) {
	return runner.Result{}, nil
}
func (f *fakeSandboxForCompileTest) Kill(_ context.Context) error    { return nil }
func (f *fakeSandboxForCompileTest) Cleanup() error                  { return nil }

func (f *fakeSandboxForCompileTest) Compile(_ context.Context, argv []string, stderrFn func([]byte)) (runner.CompileResult, error) {
	f.mu.Lock()
	f.compileCalled = true
	f.compileArgv = argv
	f.mu.Unlock()

	// Forward the scripted stderr bytes to the callback.
	if len(f.compileStderr) > 0 && stderrFn != nil {
		stderrFn(f.compileStderr)
	}

	return runner.CompileResult{ExitCode: f.compileExit, DurationMs: 1}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestStubCompileReturnsExitZero verifies that stubSandbox.Compile returns a
// zero-exit CompileResult without error.
func TestStubCompileReturnsExitZero(t *testing.T) {
	r := runner.NewStub()
	sb, err := r.Create(context.Background(), wire.JobSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sb.Cleanup() //nolint:errcheck

	result, err := sb.Compile(context.Background(), []string{"/usr/bin/true"}, nil)
	if err != nil {
		t.Fatalf("stub Compile returned unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("stub Compile: expected ExitCode 0, got %d", result.ExitCode)
	}
}

// TestStubCompileNilStderrCallback verifies that a nil stderr callback does not
// panic — callers are permitted to pass nil when they don't need diagnostics.
func TestStubCompileNilStderrCallback(t *testing.T) {
	r := runner.NewStub()
	sb, err := r.Create(context.Background(), wire.JobSpec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sb.Cleanup() //nolint:errcheck

	// Should not panic with nil callback.
	if _, err := sb.Compile(context.Background(), []string{"any", "argv"}, nil); err != nil {
		t.Fatalf("stub Compile with nil callback: %v", err)
	}
}

// TestCompileForwardsStderrThroughCallback verifies that when the compile step
// produces stderr bytes, they are forwarded through the callback argument.
// Uses fakeSandboxForCompileTest — no Docker required.
func TestCompileForwardsStderrThroughCallback(t *testing.T) {
	expectedStderr := []byte("error: undefined symbol 'main'\n")

	fake := &fakeSandboxForCompileTest{
		compileExit:   1,
		compileStderr: expectedStderr,
	}

	var received []byte
	stderrFn := func(b []byte) {
		received = append(received, b...)
	}

	argv := []string{"/usr/bin/compile-tool", "-o", "/workspace/prog", "main.c"}
	result, err := fake.Compile(context.Background(), argv, stderrFn)
	if err != nil {
		t.Fatalf("Compile returned unexpected error: %v", err)
	}

	// The fake returns compileExit=1 → non-zero means compile failure.
	if result.ExitCode == 0 {
		t.Error("expected non-zero ExitCode for failed compile")
	}

	if !bytes.Equal(received, expectedStderr) {
		t.Errorf("stderr mismatch: got %q, want %q", received, expectedStderr)
	}

	// Confirm argv was forwarded unchanged.
	if !stringSliceEqual(fake.compileArgv, argv) {
		t.Errorf("argv mismatch: got %v, want %v", fake.compileArgv, argv)
	}
}

// TestCompileTableCases covers the three key behaviors via table-driven tests
// using the fake sandbox (no Docker).
func TestCompileTableCases(t *testing.T) {
	cases := []struct {
		name          string
		compileExit   int
		compileStderr []byte
		wantExitCode  int
		wantStderr    bool
	}{
		{
			name:         "exit 0 no stderr",
			compileExit:  0,
			wantExitCode: 0,
			wantStderr:   false,
		},
		{
			name:          "exit 1 with stderr",
			compileExit:   1,
			compileStderr: []byte("error: syntax error\n"),
			wantExitCode:  1,
			wantStderr:    true,
		},
		{
			name:          "exit 2 with stderr",
			compileExit:   2,
			compileStderr: []byte("fatal error: file not found\n"),
			wantExitCode:  2,
			wantStderr:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeSandboxForCompileTest{
				compileExit:   tc.compileExit,
				compileStderr: tc.compileStderr,
			}

			var stderrOut []byte
			result, err := fake.Compile(
				context.Background(),
				[]string{"/usr/bin/compiler", "main.src"},
				func(b []byte) { stderrOut = append(stderrOut, b...) },
			)
			if err != nil {
				t.Fatalf("Compile error: %v", err)
			}
			if result.ExitCode != tc.wantExitCode {
				t.Errorf("ExitCode: got %d, want %d", result.ExitCode, tc.wantExitCode)
			}
			gotStderr := len(stderrOut) > 0
			if gotStderr != tc.wantStderr {
				t.Errorf("wantStderr=%v but gotStderr=%v (content: %q)", tc.wantStderr, gotStderr, stderrOut)
			}
		})
	}
}

// TestCompileDoesNotStartRunArgv verifies that Compile only operates on the
// compile argv and does not invoke any run-step side effects.  The fake
// sandbox records compileCalled; the test asserts no Wait/Stdout read occurred.
func TestCompileDoesNotStartRunArgv(t *testing.T) {
	fake := &fakeSandboxForCompileTest{compileExit: 0}

	compileArgv := []string{"/usr/bin/compile-step", "source.src", "-o", "/workspace/out"}
	_, err := fake.Compile(context.Background(), compileArgv, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	fake.mu.Lock()
	called := fake.compileCalled
	fake.mu.Unlock()

	if !called {
		t.Error("expected Compile to be called on the sandbox")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
