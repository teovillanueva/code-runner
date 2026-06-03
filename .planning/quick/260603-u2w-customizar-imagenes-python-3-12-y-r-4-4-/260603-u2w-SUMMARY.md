---
quick_id: 260603-u2w
type: execute
subsystem: infra
tags: [docker, python, r, matplotlib, ggplot2, picos, glpk, lpSolve, sandbox, artifacts]

provides:
  - Extended executor/python:3.12 image with the university-exam scientific stack (scipy, statsmodels, scikit-learn, seaborn, cvxopt, swiglpk, PICOS) + matplotlib figure auto-capture
  - Extended executor/r:4.4 image with lpSolve + ggplot2 + a lazy /workspace png device (zero blank artifacts)
affects: [language-fan-out, artifacts-pullable-run-output, exam-grading-migration]

tech-stack:
  added:
    - "Python: scipy 1.15.3, statsmodels 0.14.4, scikit-learn 1.6.1, seaborn 0.13.2, cvxopt 1.3.2, swiglpk 5.0.12, PICOS 2.6.0"
    - "R: lpSolve, ggplot2 (Posit snapshot 2024-11-01)"
  patterns:
    - "sitecustomize.py __import__ wrapper to defer-register an atexit figure saver after matplotlib loads"
    - "R options(device=<lazy png opener>) as the lazy single-device capture mechanism (supersedes the planned before.plot.new hook)"

key-files:
  created:
    - languages/python-3.12/sitecustomize.py
    - languages/r-4.4/Rprofile.site
  modified:
    - languages/python-3.12/Dockerfile
    - languages/python-3.12/.dockerignore
    - languages/r-4.4/Dockerfile

key-decisions:
  - "Use options(device=<png opener>) instead of the plan's locked before.plot.new hook — the hook left a stray blank Rplots.pdf because R_DEFAULT_DEVICE=pdf auto-opens the pdf default device BEFORE any plot.new hook fires."
  - "swiglpk bundles GLPK; PICOS solves with solver='glpk' out of the box, so the Python image needs ZERO new apt deps."
  - "wrap invisible(local({...})) in Rprofile.site so the sourced profile does not print a stray NULL to stdout on every R run."

requirements-completed: [G1, G2, G3]

duration: 22min
completed: 2026-06-03
---

# Quick 260603-u2w: Customize Python 3.12 + R 4.4 sandbox images Summary

**Baked the exam scientific stack into both sandbox images and made matplotlib/R plots auto-capture to `/workspace` as artifacts with ZERO blank-PNG/PDF leakage — verified by a full direct `docker run` smoke matrix against the freshly built images.**

## Performance

- **Duration:** ~22 min
- **Started:** 2026-06-03T19:50:53Z
- **Completed:** 2026-06-03 (~20:13Z)
- **Tasks:** 3 of 3
- **Files modified:** 5 (2 created, 3 modified)

## Accomplishments

- `executor/python:3.12` now imports scipy/statsmodels/scikit-learn/seaborn/picos and solves a GLPK LP via PICOS (optimum 8.0), and auto-captures open matplotlib figures to `/workspace/figure_NNN.png` without `savefig()`.
- `executor/r:4.4` now loads `lpSolve` AND `ggplot2` (G3) and auto-captures `plot()` output to `/workspace/RplotNNN.png` via a LAZY device — no plot means no file (G1), and NO stray `Rplots.pdf` is ever produced.
- Both `manifest.json` files are byte-identical to before (G2); image tags `executor/python:3.12` / `executor/r:4.4` unchanged. All changes confined to `languages/`.

## Task Commits

Each task was committed atomically (code only — docs handled by the orchestrator):

1. **Task 1: Extend Python image (science stack + auto-capture sitecustomize)** — `0aa9487` (feat)
2. **Task 2: Extend R image (lpSolve + ggplot2 + auto-sourced Rprofile.site)** — `d276500` (feat)
3. **Task 3 deviation fix: R lazy device via `options(device=)` to drop stray `Rplots.pdf`** — `9e0a0ac` (fix)

Task 3 itself ran the smoke matrix only (no source files of its own); its work surfaced one Rule-1 bug in Task 2's `Rprofile.site`, fixed in `9e0a0ac`.

## Files Created/Modified

- `languages/python-3.12/Dockerfile` — added the science-stack pip pins (scipy/statsmodels/scikit-learn/seaborn/cvxopt/swiglpk/PICOS), `ENV OMP_NUM_THREADS=1` / `OPENBLAS_NUM_THREADS=1` / `MPLCONFIGDIR=/tmp/mplconfig`, and `COPY sitecustomize.py …`.
- `languages/python-3.12/sitecustomize.py` (new) — wraps `builtins.__import__`; on the first `matplotlib*` import, unregisters matplotlib's `Gcf.destroy_all` atexit handler and registers a saver that writes every open figure to `/workspace/figure_{NNN}.png` then destroys them. No matplotlib import ⇒ no saver ⇒ zero figures.
- `languages/python-3.12/.dockerignore` — added `!sitecustomize.py` so the COPY source is in the build context.
- `languages/r-4.4/Dockerfile` — removed the DEAD `/etc/R/Rsite.profile` RUN block, added `lpSolve` + `ggplot2` to `install.packages`, added `COPY Rprofile.site /etc/R/Rprofile.site`, added `ENV XDG_CACHE_HOME=/tmp`. Kept `R_DEFAULT_DEVICE=pdf`, `OPENBLAS_NUM_THREADS=1`, and `R_DEFAULT_PACKAGES`.
- `languages/r-4.4/Rprofile.site` (new) — `invisible(local({…}))` with: per-expression flush `addTaskCallback`; `options(device=<lazy png opener>)` (single `/workspace/Rplot%03d.png` device opened on the first plot, overriding `R_DEFAULT_DEVICE=pdf` so no pdf is created); and an `onexit` `reg.finalizer` that closes the device on normal AND error exit.

