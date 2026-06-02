---
phase: 02-sandbox-hardening-runner
plan: 01
subsystem: sandbox
tags: [go, docker, seccomp, clocks, pump, sync.Once, cgroup, testify]

requires:
  - phase: 01-foundation-wire-contract
    provides: runner.Sandbox interface, runner.Result, wire.Limits types

provides:
  - Phase 2 Go deps (docker/docker v28.5.2, pusher-http-go/v5 v5.1.1, stretchr/testify v1.11.1) in go.mod
  - Restrictive seccomp allowlist (profiles/seccomp/runner.json) forking Docker default with dangerous syscalls removed
  - internal/session: Docker-free, race-clean testable session safety logic
  - session.Run / session.RunWithTruncated: unified entry point for the three-clock supervisor
  - session.NewPump: byte-capped always-draining output pump with shared budget and truncated flag
  - sync.Once idempotent teardown funneling every terminal path through a single terminate()

affects:
  - 02-02 (publisher): uses session for output activity signals, truncated flag
  - 02-03 (docker runner): plugs real cgroup CPU stats via CPUUsageFunc hook
  - 02-04 (integration): runs the whole stack including session lifecycle

tech-stack:
  added:
    - github.com/docker/docker v28.5.2+incompatible
    - github.com/pusher/pusher-http-go/v5 v5.1.1
    - github.com/stretchr/testify v1.11.1
  patterns:
    - Injectable CPUUsageFunc hook: real cgroup poller (plan 02-03) and fake functions in tests
    - sync.Once + done channel: idempotent teardown with early-exit for all clock goroutines
    - Shared atomic.Int64 budget + atomic.Bool truncated: thread-safe combined output cap across stdout+stderr pumps
    - TDD: RED commit (failing tests) → GREEN commit (passing implementation) per task
    - tools/tools.go with //go:build tools build tag to anchor deps not yet imported by production code

key-files:
  created:
    - internal/session/doc.go
    - internal/session/clocks.go
    - internal/session/lifecycle.go
    - internal/session/clocks_test.go
    - internal/session/lifecycle_test.go
    - internal/session/pump.go
    - internal/session/pump_test.go
    - profiles/seccomp/runner.json
    - tools/tools.go
  modified:
    - go.mod
    - go.sum

key-decisions:
  - "CPUUsageFunc is an injected func(ctx)(int,error) so the real cgroup stats poller (plan 02-03) plugs in without docker/docker import in session"
  - "sync.Once + done channel pattern: terminate() closes done after sending to resultCh so clock goroutines exit cleanly without consuming the result"
  - "tools/tools.go with //go:build tools anchors docker/pusher/testify deps before production code imports them (wave-1 owns go.mod for all Phase 2)"
  - "Pump.Run always drains to EOF past cap (anti-deadlock per PITFALLS pitfall 6); CompareAndSwap on shared truncated flag prevents double-set"
  - "Session Kill is always called on every terminal path including normal exit — safe per Sandbox.Kill contract (idempotent, callable after Wait)"

patterns-established:
  - "Fake Sandbox pattern: scriptable Kill/Cleanup counts, Wait delay/result, nopWriteCloser for stdin"
  - "Shared atomic budget: pass *atomic.Int64 + *atomic.Bool to multiple Pumps for combined cap"
  - "Done channel: terminate() closes s.done so all goroutines can select on it without consuming resultCh"

requirements-completed: [LIM-01, LIM-02, LIM-03, LIM-04, LIFE-01, LIFE-02]

duration: 25min
completed: 2026-06-02
---

# Phase 2 Plan 01: Deps + Seccomp + Session Safety Logic Summary

**Three independent clocks (wall/idle/CPU), byte-capped always-draining output pump, and sync.Once idempotent teardown in Docker-free testable internal/session package with restrictive seccomp profile**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-06-02T19:40:00Z
- **Completed:** 2026-06-02T20:06:10Z
- **Tasks:** 3
- **Files created:** 9

## Accomplishments

- Phase 2 Go deps locked in go.mod (docker SDK, pusher SDK, testify) via tools/tools.go anchor before any production code imports them
- Restrictive seccomp allowlist forked from Docker default — dangerous syscalls absent: ptrace, mount, bpf, keyctl, userfaultfd, clone3, unshare, setns, kexec_load, perf_event_open, add_key, request_key, umount2; clone restricted to no-namespace-create mask; socket restricted to AF_UNIX
- internal/session fully implemented: wall clock (unconditional), idle clock (resets on stdout activity), CPU clock (polls injected CPUUsageFunc every 100ms), shared output budget pump (always drains past cap), sync.Once terminate() with done-channel shutdown
- 16 tests, all passing with -race; no docker/docker import in the package

