---
phase: 02-sandbox-hardening-runner
plan: "03"
subsystem: runner
tags: [docker, runner, sandbox, hardening, cgroup, cpu-clock]
dependency_graph:
  requires: ["02-01"]
  provides: ["DockerSocketRunner", "cgroup-v2-cpu-reader"]
  affects: ["apps/worker", "internal/session"]
tech_stack:
  added: []
  patterns:
    - "docker moby SDK ContainerCreate/ContainerAttach/ContainerStart/ContainerWait/ContainerKill/ContainerRemove"
    - "stdcopy.StdCopy demux for multiplexed attach streams"
    - "sync.Once idempotent Cleanup"
    - "ContainerStatsOneShot for cgroup-v2 CPU usage polling"
key_files:
  created:
    - internal/runner/docker.go
    - internal/runner/docker_unit_test.go
    - internal/runner/cgroup.go
    - internal/runner/cgroup_test.go
  modified: []
decisions:
  - "docker.go does NOT import internal/session (would create import cycle: session imports runner). Instead, dockerSandbox.Wait uses ContainerWait for the normal-exit path; the worker layer calls session.Run(ctx, sandbox, limits, cpuReader)"
  - "CPUReader() and Limits() accessors added to dockerSandbox so the worker can pass them to session.Run without type-asserting internal struct fields"
  - "CPUUsageFunc defined as a type alias in runner package (not imported from session) to break the cycle while remaining assignment-compatible with session.CPUUsageFunc"
  - "docker_unit_test.go placed in package runner_test (external) rather than package runner to avoid import cycle in tests"
  - "WorkingDir=/workspace with tar-based file copy via CopyToContainer before ContainerStart"
metrics:
  duration_minutes: 8
  completed_date: "2026-06-02"
  tasks_completed: 3
  files_changed: 4
---

# Phase 02 Plan 03: DockerSocketRunner Summary

DockerSocketRunner implementing runner.Runner/Sandbox over the moby SDK with full HARD-01..05 hardening, cgroup-v2 CPU reader, stdcopy demux, tree-kill, and sync.Once idempotent cleanup.

## Tasks Completed

| Task | Description | Commit |
|------|-------------|--------|
| 1 | cgroup-v2 CPU usage reader (TDD: RED + GREEN) | 024c021 + 0b8b44e |
| 2 | DockerSocketRunner.Create full hardening + attach + demux + labels | d8657f3 |
| 3 | Wait (ContainerWait) + Kill (tree-kill) + Cleanup (sync.Once) + unit tests | dc155e6 |

## What Was Built

### `internal/runner/cgroup.go`
- `usageNanosToMs(nanos uint64) int` — pure conversion helper (Docker stats TotalUsage nanos → ms)
- `extractCPUMs(stats StatsResponse) int` — pure helper for unit tests
- `newContainerCPUReader(cli, containerID) CPUUsageFunc` — returns the polling function for session CPU clock
- `CPUUsageFunc` type alias (avoids importing session, compatible with session.CPUUsageFunc)

### `internal/runner/docker.go`
- `DockerSocketRunner` — implements `Runner` via moby SDK
- `NewDockerSocketRunner(cfg, seccompProfilePath)` — builds client with DockerHost override + version negotiation
- `Create(ctx, spec)` — full HARD-01..05 hardening in one unconditional code path:
  - HARD-01: `NetworkMode="none"`
  - HARD-02: `ReadonlyRootfs=true` + `Tmpfs{"/tmp": "rw,noexec,nosuid,size=Nm"}`
  - HARD-03: `Memory == MemorySwap` (no swap)
  - HARD-04: `PidsLimit`, `NanoCPUs=1_000_000_000` (1 CPU ceiling)
  - HARD-05: `CapDrop=["ALL"]`, `SecurityOpt=["no-new-privileges","seccomp=<path>"]`
  - Non-root user `65534:65534`; runtime override from `cfg.SandboxRuntime`
  - `code-runner.jobId` label on every container
  - `ContainerAttach` with Stream+Stdin+Stdout+Stderr
  - `stdcopy.StdCopy` goroutine demuxes multiplexed stream into two `io.Pipe` pairs
  - Tar-based file copy to `/workspace` via `CopyToContainer` before `ContainerStart`
  - No docker.sock in any Binds/Volumes/Mounts
