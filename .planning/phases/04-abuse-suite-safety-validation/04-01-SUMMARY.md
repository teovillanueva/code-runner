---
phase: 04-abuse-suite-safety-validation
plan: "01"
subsystem: worker/safety
tags: [abuse-suite, safety, containment, cgroup-v2, stdin-eof, cpu-clock]
dependency_graph:
  requires:
    - "03-*: worker run loop + session clocks + DockerSocketRunner"
  provides:
    - "Empirical proof of all six sandbox safety properties (fork-bomb, OOM, wall, idle, EOF, output-flood)"
    - "CPU clock end-to-end path verified via real cgroup v2 polling"
  affects:
    - "internal/worker/abuse_test.go (new)"
    - "internal/worker/worker.go (DockerSandbox interface fix)"
    - "internal/runner/docker.go (stdin pipe + StdinOnce fix)"
    - "internal/worker/integration_test.go (stdin JSON format fix)"
    - "Makefile (abuse target path fix)"
tech_stack:
  added: []
  patterns:
    - "build-tagged abuse suite (//go:build abuse) separate from go test ./..."
    - "Redis pub/sub stdin delivery must use JSON StdinMessage envelope"
    - "Docker StdinOnce=true required for EOF delivery on macOS Docker Desktop"
    - "Internal io.Pipe for stdin pump decouples EOF signal from stdout read path"
key_files:
  created:
    - "internal/worker/abuse_test.go"
    - ".planning/phases/04-abuse-suite-safety-validation/04-01-SUMMARY.md"
  modified:
    - "internal/worker/worker.go"
    - "internal/runner/docker.go"
    - "internal/worker/integration_test.go"
    - "Makefile"
decisions:
  - "Use JSON StdinMessage envelope for all Redis stdin pub/sub (matches transport contract)"
  - "StdinOnce=true on container config: delivers EOF to process when attach closes"
  - "Internal io.Pipe + 500ms flush window: decouples stdin close from stdout read path"
  - "runner.CPUUsageFunc (alias) in DockerSandbox interface: enables runtime type assertion"
  - "CPU evasion test uses heartbeat stdout to reset idle clock while CPU accumulates"
metrics:
  duration: "~45 minutes (implementation + debugging)"
  completed: "2026-06-03"
  tasks_completed: 3
  files_modified: 5
---

# Phase 04 Plan 01: Adversarial Abuse Suite Summary

Implements and runs the adversarial abuse/safety suite (`//go:build abuse`) that proves sandbox containment for all six hostile job scenarios. Discovered and fixed three safety bugs in the process.

## What Was Built

`internal/worker/abuse_test.go` (890 lines) drives 7 hostile Python jobs through the full worker path — Redis queue → worker run loop → DockerSocketRunner with hardening + three clocks → recording publisher — and asserts the published terminal `wire.ResultEvent` flags plus zero container leak.

### Test Cases and Observed Result Flags

| Test | Scenario | Key Flag | Observed Value | Duration |
|------|----------|----------|----------------|----------|
| TEST-01 ForkBomb | Unbounded os.fork() | exitCode != 0 | `exitCode=1, durationMs=66ms` | 2.40s |
| TEST-02 OOM | Allocate 200MB in 64MB sandbox | exitCode=137 (SIGKILL) | `exitCode=137, durationMs=57ms` | 2.36s |
| TEST-03 InfiniteLoop | `while True: pass` | `timedOut=true` | `timedOut=true, durationMs=2001ms` | 4.09s |
| TEST-04 IdleBlockedStdin | `sys.stdin.readline()` no input | `idleTimedOut=true` | `idleTimedOut=true, durationMs=2000ms` | 4.11s |
| TEST-05 EofCleanExit | `for line in stdin` + stdin_close | `exitCode=0, idleTimedOut=false` | `exitCode=0, durationMs=1579ms` | 2.05s |
| TEST-06 GiantOutput | Flood 10MB past 32KB cap | `truncated=true` | `truncated=true, recordedBytes=32768` | 2.05s |
| Bonus CpuClockEvasion | Read byte then `while True: pass` | `timedOut=true` (CPU clock) | `timedOut=true, durationMs=4118ms` | 6.11s |

