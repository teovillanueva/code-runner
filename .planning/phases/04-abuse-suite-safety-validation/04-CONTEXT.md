# Phase 4: Abuse Suite & Safety Validation - Context

**Gathered:** 2026-06-03
**Status:** Ready for planning
**Mode:** Auto-generated (discuss skipped — autonomous run)

<domain>
## Phase Boundary

Build the abuse/safety test suite that adversarially proves the sandbox guarantees through the FULL worker path (Redis → worker → DockerSocketRunner + session three clocks + publisher), and wire it as a Linux CI gate. This suite is the verification backbone and GATES the language fan-out (Phase 6) — it is built EARLY (now, right after the Python E2E), per the spec.

Phase 2 already added runner-level Docker integration tests (hardening flags, each clock, truncation, no-leak). Phase 4 is the ADVERSARIAL end-to-end suite: real hostile programs submitted as jobs, asserting they are contained and the worker survives, plus the CI workflow to run it on real Linux cgroup v2.
</domain>

<decisions>
## Implementation Decisions

### The abuse cases (TEST-01..06), each run through the worker as a real Python job
- **TEST-01 fork bomb:** a program that spawns processes unboundedly (e.g. `os.fork()` loop / threads) → contained by `--pids-limit`; the sandbox is killed/limited cleanly and the WORKER SURVIVES (assert worker still processes a subsequent job). result shows non-zero/killed, no leak.
- **TEST-02 OOM:** allocate memory past `memoryMb` → killed by the memory cap (cgroup v2 OOM kills the cgroup); worker survives; result reflects the kill.
- **TEST-03 infinite loop (no stdin):** `while True: pass` → killed by the WALL clock; `result.timedOut=true`.
- **TEST-04 stdin-blocked (idle):** reads stdin that never arrives → killed by the IDLE clock; `result.idleTimedOut=true`.
- **TEST-05 EOF:** reads stdin to EOF; driver sends a chunk then `stdin_close` → program terminates correctly (exitCode 0), NOT idle-timed-out.
- **TEST-06 giant output:** floods stdout past `outputKb` → `result.truncated=true`, memory not exhausted, worker keeps draining (process doesn't block/deadlock).
- (Bonus, reuse Phase 2 intent) **CPU-clock evasion:** "read one byte then spin" → killed by the CPU clock, not just the wall clock.

### Mechanics
- Prefer driving each case through `internal/worker` over a real `redis:7` + Docker + `executor/python:3.12`, asserting the published `result` event flags and that NO container/volume leaks (filter label `code-runner.jobId`). Reuse the worker integration harness/fake-publisher from Phase 3 (`internal/worker/integration_test.go`, `publisher.NewForTest`).
- Guard with a build tag (e.g. `//go:build abuse`) + runtime skip when Docker/Redis unavailable, so plain `go test ./...` stays green. Provide `make abuse` (the Makefile already has an `abuse` target — align it to the actual tag/path).
- Use SMALL limits in tests (low wallMs/idleMs/cpuMs/memoryMb/outputKb) so the suite is fast (seconds, not minutes).
- Keep each abuse program as a small inline Python source or a testdata file submitted as `wire.FileInput`.

### CI (TEST-07, TEST-08)
- A GitHub Actions workflow (`.github/workflows/abuse.yml` or a job in a CI workflow) that runs on `ubuntu-latest` (real Linux, cgroup v2), sets up Go, builds the `executor/python:3.12` image, starts a `redis` service, and runs `make abuse`. This is the gate that proves real cgroup OOM/CPU behavior (not just macOS Docker Desktop). TEST-07.
- Document/encode that this suite gates the language fan-out: TEST-08 — e.g. the CI workflow runs on PRs and the fan-out phase depends on it being green; a note in the suite/README.
</decisions>

<canonical_refs>
## Canonical References — downstream agents MUST read
- `internal/worker/worker.go` + `internal/worker/integration_test.go` (the worker path + existing integration harness to extend)
- `internal/runner/docker.go`, `internal/runner/docker_integration_test.go` (Phase 2 hardening tests — model for assertions)
- `internal/session` (clocks: timedOut vs idleTimedOut vs truncated semantics)
- `internal/publisher/testing.go` (`NewForTest`, recording publisher)
- `packages/contract/gen/go/wire` (`ResultEvent` flags)
- `.planning/research/PITFALLS.md` ("looks done but isn't" abuse checklist; cgroup v1/v2 OOM scope; fork bomb; output flood)
- `Makefile` (existing `abuse` target), `languages/python-3.12/` (image)
- `CLAUDE.md`
</canonical_refs>

<specifics>
## Specific Ideas

Phase 4 requirement IDs: TEST-01, TEST-02, TEST-03, TEST-04, TEST-05, TEST-06, TEST-07, TEST-08.

**ACTUALLY RUN the suite** on this machine (Docker cgroup v2 available, image built, redis runnable) and confirm every case passes — these are the most important tests in the project. Paste real output. Then ensure the CI workflow is valid (yaml lint / `act`-style sanity if available, else careful authoring) since it can't execute here without a GitHub runner.
</specifics>

<deferred>
## Deferred Ideas

Slot/capacity/429 backpressure and the dead-worker reaper (Phase 5). The broader CI matrix (lint, unit, contract-drift, multi-job) is Phase 7 — Phase 4 only needs the abuse-on-Linux gate. Rust/R/SQLite abuse coverage comes when those languages land (Phase 6), reusing this harness.
</deferred>