## Reported Metrics

| Image | Final size |
|-------|-----------|
| `executor/python:3.12` | **689 MB** |
| `executor/r:4.4` | **888 MB** |

**Python heavy-stack cold-start** (wall time to `import numpy,pandas,scipy,statsmodels,sklearn,seaborn,picos,matplotlib.pyplot`), 3 runs: **1.434 s / 1.305 s / 1.227 s** (~1.3 s typical).

## Smoke Matrix Results (direct `docker run`, UID 65534)

All cases run against the freshly built images. Python with `-m 1024m`, R with `-m 512m` and `-e R_DEFAULT_PACKAGES=base,grDevices,graphics` (memory overridden at run time only — manifests untouched, G2).

| Case | Description | Result |
|------|-------------|--------|
| a | `import scipy, statsmodels, sklearn, seaborn, picos` exits 0 | **PASS** |
| b | GLPK LP via PICOS (`solver='glpk'`), optimum = 8.0 | **PASS** (printed `OPT 8.0`) |
| c | 2 matplotlib figures, no `savefig` ⇒ exactly 2 `/workspace/figure_*.png` | **PASS** (figure_001/002.png) |
| c2 | No matplotlib import ⇒ ZERO `figure_*.png` | **PASS** (0 files) |
| d | `lpSolve` solves an LP and prints the solution | **PASS** (status 0, optimum 12, solution `4 0`) |
| d2 | `ggplot2` loads, exits 0 (G3) | **PASS** (`ggplot2 OK`) |
| e | 2 `plot()` calls, no device opened ⇒ exactly 2 `/workspace/Rplot*.png` | **PASS** (Rplot001/002.png, NO Rplots.pdf) |
| e2 | Empty/no-plot R script ⇒ ZERO `Rplot*.png` (G1 lazy device) | **PASS** (0 files, no artifacts at all) |
| e3 | One `plot()` then `stop('boom')` ⇒ the in-progress frame still flushed | **PASS** (Rplot001.png written, exit 1, NO pdf) |

Additional sanity: 3 `plot()` calls ⇒ `Rplot001/002/003.png` (single-device page numbering). The plan's exact Task-3 automated gate printed **`SMOKE PASS`**.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Stray `NULL` printed to stdout by the R site profile**
- **Found during:** Task 2 verification.
- **Issue:** Sourcing `Rprofile.site` at top level auto-printed the visible value of the `local({...})` block (the finalizer's `NULL`), so every R run's stdout began with a stray `NULL` line.
- **Fix:** Wrapped the block in `invisible(local({...}))`.
- **Files modified:** `languages/r-4.4/Rprofile.site`
- **Commit:** folded into `d276500`.

**2. [Rule 1 - Bug] Lazy device left a blank `Rplots.pdf` artifact on every plotting run (G1 violation)**
- **Found during:** Task 3 smoke verification (cases e / e3).
- **Issue:** The plan's locked `setHook("before.plot.new", …)` mechanism (fact B) did not account for the baked `ENV R_DEFAULT_DEVICE=pdf`. When user code plots with no device open, R auto-opens the DEFAULT device (pdf ⇒ `/workspace/Rplots.pdf`) BEFORE any `plot.new`/`before.plot.new` hook fires. The hook then opened a SECOND png device, so each plotting run leaked a blank `Rplots.pdf` that the worker captures as a top-level `/workspace` artifact — contradicting G1's "zero blank artifacts" intent.
- **Fix:** Replaced the `before.plot.new` hook with `options(device = <lazy png opener>)`. The `device` option takes precedence over the `R_DEFAULT_DEVICE` env var, so the auto-opened default device IS our `/workspace/Rplot%03d.png` png — no pdf is ever created. Kept `R_DEFAULT_DEVICE=pdf` baked in the Dockerfile per the locked Task-2 constraint. Re-verified the full R matrix: N plots ⇒ N PNGs, 0 plots ⇒ 0 PNGs, error-after-first-plot ⇒ that frame flushed, and NO `Rplots.pdf` in any case.
- **Files modified:** `languages/r-4.4/Rprofile.site`
- **Commit:** `9e0a0ac`

No other deviations. Python pins all resolved as manylinux wheels with no conflicts (no pin loosening needed). No new apt deps were required for either image.

## Locked Decisions Honored

- **G1 (lazy device, zero blank artifacts):** R no-plot run ⇒ 0 PNGs and 0 pdf; Python no-matplotlib run ⇒ 0 figures. The deviation strengthens G1 (eliminates the previously-leaked `Rplots.pdf`).
- **G2 (manifests untouched):** both `manifest.json` byte-identical (`git diff --exit-code` clean); memoryMb 128/256 and `interactive:true` preserved; smoke tests overrode memory via `docker run -m` only. Image tags `executor/python:3.12` / `executor/r:4.4` unchanged.
- **G3 (ggplot2 included):** `library(ggplot2)` loads with exit 0.

Scope guard: `git diff --name-only` against the base commit shows changes ONLY under `languages/python-3.12/` and `languages/r-4.4/` — nothing in `apps/worker`, `apps/api`, or `packages/contract`.

## Known Stubs

None.

## Self-Check: PASSED

- `languages/python-3.12/sitecustomize.py` — FOUND
- `languages/r-4.4/Rprofile.site` — FOUND
- `languages/python-3.12/Dockerfile`, `languages/r-4.4/Dockerfile` — FOUND (modified)
- Commit `0aa9487` — FOUND
- Commit `d276500` — FOUND
- Commit `9e0a0ac` — FOUND