### Worker Survival (TEST-01, TEST-02)

After each containment kill, a follow-up trivial job (`python -c "print('alive')"`) runs on the same worker instance and asserts `exitCode=0`. Both fork-bomb and OOM proved worker survival.

### Real `make abuse` Output

```
go test -tags=abuse -timeout 600s ./internal/worker/... -run Abuse -v
=== RUN   TestAbuseForkBomb
--- PASS: TestAbuseForkBomb (2.40s)
=== RUN   TestAbuseOOM
--- PASS: TestAbuseOOM (2.36s)
=== RUN   TestAbuseInfiniteLoop
--- PASS: TestAbuseInfiniteLoop (4.09s)
=== RUN   TestAbuseCpuClockEvasion
--- PASS: TestAbuseCpuClockEvasion (6.11s)
=== RUN   TestAbuseIdleBlockedStdin
--- PASS: TestAbuseIdleBlockedStdin (4.11s)
=== RUN   TestAbuseEofCleanExit
--- PASS: TestAbuseEofCleanExit (2.05s)
=== RUN   TestAbuseGiantOutput
--- PASS: TestAbuseGiantOutput (2.05s)
PASS
ok  	github.com/teovillanueva/code-runner/internal/worker	31.651s
```

## Deviations from Plan

### Auto-fixed Safety Bugs Found During Implementation

**1. [Rule 1 - Bug] CPU clock never fires in full worker path (CPUUsageFunc type mismatch)**
- **Found during:** Task 1 (TestAbuseCpuClockEvasion was failing with idleTimedOut instead of timedOut)
- **Root cause:** `DockerSandbox` interface in `worker.go` declared `CPUReader() session.CPUUsageFunc` (a named type), but `*dockerSandbox.CPUReader()` in `runner/docker.go` returns `runner.CPUUsageFunc` (a type alias for the same underlying func). Go interface dispatch requires exact type match; the alias differs from the named type, so `sb.(DockerSandbox)` returned `ok=false` at runtime, silently falling back to a zero-reader that always returned 0ms CPU. The CPU clock polled every 100ms but never exceeded `CpuMs`, so it never fired.
- **Fix:** Changed `DockerSandbox.CPUReader()` to return `runner.CPUUsageFunc` in `worker.go`. Added `var cpuFn runner.CPUUsageFunc` correspondingly. The type assertion now succeeds and the CPU clock works.
- **Files modified:** `internal/worker/worker.go`
- **Verification:** `TestAbuseCpuClockEvasion` changed from `idleTimedOut=true@10s` to `timedOut=true@4s`; `TestAbuseCpuDebug` (direct session test) confirmed CPU stats accumulate correctly.
- **Commit:** `e725241`

**2. [Rule 1 - Bug] All stdin bytes silently dropped in worker+integration tests (missing JSON envelope)**
- **Found during:** Task 1/2 (TestAbuseEofCleanExit, TestAbuseIdleBlockedStdin — stdin never arrived at container)
- **Root cause:** `stdintransport.redis.go Subscribe()` decodes `wire.StdinMessage{Chunk string}` JSON from the Redis payload. Integration tests published raw bytes (`"world\n"`, `"x"`) directly via `redis.Publish`. `json.Unmarshal([]byte("world\n"), &msg)` fails with "invalid character 'w'" and the bytes are silently dropped. The previously-passing integration tests had never actually delivered stdin to the container.
- **Fix:** Added `publishStdin(ctx, rc, jobID, chunk)` helper in `abuse_test.go` that marshals `wire.StdinMessage{Chunk: chunk}` before publishing. Added `publishStdinRaw(t, ctx, rc, jobID, chunk)` helper in `integration_test.go`. Fixed all three integration tests.
- **Files modified:** `internal/worker/abuse_test.go`, `internal/worker/integration_test.go`
- **Impact:** `TestIntegration_InteractivePythonJob` (previously failing silently with 15s timeout) now passes in 2.13s.
- **Commit:** `e725241`

