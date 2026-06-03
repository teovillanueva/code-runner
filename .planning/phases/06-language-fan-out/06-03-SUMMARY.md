---
phase: 06-language-fan-out
plan: 03
subsystem: languages/r-4.4
tags: [r, rscript, interpreted, language-package, langfanout, unbuffered-output]
dependency_graph:
  requires: [06-01]
  provides: [r-4.4-language-package, langfanout-shared-helpers]
  affects: [internal/worker/langfanout_shared_test.go]
tech_stack:
  added:
    - r-base:4.4.2 (Docker base image, 847 MB)
    - jsonlite (CRAN, pinned via Posit PPM snapshot 2024-11-01)
    - data.table (CRAN, pinned via Posit PPM snapshot 2024-11-01)
  patterns:
    - R_DEFAULT_PACKAGES=base to suppress popen-blocked startup warnings under cap-drop ALL + seccomp
    - OPENBLAS_NUM_THREADS=1 to prevent pids-limit pthread warnings
    - Rsite.profile addTaskCallback for per-expression stdout flush (belt-and-suspenders)
    - Rscript 4.2+ line-buffers stdout to non-TTY pipe natively (confirmed behaviour)
key_files:
  created:
    - languages/r-4.4/manifest.json
    - languages/r-4.4/Dockerfile
    - internal/worker/langfanout_r_test.go
    - internal/worker/langfanout_shared_test.go
  modified: []
decisions:
  - "Rscript 4.2+ line-buffers stdout to a non-TTY pipe by default — no PYTHONUNBUFFERED-equivalent ENV needed; Rsite.profile addTaskCallback provides belt-and-suspenders flush for connection-based writes"
  - "R_DEFAULT_PACKAGES=base set AFTER the install.packages RUN step: build needs utils; runtime needs suppression of popen-blocked startup warnings under cap-drop ALL + custom seccomp profile"
  - "OPENBLAS_NUM_THREADS=1: prevents harmless-but-noisy pthread_create failure messages when pids-limit=128 is enforced"
  - "langfanout_shared_test.go provides integrationTriggerer and shared helpers under langfanout tag — fixes pre-existing compile failure in langfanout_sqlite_test.go which referenced worker_integration-guarded symbols"
  - "Posit Public Package Manager CRAN snapshot 2024-11-01 for deterministic binary package installs"
metrics:
  duration: 15m
  completed_date: 2026-06-03
  tasks: 2
  files: 4
---

# Phase 06 Plan 03: R 4.4 Language Package Summary

R 4.4 language package with executor/r:4.4 Docker image (r-base:4.4.2, jsonlite+data.table baked, unbuffered streaming via Rsite.profile + OPENBLAS/R_DEFAULT_PACKAGES env tuning), null compile, Rscript main.R run argv, interactive stdin proven end-to-end.

## Tasks Completed

| # | Task | Commit | Key Files |
|---|------|--------|-----------|
| 1 | R 4.4 language package (manifest + Dockerfile) | 93baecb | languages/r-4.4/manifest.json, languages/r-4.4/Dockerfile |
| 1a | Improve image: OPENBLAS, R_DEFAULT_PACKAGES, pids 128 | fb74ab4 | languages/r-4.4/Dockerfile, languages/r-4.4/manifest.json |
| 2 | End-to-end R integration test (batch + interactive) | 832ba5e | internal/worker/langfanout_r_test.go, internal/worker/langfanout_shared_test.go |

## Build Output

```
docker build -t executor/r:4.4 languages/r-4.4/

[1/5] FROM docker.io/library/r-base:4.4.2
[2/5] RUN mkdir -p /etc/R && printf '%s\n' ... > /etc/R/Rsite.profile  DONE 0.3s
[3/5] RUN Rscript -e "install.packages(c('jsonlite','data.table')...)"  DONE 20.9s
[4/5] RUN mkdir -p /workspace && chmod 1777 /workspace               DONE 0.2s
[5/5] WORKDIR /workspace                                              DONE 0.0s
=> writing image sha256:d5484a3dc94e0e56e59aad013c295ffad60c377c9b305a1a29e395994462e42d
=> naming to docker.io/executor/r:4.4
```

## E2E Test Output

