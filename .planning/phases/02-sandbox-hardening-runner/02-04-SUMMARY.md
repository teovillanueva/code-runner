---
phase: 02-sandbox-hardening-runner
plan: "04"
subsystem: runner
tags: [docker, integration-tests, hardening, clocks, cgroup-v2, seccomp]
dependency_graph:
  requires: ["02-03"]
  provides: ["docker-integration-tests", "hardening-proof", "clock-proof"]
  affects: ["internal/runner"]
tech_stack:
  added: []
  patterns:
    - "//go:build docker + runtime t.Skip guard for two-gate test exclusion"
    - "ContainerInspect to assert applied HostConfig fields (not just trust the create call)"
    - "ContainerList(filters.Args) with code-runner.jobId label for no-leak assertion"
    - "session.Run to drive real clocks against real containers"
    - "extractCPUReader type-assertion to access dockerSandbox.CPUReader() without importing internal types"
key_files:
  created:
    - internal/runner/testhelpers_test.go
    - internal/runner/docker_integration_test.go
  modified:
    - Makefile
    - internal/runner/docker.go
decisions:
  - "stdin round-trip uses sb.Wait() directly (not session.Run) to avoid race between test's direct Stdout() read and session.Run's pump goroutine consuming the same pipe"
  - "seccomp fix: read JSON file at NewDockerSocketRunner time and embed inline in SecurityOpt (Docker Desktop VM cannot access macOS host paths; Docker CLI does the same)"
  - "testImage=alpine:3.20 — already present on this machine, smallest image that has cat/sleep/yes/dd/sh"
  - "cpuReaderSandbox interface for type-asserting CPUReader() without importing unexported dockerSandbox"
metrics:
  duration_minutes: 35
  completed_date: "2026-06-02"
  tasks_completed: 3
  files_changed: 4
---

# Phase 02 Plan 04: Docker Integration Tests Summary

Build-tagged Docker integration tests proving hardening, three clocks, output truncation, stdin round-trip, and no-leak against real cgroup-v2 containers — plus a runner bug fix (seccomp profile inline embedding).

## Tasks Completed