**3. [Rule 1 - Bug] stdin_close (EOF delivery) broken on macOS Docker Desktop — three root causes**
- **Found during:** Task 2 (TestAbuseEofCleanExit — idle clock fires at IdleMs instead of clean exit)
- **Root cause A:** `StdinOnce=false` (Docker default): `OpenStdin=true` + `StdinOnce=false` means Docker keeps the container's stdin pipe open for re-attachment even after the attach connection closes. Python's `for line in sys.stdin` loop never sees EOF. **Fix:** Set `StdinOnce: true` on the container config — Docker now closes the container's stdin pipe when the single attach client disconnects.
- **Root cause B:** Closing `attachResp.Conn` (the hijacked Docker attach) also closes its read direction on macOS Docker Desktop (Unix socket proxy does not support TCP half-close). `stdcopy.StdCopy` reading from `attachResp.Reader` (which wraps the same `Conn`) immediately gets an error, discarding any stdout the process emits after receiving EOF. **Fix:** Internal `io.Pipe` for stdin: writes go through `stdinW`→`stdinR`, a pump goroutine copies to `attachResp.Conn`. When `stdinW.Close()` is called, the pump goroutine waits `stdinEOFFlushDelay=500ms` before closing `attachResp.Conn`, giving the process time to flush its final output through `stdcopy.StdCopy`.
- **Root cause C:** `Stdin()` was designed to be called once for writes and once for close, but the worker calls it once per chunk AND once in `closeStdin()`. The pipe approach requires returning the same `*io.PipeWriter` on every call. **Fix:** `stdinW *io.PipeWriter` stored on `dockerSandbox` struct, returned consistently by `Stdin()`. Pump goroutine created once in `Create()`.
- **Files modified:** `internal/runner/docker.go`
- **Verification:** `TestAbuseEofDebug` (debug test, later removed) showed attach conn closed at t=1.46s of session; with `StdinOnce=true`, Python exited cleanly at t=1.58s. `TestAbuseEofCleanExit` passes with `exitCode=0, idleTimedOut=false`.
- **Commit:** `e725241`

### Plan Deviation: CPU Evasion Test Design

The plan suggested the interaction callback should "periodically publish stdin bytes to reset the idle clock". This is incorrect: the idle clock resets on **stdout** activity, not stdin. Sending stdin bytes to a non-printing process does not reset the idle clock.

**Adjusted design:** The Python program reads one byte (the "interactive" setup) then enters a tight CPU loop while printing heartbeats to stdout every ~500K iterations. The heartbeats reset the idle clock; the CPU clock (2500ms) fires before the wall clock (15000ms) or idle clock (8000ms). The test correctly proves Pitfall 1.

### Plan Deviation: driveJob Event Filtering

The initial `driveJob` helper matched ANY "result" event from the shared triggerer, not just the current job's. The fork-bomb survival job would immediately match the fork-bomb's result event. Fixed: all event lookups in `driveJob` filter by `ev.channel == "private-run-<jobID>"`.

## Self-Check

### Created Files
- `/Users/teovillanueva/code-runner/internal/worker/abuse_test.go` — FOUND (890 lines)
- `/Users/teovillanueva/code-runner/.planning/phases/04-abuse-suite-safety-validation/04-01-SUMMARY.md` — CREATED

### Commits
- `e725241`: `feat(04-01): adversarial abuse suite + safety bug fixes` — FOUND

### make abuse
All 7 Abuse tests pass. `go test ./...` (no tags) remains green (abuse suite excluded by build tag).

## Self-Check: PASSED
