package runner

import (
	"context"
	"testing"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// TestBuildHelloFromSpec asserts the HELLO payload is derived correctly from the
// JobSpec + config: per-child limits come from the job, uid base from config,
// files mapped, entrypoint defaulted.
func TestBuildHelloFromSpec(t *testing.T) {
	cfg := config.Default()
	cfg.ZygoteUIDBase = 200000

	spec := wire.JobSpec{
		JobId:      "job-xyz",
		Entrypoint: "solution.py",
		Files: []wire.FileInput{
			{Name: "solution.py", Content: "print(1)"},
			{Name: "data.txt", Content: "x"},
		},
		Limits: wire.Limits{
			MemoryMb: 256,
			Pids:     32,
			OutputKb: 8192,
		},
	}

	h := buildHello(spec, cfg)

	if h.JobID != "job-xyz" {
		t.Errorf("JobID = %q", h.JobID)
	}
	if h.Entrypoint != "solution.py" {
		t.Errorf("Entrypoint = %q", h.Entrypoint)
	}
	if h.UID != 200000 {
		t.Errorf("UID = %d, want 200000", h.UID)
	}
	if h.MemMaxBytes != 256*1024*1024 {
		t.Errorf("MemMaxBytes = %d, want %d", h.MemMaxBytes, 256*1024*1024)
	}
	if h.PidsMax != 32 {
		t.Errorf("PidsMax = %d, want 32", h.PidsMax)
	}
	if len(h.Files) != 2 || h.Files[0].Name != "solution.py" {
		t.Errorf("Files mismatch: %+v", h.Files)
	}
	// tmpfsBytes derived from outputKb heuristic: ((8192/1024+1)*2) MiB = 18 MiB,
	// but never below the 64 MiB default.
	if h.TmpfsBytes < defaultZygoteTmpfsBytes {
		t.Errorf("TmpfsBytes = %d, want >= default %d", h.TmpfsBytes, defaultZygoteTmpfsBytes)
	}
}

// TestBuildHelloDefaults asserts sane defaults when limits/entrypoint are zero.
func TestBuildHelloDefaults(t *testing.T) {
	cfg := config.Default()
	spec := wire.JobSpec{JobId: "j"}
	h := buildHello(spec, cfg)
	if h.Entrypoint != "main.py" {
		t.Errorf("default Entrypoint = %q, want main.py", h.Entrypoint)
	}
	if h.MemMaxBytes != 128*1024*1024 {
		t.Errorf("default MemMaxBytes = %d", h.MemMaxBytes)
	}
	if h.PidsMax != 64 {
		t.Errorf("default PidsMax = %d, want 64", h.PidsMax)
	}
	if h.TmpfsBytes != defaultZygoteTmpfsBytes {
		t.Errorf("default TmpfsBytes = %d", h.TmpfsBytes)
	}
}

// TestZygoteSandboxCompileUnsupported asserts Compile returns the expected error
// (it is never called for Python/R, which have compile==nil).
func TestZygoteSandboxCompileUnsupported(t *testing.T) {
	rc, agent := newTestRelay(t)
	defer func() { _ = agent.Close() }()
	sb := &zygoteSandbox{rc: rc, stdin: &relayStdin{rc: rc}}
	_, err := sb.Compile(context.Background(), []string{"gcc"}, nil)
	if err == nil {
		t.Fatal("Compile: err = nil, want unsupported error")
	}
}

// TestZygoteSandboxCPUReader returns the latest pushed CPU frame value.
func TestZygoteSandboxCPUReader(t *testing.T) {
	rc, agent := newTestRelay(t)
	defer func() { _ = agent.Close() }()
	sb := &zygoteSandbox{rc: rc, stdin: &relayStdin{rc: rc}}

	cpuFn := sb.CPUReader()
	if v, _ := cpuFn(context.Background()); v != 0 {
		t.Errorf("initial CPU = %d, want 0", v)
	}
	rc.latestCPUMs.Store(555)
	if v, _ := cpuFn(context.Background()); v != 555 {
		t.Errorf("CPU after push = %d, want 555", v)
	}
}

// TestZygoteSandboxWaitFromExit builds a Result from an EXIT frame value.
func TestZygoteSandboxWaitFromExit(t *testing.T) {
	rc, agent := newTestRelay(t)
	defer func() { _ = agent.Close() }()
	sb := &zygoteSandbox{rc: rc, stdin: &relayStdin{rc: rc}}

	code := 3
	rc.exitCh <- relayExit{exitCode: &code}

	res, err := sb.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Errorf("ExitCode = %v, want 3", res.ExitCode)
	}
	if res.TimedOut || res.IdleTimedOut {
		t.Errorf("Wait must not set clock flags (session does): %+v", res)
	}
}

// TestZygoteSandboxWaitSignal maps a signal number to a name.
func TestZygoteSandboxWaitSignal(t *testing.T) {
	rc, agent := newTestRelay(t)
	defer func() { _ = agent.Close() }()
	sb := &zygoteSandbox{rc: rc, stdin: &relayStdin{rc: rc}}

	sig := 9
	rc.exitCh <- relayExit{signal: &sig}
	res, err := sb.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Signal == nil || *res.Signal != "SIGKILL" {
		t.Errorf("Signal = %v, want SIGKILL", res.Signal)
	}
}

// TestSignalName covers the signal name mapping including the numeric fallback.
func TestSignalName(t *testing.T) {
	cases := map[int]string{9: "SIGKILL", 15: "SIGTERM", 11: "SIGSEGV", 99: "SIG99"}
	for sig, want := range cases {
		if got := signalName(sig); got != want {
			t.Errorf("signalName(%d) = %q, want %q", sig, got, want)
		}
	}
}

// TestZygoteSandboxCleanupIdempotent: Cleanup is safe to call repeatedly and
// releases the pool reservation exactly once.
func TestZygoteSandboxCleanupIdempotent(t *testing.T) {
	rc, agent := newTestRelay(t)
	defer func() { _ = agent.Close() }()
	releases := 0
	sb := &zygoteSandbox{
		rc:      rc,
		stdin:   &relayStdin{rc: rc},
		release: func() { releases++ },
	}
	_ = sb.Cleanup()
	_ = sb.Cleanup()
	_ = sb.Cleanup()
	if releases != 1 {
		t.Errorf("release called %d times, want 1 (idempotent Cleanup)", releases)
	}
}