## Task Commits

1. **Task 1: Add Phase 2 deps + restrictive seccomp profile** - `077446d` (chore)
2. **Task 2: Output pump RED** - `2993501` (test)
3. **Task 2: Output pump GREEN** - `c2e31ce` (feat)
4. **Task 3: Three clocks + lifecycle RED** - `0d4f624` (test)
5. **Task 3: Three clocks + lifecycle GREEN** - `f3ffa50` (feat)

## Files Created/Modified

- `go.mod` - Added docker/docker, pusher-http-go/v5, stretchr/testify direct requires + transitive deps
- `go.sum` - Frozen checksums for all Phase 2 deps
- `tools/tools.go` - //go:build tools anchor to retain deps before production imports
- `profiles/seccomp/runner.json` - Restrictive seccomp allowlist, defaultAction SCMP_ACT_ERRNO, dangerous syscalls removed
- `internal/session/doc.go` - Package overview: design contract, pitfall mitigations, no-Docker guarantee
- `internal/session/lifecycle.go` - session struct, Run/RunWithTruncated entry points, sync.Once terminate()
- `internal/session/clocks.go` - runWallClock, runIdleClock (reset on activity), runCPUClock (injected CPUUsageFunc)
- `internal/session/pump.go` - NewPump, Run: shared atomic budget, always-drain, activity signals
- `internal/session/pump_test.go` - 5 pump tests including blocking-reader drain proof
- `internal/session/clocks_test.go` - 8 clock/teardown tests including idempotency double-fire race
- `internal/session/lifecycle_test.go` - 3 lifecycle tests: DurationMs, Cleanup on timeout, activity resets idle

## Decisions Made

- CPUUsageFunc injection pattern: keeps internal/session Docker-free; plan 02-03 plugs in the real cgroup reader
- sync.Once + done channel: `terminate()` sends Result to resultCh THEN closes done; clock goroutines select on done not resultCh (preserves the single receiver invariant)
- tools/tools.go: standard Go tools-package pattern to pin deps that wave-2 plans (02-02, 02-03) will need without race on go.mod
- Pump.Run loops on io.Read even after budget is exhausted (discards bytes); upstream pipes never block regardless of cap status

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed io.NopCloser used as io.WriteCloser in fakeSandbox**
- **Found during:** Task 3 RED tests (clocks_test.go compilation)
- **Issue:** `io.NopCloser(bytes.NewReader(nil))` returns `io.ReadCloser`, not `io.WriteCloser`; compilation failed
- **Fix:** Added `nopWriteCloser` struct implementing `io.WriteCloser` with discard Write and no-op Close
- **Files modified:** internal/session/clocks_test.go
- **Committed in:** 0d4f624 (RED gate commit)

---

**Total deviations:** 1 auto-fixed (1 bug in test setup)
**Impact on plan:** Trivial fix to test double type mismatch. No scope changes.

## Issues Encountered

None beyond the io.WriteCloser type mismatch documented above.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- **02-02 (Publisher):** Can consume the activity channel pattern from session.Pump to signal soketi output events
- **02-03 (Docker Runner):** Provides the real `CPUUsageFunc` reading cgroup cpu.stat; wires `session.Run` as the actual Wait() implementation
- **02-04 (Integration):** All session safety logic is testable; integration tests add real Docker containers

## Threat Flags

No new network endpoints, auth paths, file access patterns, or schema changes introduced. The seccomp profile is a hardening artifact (T-02-03). The output pump mitigates T-02-01. The clocks mitigate T-02-02.

## Self-Check: PASSED

- internal/session/doc.go: FOUND
- internal/session/clocks.go: FOUND
- internal/session/lifecycle.go: FOUND
- internal/session/pump.go: FOUND
- profiles/seccomp/runner.json: FOUND
- go.mod contains docker/docker: FOUND
- Commits 077446d, 2993501, c2e31ce, 0d4f624, f3ffa50: all present in git log

---
*Phase: 02-sandbox-hardening-runner*
*Completed: 2026-06-02*
