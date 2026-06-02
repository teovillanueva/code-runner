---
phase: 03-interactive-python-e2e
plan: "03"
subsystem: infra
tags: [docker, python, python3.12, numpy, pandas, requests, sandbox, image]

requires:
  - phase: 03-interactive-python-e2e
    provides: manifest.json with image=executor/python:3.12 and run=["python","main.py"]

provides:
  - "languages/python-3.12/Dockerfile: executor/python:3.12 image with numpy/pandas/requests baked in, PYTHONUNBUFFERED=1, /workspace world-writable"
  - "languages/python-3.12/.dockerignore: minimal build context"

affects:
  - 03-interactive-python-e2e
  - future language fan-out plans (add-language pattern established)

tech-stack:
  added:
    - "python:3.12-slim base image"
    - "numpy==2.2.6"
    - "pandas==2.2.3"
    - "requests==2.32.3"
  patterns:
    - "Language image pattern: FROM slim + baked deps + PYTHONUNBUFFERED=1 + /workspace chmod 1777 + WORKDIR /workspace + no CMD"
    - "No USER instruction in image: runner enforces --user 65534:65534 at container start time"
    - "All deps baked at build time; runtime --network=none prevents any fetch"

key-files:
  created:
    - languages/python-3.12/Dockerfile
    - languages/python-3.12/.dockerignore

key-decisions:
  - "No USER instruction in Dockerfile: runner (docker.go) overrides to 65534:65534 at run time; baking a USER would conflict or be redundant"
  - "chmod 1777 on /workspace: world-writable sticky bit allows runner UID 65534 to write without a dedicated system user in the image"
  - "PYTHONUNBUFFERED=1 via ENV (not -u flag): manifest run=[python,main.py] stays clean; env var is sufficient and covers all code paths"
  - "PYTHONDONTWRITEBYTECODE=1: prevents .pyc write failures since rootfs is read-only at runtime (--read-only in runner)"
  - "Pinned library versions (numpy 2.2.6, pandas 2.2.3, requests 2.32.3): reproducible builds, avoids silent regressions"

requirements-completed: [LANG-04, LANG-05]

duration: 12min
completed: 2026-06-02
---

# Phase 03 Plan 03: Python 3.12 Image Summary

**executor/python:3.12 image with numpy/pandas/requests baked in, PYTHONUNBUFFERED=1 for interactive streaming, /workspace world-writable for runner UID 65534**

## Performance

- **Duration:** ~12 min
- **Started:** 2026-06-02T00:00:00Z
- **Completed:** 2026-06-02T00:12:00Z
- **Tasks:** 2 (Task 1: author Dockerfile + Task 2: verify unbuffered streaming + manifest agreement)
- **Files modified:** 2

## Accomplishments

- Dockerfile for `executor/python:3.12` based on `python:3.12-slim` with numpy 2.2.6, pandas 2.2.3, requests 2.32.3 baked in (all pinned, --no-cache-dir)
- `PYTHONUNBUFFERED=1` set via ENV — the locked streaming requirement ensuring interactive stdout is not block-buffered when not attached to a TTY
- `/workspace` created with `chmod 1777` so the runner's runtime UID 65534 can write files copied in by `CopyToContainer` before start
- Image tag `executor/python:3.12` matches `manifest.json` image field exactly
- `docker build` exits 0; numpy/pandas/requests import under `--user 65534:65534 --network=none`; unflushed `print()` appears on stdout

## Task Commits

1. **Task 1+2: Python 3.12 Dockerfile (unbuffered, non-root, libs baked in) + verification** - `834d132` (feat)

## Files Created/Modified

- `languages/python-3.12/Dockerfile` - Python 3.12-slim image with baked libs, PYTHONUNBUFFERED=1, /workspace 1777, WORKDIR /workspace
- `languages/python-3.12/.dockerignore` - Exclude everything except Dockerfile from build context

## Decisions Made

- **No USER instruction:** The runner (`internal/runner/docker.go` `sandboxUser = "65534:65534"`) overrides the user at `ContainerCreate` time via `container.Config.User`. Adding a `USER` directive in the Dockerfile would be either redundant or conflicting. Omitting it keeps the image generic and avoids any potential UID collision.
- **chmod 1777 on /workspace:** The runner's `copyFilesToContainer` uses `CopyToContainer` which writes as root into the container filesystem before start. With `ReadonlyRootfs=true`, files are injected as a tar overlay before the process starts. Making /workspace world-writable with sticky bit allows UID 65534 to read and write without requiring a dedicated `nobody` user entry in /etc/passwd.
- **PYTHONUNBUFFERED via ENV not -u flag:** The manifest `run` field is `["python","main.py"]` (no `-u`). Using ENV means buffering is disabled for any Python invocation in the container, including the manifest run command, without modifying the manifest.
- **PYTHONDONTWRITEBYTECODE=1:** The runner sets `ReadonlyRootfs=true`. Python by default writes `__pycache__/*.pyc` on import. On a read-only rootfs this would cause an `EROFS` error on first import. This env var prevents that failure.
- **Pinned exact versions:** Reproducible builds and protection against supply-chain drift for the core data-science stack.

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## Threat Model Coverage

| Threat ID | Status | Notes |
|-----------|--------|-------|
| T-03-SC | Mitigated | numpy/pandas/requests pinned to exact versions; --no-cache-dir keeps layers clean |
| T-03-03 | Mitigated | /workspace world-writable; runner enforces --user 65534, cap-drop ALL, no-new-privileges at run time |
| T-03-04 | Mitigated | All libs baked at build time; runtime containers use --network=none |

## Docker Build + Test Outputs

**Build (final lines):**
```
#7 [4/4] WORKDIR /workspace
#7 DONE 0.0s
#8 exporting to image
#8 writing image sha256:92a25a7cea01fff8de728c1389c997f68fb036fea630f5d509367d67009ee5c5 done
#8 naming to docker.io/executor/python:3.12 done
#8 DONE 0.5s
```

**Import verification (--user 65534:65534 --network=none):**
```
$ docker run --rm --network=none --user 65534:65534 executor/python:3.12 python -c "import numpy,pandas,requests; print('ok')"
ok
```

**Non-root user verification:**
```
$ docker run --rm --user 65534:65534 executor/python:3.12 id -u
65534
```

**Unbuffered print verification:**
```
$ docker run --rm executor/python:3.12 python -c "print('first', flush=False)"
first
```

## User Setup Required

None - this is a pure Docker image build; no external service configuration required.

## Next Phase Readiness

- `executor/python:3.12` is built, tagged, and verified locally
- The image satisfies all runner requirements: non-root-accessible /workspace, PYTHONUNBUFFERED=1, matching manifest tag/run
- LANG-04 (per-request limits override) is a runtime path requiring no image change — confirmed
- Plan 05 (`make python-image` Makefile target) can reference this Dockerfile path
- The language-image pattern established here (slim base + baked deps + PYTHONUNBUFFERED + /workspace 1777 + no CMD) should be replicated for Rust/R/SQLite images in Phase 6

---
*Phase: 03-interactive-python-e2e*
*Completed: 2026-06-02*