| Task | Description | Commit |
|------|-------------|--------|
| 1 | Test harness (testhelpers_test.go, //go:build docker guard) + Makefile target | 7faef87 |
| 2 | Runner bug fix: seccomp JSON inline embedding | 2b8fc4a |
| 3 | Integration tests: hardening inspect, stdin round-trip, tree-kill, three clocks, truncation | bd2e367 |

## What Was Built

### `internal/runner/testhelpers_test.go` (169 lines, //go:build docker)

- `requireDocker(t)` — constructs moby client; `t.Skip` when daemon unreachable (two-gate guard: build tag AND runtime check)
- `testImage = "alpine:3.20"` — smallest image already on this machine
- `dockerOnce` + `sync.Once` — image pull at most once per test binary run
- `newTestRunner(t)` — builds `DockerSocketRunner` with `config.Default()` and bundled seccomp profile
- `buildSpec(jobID, run, limits)` — creates `wire.JobSpec` with unique jobId, testImage, and given limits
- `assertNoLeak(t, cli, jobID)` — `ContainerList(All:true, filters.Args code-runner.jobId=<id>)` asserts zero survivors
- `testLimitsDefault()` — standard small limits for integration tests

### `internal/runner/docker_integration_test.go` (464 lines, //go:build docker)

- `TestIntegrationHardeningFlags`: ContainerInspect asserts all HARD-01..05 applied verbatim
- `TestIntegrationStdinRoundtrip`: write "hello integration\n", read back byte-for-byte, close stdin, verify clean exit
- `TestIntegrationTreeKillNoLeak`: Kill a sleep 300 container, assertNoLeak confirms zero labeled survivors
- `TestIntegrationWallClock`: sleep 999 + 2s wall clock → TimedOut=true, terminates in <10s
- `TestIntegrationIdleClock`: stdin-blocked cat + 2s idle clock → IdleTimedOut=true
- `TestIntegrationCpuClock`: read-one-byte-then-spin + 1s CPU budget vs 15s wall → TimedOut=true, terminates in <12s (cgroup CPU accounting confirmed working)
- `TestIntegrationOutputTruncation`: yes floods past 4KiB cap → Truncated=true

### `Makefile`

- Added `test-docker` target: `go test -tags=docker -timeout 300s ./internal/runner/... -run Integration -v`
- Mirrors style of existing `abuse` target; does not collide

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Seccomp profile path fails on Docker Desktop (macOS)**

- **Found during:** Task 2 — all tests failed with `Decoding seccomp profile failed: invalid character '/' looking for beginning of value`
- **Issue:** The runner passed `seccomp=/absolute/path/to/runner.json` as a SecurityOpt string. The moby SDK sends this verbatim to the daemon. On Docker Desktop, the daemon runs in a Linux VM and cannot resolve macOS host filesystem paths — it tries to parse the path string as JSON and fails.
- **Root cause:** The original code comment in `docker.go` said "dockerd reads the seccomp profile path" — true for the Docker CLI (which reads+embeds the file), but incorrect for the SDK (which sends the string verbatim).
- **Fix:** `NewDockerSocketRunner` now reads the seccomp profile JSON at construction time (`os.ReadFile`) and stores the JSON content instead of the path. At container creation, it passes `seccomp=<json_content>` inline. This is identical to how `docker run --security-opt seccomp=...` works (the CLI reads the file and embeds the JSON before sending to the daemon).
- **Verified:** `docker inspect` output after the fix confirms `SecurityOpt` contains the full JSON, matching CLI behavior.
- **Files modified:** `internal/runner/docker.go`
- **Commit:** 2b8fc4a

**2. [Rule 1 - Bug] stdin round-trip test design: direct Stdout() read races with session.Run pump**

- **Found during:** Task 3 design — `session.Run` creates pump goroutines that consume `Stdout()`. If both the test and `session.Run` try to read from the same `io.PipeReader`, one wins and the other gets nothing.
- **Fix:** The stdin round-trip test uses `sb.Wait()` directly (the normal-exit path) rather than `session.Run`. This avoids the race: the test reads stdout itself, closes stdin, then waits for the container via `ContainerWait`. No session pump involvement.
- **Files modified:** `internal/runner/docker_integration_test.go`
- **Commit:** bd2e367

## Test Results (real `make test-docker` output)

```
go test -tags=docker -timeout 300s ./internal/runner/... -run Integration -v
=== RUN   TestIntegrationHardeningFlags
--- PASS: TestIntegrationHardeningFlags (1.39s)
=== RUN   TestIntegrationStdinRoundtrip
--- PASS: TestIntegrationStdinRoundtrip (10.21s)
=== RUN   TestIntegrationTreeKillNoLeak
--- PASS: TestIntegrationTreeKillNoLeak (0.22s)
=== RUN   TestIntegrationWallClock
--- PASS: TestIntegrationWallClock (2.25s)
=== RUN   TestIntegrationIdleClock
--- PASS: TestIntegrationIdleClock (2.28s)
=== RUN   TestIntegrationCpuClock
--- PASS: TestIntegrationCpuClock (2.37s)
=== RUN   TestIntegrationOutputTruncation
--- PASS: TestIntegrationOutputTruncation (6.52s)
PASS
ok      github.com/teovillanueva/code-runner/internal/runner    25.603s
```

7/7 tests pass. Total suite time: 25.6s on cgroup-v2 Docker Desktop (macOS arm64).

## Requirements Verified End-to-End on cgroup v2

| Requirement | Test | Assertion |
|-------------|------|-----------|
| RUN-02: hardened sandbox | TestIntegrationHardeningFlags | ContainerInspect confirms all flags |
| RUN-03: stdin/stdout pipes | TestIntegrationStdinRoundtrip | exact byte round-trip |
| RUN-04: tree-kill | TestIntegrationTreeKillNoLeak | no labeled container survives Kill |
| HARD-01: NetworkMode=none | TestIntegrationHardeningFlags | hc.NetworkMode == "none" |
| HARD-02: ReadonlyRootfs + tmpfs | TestIntegrationHardeningFlags | ReadonlyRootfs=true, /tmp with size= |
| HARD-03: Memory==MemorySwap | TestIntegrationHardeningFlags | hc.Memory == hc.MemorySwap > 0 |
| HARD-04: PidsLimit + NanoCPUs | TestIntegrationHardeningFlags | PidsLimit>0, NanoCPUs>0 |
| HARD-05: CapDrop+seccomp+no-new-priv | TestIntegrationHardeningFlags | CapDrop=[ALL], SecurityOpt includes both |
| LIM-01: wall clock | TestIntegrationWallClock | TimedOut=true in <10s |
| LIM-02: idle clock | TestIntegrationIdleClock | IdleTimedOut=true in <15s |
| LIM-03: CPU clock | TestIntegrationCpuClock | TimedOut=true by CPU before wall clock |
| LIM-04: output cap | TestIntegrationOutputTruncation | Truncated=true |
| LIFE-01/02: cleanup / no-leak | all tests | assertNoLeak confirms no labeled survivors |

## Threat Surface Scan

No new network endpoints, auth paths, or schema changes introduced. Test files are compile-time excluded from production builds via build tag.

## Known Stubs

None. All test assertions exercise real behavior against real containers.

## Self-Check: PASSED

- [x] `internal/runner/testhelpers_test.go` exists (169 lines > 0)
- [x] `internal/runner/docker_integration_test.go` exists (464 lines > 120 ✓)
- [x] Both files begin with `//go:build docker`
- [x] `Makefile` contains `test-docker` target
- [x] `go test ./internal/runner/ -count=1` (no tag) exits 0 (Docker-free)
- [x] `make test-docker` exits 0, 7/7 tests pass
- [x] ContainerInspect asserts: NetworkMode none, ReadonlyRootfs, tmpfs size=, Memory==MemorySwap>0, PidsLimit, NanoCPUs, CapDrop ALL, no-new-privileges, seccomp, user 65534:65534
- [x] stdin round-trip verified byte-for-byte
- [x] Kill confirms no labeled container survives
- [x] Three clocks (wall/idle/CPU) each kill a real container
- [x] Output truncation produces Truncated=true
- [x] Commits 7faef87, 2b8fc4a, bd2e367 present in git log
- [x] Runner bug fixed: seccomp JSON embedded inline (not as path)