```
=== RUN   TestLangFanout_R_Batch
    langfanout_r_test.go:338: TestLangFanout_R_Batch: captured 4 events:
      [private-run-langfanout-r-batch-...] stage: {"phase":"queued"}
      [private-run-langfanout-r-batch-...] stage: {"phase":"running"}
      [private-run-langfanout-r-batch-...] stdout: {"chunk":"hi r\n","seq":1}
      [private-run-langfanout-r-batch-...] result: {"durationMs":56,"exitCode":0,...}
--- PASS: TestLangFanout_R_Batch (2.12s)

=== RUN   TestLangFanout_R_Interactive
    langfanout_r_test.go:473: TestLangFanout_R_Interactive: captured 4 events:
      [private-run-langfanout-r-interactive-...] stage: {"phase":"queued"}
      [private-run-langfanout-r-interactive-...] stage: {"phase":"running"}
      [private-run-langfanout-r-interactive-...] stdout: {"chunk":"hi world\n","seq":1}
      [private-run-langfanout-r-interactive-...] result: {"durationMs":553,"exitCode":0,...}
--- PASS: TestLangFanout_R_Interactive (2.05s)

PASS
ok  github.com/teovillanueva/code-runner/internal/worker  8.644s
```

## go test ./... Output

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

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Pre-existing compile failure in langfanout_sqlite_test.go**
- **Found during:** Task 2 — `go vet -tags=langfanout ./internal/worker/...` failed because `langfanout_sqlite_test.go` (pre-existing) referenced `integrationTriggerer`, `integrationSeccompProfilePath`, `assertNoContainerLeak`, `publishStdinRaw` which are defined only under the `worker_integration` build tag.
- **Fix:** Created `langfanout_shared_test.go` providing these shared symbols under the `langfanout` tag (mirrors `integration_test.go` identically for the symbols used by the SQLite test).
- **Files modified:** `internal/worker/langfanout_shared_test.go` (created)
- **Commits:** 832ba5e

**2. [Rule 1 - Bug] OpenBLAS thread warnings under pids-limit**
- **Found during:** First test run — OpenBLAS tried to spawn 5 threads but the sandbox pids-limit blocked them, emitting noisy `pthread_create failed` messages to stderr.
- **Fix:** Added `ENV OPENBLAS_NUM_THREADS=1` to Dockerfile; bumped pids to 128 in manifest.
- **Files modified:** `languages/r-4.4/Dockerfile`, `languages/r-4.4/manifest.json`
- **Commit:** fb74ab4

**3. [Rule 1 - Bug] R startup warnings: utils/stats packages "not found" under cap-drop ALL + seccomp**
- **Found during:** Second test run — R's `utils` package initialization calls `system("which uname")` via popen, which requires fork+exec. With cap-drop ALL, popen fails with EPERM, causing R to report utils/stats not found at startup (even though R functionality works).
- **Fix:** Added `ENV R_DEFAULT_PACKAGES=base` AFTER the `install.packages` RUN step (the build step needs utils; only the runtime needs the suppression).
- **Files modified:** `languages/r-4.4/Dockerfile`
- **Commit:** fb74ab4

## Zero Core Changes

Git diff for my 3 commits (93baecb, fb74ab4, 832ba5e) touches ONLY:
- `languages/r-4.4/Dockerfile` (+103/-18)
- `languages/r-4.4/manifest.json` (+19/-1)
- `internal/worker/langfanout_r_test.go` (+477/0, new file)
- `internal/worker/langfanout_shared_test.go` (+136/0, new file)

No changes to `internal/runner/`, `internal/worker/worker.go`, `internal/session/`, or any other core files.

## Known Stubs

None. All files are fully functional implementations.

## Threat Flags

No new network endpoints or auth paths. executor/r:4.4 runs with `--network=none`, inherited sandbox hardening (cap-drop ALL, seccomp, read-only rootfs, tmpfs workspace). T-06-03-01 mitigated by inherited three-clock + pids-limit. T-06-03-SC mitigated by pinned r-base:4.4.2 official image.

## Self-Check: PASSED

- languages/r-4.4/manifest.json exists: FOUND
- languages/r-4.4/Dockerfile exists: FOUND
- internal/worker/langfanout_r_test.go exists: FOUND
- internal/worker/langfanout_shared_test.go exists: FOUND
- Commit 93baecb: FOUND
- Commit fb74ab4: FOUND
- Commit 832ba5e: FOUND
- docker image executor/r:4.4: FOUND (sha256:d5484a3...)
- go test ./...: PASS
- TestLangFanout_R_Batch: PASS
- TestLangFanout_R_Interactive: PASS