- `dockerSandbox.Wait` — `ContainerWait` (normal-exit path only; session.Run called by worker)
- `dockerSandbox.Kill` — `ContainerKill("KILL")` + `ContainerRemove(Force:true)` (tree-kill, no bare PID)
- `dockerSandbox.Cleanup` — `sync.Once` idempotent: closes hijacked conn + pipe readers + force-removes container (ignores not-found)
- `CPUReader()`, `Limits()` accessors for worker layer

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Import cycle: runner → session → runner**

- **Found during:** Task 3 implementation
- **Issue:** `session.Run` accepts `runner.Sandbox` (session imports runner). If `dockerSandbox.Wait` called `session.Run`, the import would create a cycle.
- **Fix:** `dockerSandbox.Wait` uses `ContainerWait` for the normal-exit path only. The worker layer calls `session.Run(ctx, sandbox, limits, cpuReader)`. Added `CPUReader()` and `Limits()` accessors to `dockerSandbox` so the worker has all inputs for `session.Run` without needing Docker-specific types.
- **Files modified:** `internal/runner/docker.go`
- **Commit:** d8657f3, dc155e6

**2. [Rule 1 - Bug] CPUUsageFunc type in cgroup.go avoids session import**

- **Found during:** Task 1 — cgroup.go initially imported `session.CPUUsageFunc`, creating the same cycle.
- **Fix:** Defined `CPUUsageFunc = func(ctx context.Context) (cpuMs int, err error)` as a type alias in `cgroup.go`. Assignment-compatible with `session.CPUUsageFunc` (same underlying type).
- **Files modified:** `internal/runner/cgroup.go`
- **Commit:** 0b8b44e

**3. [Rule 1 - Bug] docker_unit_test.go: package runner → package runner_test**

- **Found during:** Task 3 — test file in `package runner` importing `session` recreated the cycle in test mode.
- **Fix:** Moved tests to `package runner_test` (external test package). Replaced all internal struct access with a `closureSandbox` test double implementing `runner.Sandbox`.
- **Files modified:** `internal/runner/docker_unit_test.go`
- **Commit:** dc155e6

## Threat Surface Scan

No new threat surfaces introduced beyond those addressed in the plan's threat model:
- T-02-08 (privilege escalation): mitigated — CapDrop ALL + no-new-privileges + seccomp + non-root unconditionally applied
- T-02-09 (network escape): mitigated — NetworkMode=none on every container
- T-02-10 (DoS via resources): mitigated — Memory==MemorySwap, PidsLimit, NanoCPUs
- T-02-11 (DoS via process tree): mitigated — Kill = ContainerKill + ContainerRemove(force)
- T-02-12 (docker.sock exposure): mitigated — no Binds/Volumes referencing socket
- T-02-13 (container leak): mitigated — sync.Once Cleanup + jobId label

## Test Results

```
go test ./internal/runner/ -count=1 -race
ok  github.com/teovillanueva/code-runner/internal/runner  (all 17 tests pass)

go test ./... -count=1 -race
ok  all packages (no Docker required)
```

## Self-Check: PASSED

- [x] `internal/runner/docker.go` exists (398 lines, min_lines=150 ✓)
- [x] `internal/runner/cgroup.go` exists (61 lines, min_lines=30 ✓)
- [x] `internal/runner/docker_unit_test.go` exists
- [x] `internal/runner/cgroup_test.go` exists
- [x] Commits 024c021, 0b8b44e, d8657f3, dc155e6 present in git log
- [x] `go build ./...` exits 0
- [x] `go test ./internal/runner/ -count=1` exits 0 (Docker-free)
- [x] `go test ./... -race -count=1` exits 0
- [x] Phase 1 stub and its tests remain intact and passing
- [x] `var _ Runner = (*DockerSocketRunner)(nil)` and `var _ Sandbox = (*dockerSandbox)(nil)` in docker.go
- [x] seccomp=<path>, no-new-privileges, CapDrop=ALL, NetworkMode=none, Memory==MemorySwap, PidsLimit, NanoCPUs all set in one unconditional code path
- [x] stdcopy.StdCopy demux wired
- [x] code-runner.jobId label on every container
- [x] No docker.sock in any mounts
- [x] Kill = ContainerKill + ContainerRemove(force); no Process.Kill/bare-PID path
- [x] Cleanup is sync.Once idempotent
- [x] TestCleanupIdempotent: 5× calls → body runs once
- [x] TestCleanupIdempotent_ViaSessionTeardown: racing Kill+Wait → each fired once
