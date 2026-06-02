---
phase: 06-language-fan-out
plan: 01
subsystem: worker/runner
tags: [compile-stage, sandbox, interface, language-agnostic]
dependency_graph:
  requires: [05-statelessness-scale]
  provides: [generic-compile-pre-step, worker-compile-gate]
  affects: [internal/runner, internal/worker]
tech_stack:
  added: []
  patterns:
    - ContainerExecCreate/Attach/Inspect for in-container compile step
    - Long-lived hold cmd (cat) when spec.Compile non-nil
    - argv-driven compile gate; zero language-name branching
key_files:
  created:
    - internal/runner/compile_test.go
    - internal/worker/compile_gate_test.go
  modified:
    - internal/runner/runner.go
    - internal/runner/docker.go
    - internal/runner/stub.go
    - internal/runner/docker_unit_test.go
    - internal/session/clocks_test.go
    - internal/worker/worker.go
    - internal/worker/worker_test.go
    - internal/worker/scale_test.go
    - Makefile
decisions:
  - "Long-lived hold cmd (cat) when spec.Compile non-nil: container stays alive through compile exec → artifact → run exec"
  - "Compile runs under live ctx (session clock governs) so compile-bomb is tree-killed same as run-bomb"
  - "stderr accumulated in bytes.Buffer then forwarded via callback in one call per exec (avoids partial-chunk interleaving)"
  - "Non-zero compile exit normalised to exit 1 when infra error to avoid zero-exit false-positive"
metrics:
  duration: 7m
  completed_date: 2026-06-03
  tasks: 2
  files: 9
---

# Phase 06 Plan 01: Language Fan-Out Compile Stage Summary

Generic, manifest-argv-driven compile stage added to Sandbox interface with DockerExec implementation and worker run-path wiring; exit-0 gate proven by three language-agnostic unit tests.

## Tasks Completed

| # | Task | Commit | Key Files |
|---|------|--------|-----------|
| 1 | Extend Sandbox seam with generic compile pre-step | 05dbaf5 | runner.go, docker.go, stub.go, compile_test.go |
| 2 | Wire compile stage into worker + gate tests + Makefile targets | 121be45 | worker.go, compile_gate_test.go, Makefile |
| - | Fix countingSandbox to satisfy updated Sandbox interface | 12c0d94 | scale_test.go |

## Interface Change

`runner.Sandbox` now includes:

```go
// CompileResult carries the compile step outcome.
type CompileResult struct {
    ExitCode   int
    DurationMs int
}

// Added to the Sandbox interface:
Compile(ctx context.Context, argv []string, stderr func([]byte)) (CompileResult, error)
```

`docker.go` Compile implementation:
- Uses `ContainerExecCreate` + `ContainerExecAttach` + `ContainerExecInspect` inside the same container
- Sets `User: sandboxUser`, `WorkingDir: sandboxWorkDir` (inherits all hardening)
- Stdout discarded; stderr accumulated and forwarded via callback
- When `spec.Compile != nil`, `Create` starts the container with `cat` (hold cmd) instead of `spec.Run` so the container stays alive through compile → run

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] closureSandbox/fakeSandbox/countingSandbox missing Compile after interface growth**
- **Found during:** Task 1 (compile phase) — existing sandbox fakes in docker_unit_test.go, session/clocks_test.go, worker/worker_test.go, worker/scale_test.go did not implement the new Compile method
- **Fix:** Added no-op `Compile() (CompileResult{ExitCode:0}, nil)` to all four fakes (interface satisfaction requirement, not behavior change)
- **Files modified:** internal/runner/docker_unit_test.go, internal/session/clocks_test.go, internal/worker/worker_test.go, internal/worker/scale_test.go
- **Commits:** 05dbaf5, 12c0d94

## Test Outputs

### Compile-stage unit tests (no Docker required)

```
=== RUN   TestStubCompileReturnsExitZero
--- PASS: TestStubCompileReturnsExitZero (0.00s)
=== RUN   TestStubCompileNilStderrCallback
--- PASS: TestStubCompileNilStderrCallback (0.00s)
=== RUN   TestCompileForwardsStderrThroughCallback
--- PASS: TestCompileForwardsStderrThroughCallback (0.00s)
=== RUN   TestCompileTableCases
--- PASS: TestCompileTableCases (0.00s)
=== RUN   TestCompileDoesNotStartRunArgv
--- PASS: TestCompileDoesNotStartRunArgv (0.00s)
PASS
ok  github.com/teovillanueva/code-runner/internal/runner  0.435s
```

### Compile gate worker tests (no Docker required)

```
=== RUN   TestWorker_CompileGate_Exit0_RunProceeds
--- PASS: TestWorker_CompileGate_Exit0_RunProceeds (0.06s)
=== RUN   TestWorker_CompileGate_NonZero_TerminatesWithoutRun
--- PASS: TestWorker_CompileGate_NonZero_TerminatesWithoutRun (0.05s)
=== RUN   TestWorker_CompileGate_NilCompile_NilPathUnchanged
--- PASS: TestWorker_CompileGate_NilCompile_NilPathUnchanged (0.06s)
PASS
ok  github.com/teovillanueva/code-runner/internal/worker  0.564s
```

### Python no-regression (worker_integration, against redis:7 + executor/python:3.12)

```
=== RUN   TestIntegration_InteractivePythonJob
--- PASS: TestIntegration_InteractivePythonJob (2.06s)
=== RUN   TestIntegration_BatchPythonJob
--- PASS: TestIntegration_BatchPythonJob (2.08s)
=== RUN   TestIntegration_FileBasedPythonJob
--- PASS: TestIntegration_FileBasedPythonJob (2.08s)
PASS
ok  github.com/teovillanueva/code-runner/internal/worker  6.646s
```

### go test ./... green

```
ok  github.com/teovillanueva/code-runner/apps/worker
ok  github.com/teovillanueva/code-runner/internal/config
ok  github.com/teovillanueva/code-runner/internal/jobstore
ok  github.com/teovillanueva/code-runner/internal/keys
ok  github.com/teovillanueva/code-runner/internal/manifest
ok  github.com/teovillanueva/code-runner/internal/publisher
ok  github.com/teovillanueva/code-runner/internal/redisx
ok  github.com/teovillanueva/code-runner/internal/runner
ok  github.com/teovillanueva/code-runner/internal/session
ok  github.com/teovillanueva/code-runner/internal/stdintransport
ok  github.com/teovillanueva/code-runner/internal/worker
```

## Known Stubs

None. All changes are functional implementations.

## Threat Flags

No new network endpoints, auth paths, or file access patterns introduced. The compile exec inherits all existing sandbox hardening (network=none, cap-drop ALL, non-root, seccomp) as required by T-06-01 and T-06-02. No new HostConfig grants.

## Self-Check: PASSED

All expected files found. All commit hashes verified in git history.
