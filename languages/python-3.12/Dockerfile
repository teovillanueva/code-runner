# Python 3.12 sandbox image for code-runner
#
# Security model:
#   - Runs as UID 65534 (nobody) at runtime; the runner overrides with
#     --user 65534:65534 at container start.
#   - /workspace is world-readable/writable so the runner can tar-copy
#     user files into it (CopyToContainer) before the process starts.
#   - PYTHONUNBUFFERED=1 is a LOCKED requirement: interactive streaming
#     depends on stdout not being block-buffered (Pitfall 6 / CONTEXT.md).
#   - All libs are baked in at build time; runtime containers run with
#     --network=none so no pip fetch is possible (T-03-04).
#   - No CMD/ENTRYPOINT: the runner supplies argv from the manifest
#     run field (["python", "main.py"]).

FROM python:3.12-slim

# ── Unbuffered output (LOCKED streaming requirement) ─────────────────────────
# PYTHONUNBUFFERED=1: disable Python's block-buffering of stdout/stderr when
# not attached to a TTY. Without this, interactive output is silently held in
# a 4–8 KB user-space buffer and the client sees nothing until the buffer fills
# or the process exits.
ENV PYTHONUNBUFFERED=1

# PYTHONDONTWRITEBYTECODE=1: don't write .pyc files. The rootfs is read-only
# at runtime (--read-only), so writing bytecache would fail; skip it.
ENV PYTHONDONTWRITEBYTECODE=1

# ── Headless matplotlib backend (plot FILES, no display, no shim) ─────────────
# MPLBACKEND=Agg selects the non-interactive Anti-Grain Geometry raster backend
# at import time, so matplotlib never tries to open a GUI/display inside the
# sandbox (there is none — --network=none, no X server, read-only rootfs).
# With Agg, savefig('plot.png') renders straight to a file; the interactive
# display call is intentionally a NO-OP (the figure never leaves Python memory,
# so there is no syscall to hook). Per D-10 we deliberately ship NO
# display-interception shim (no auto-import startup hook, no module:// capture
# backend) — the convention is: save plots EXPLICITLY with a RELATIVE name into
# the cwd (/workspace), e.g. savefig('plot.png'). The worker's workspace-diff
# capture (Phase-9 plan 04) then picks the new file up as an artifact. No
# --network needed.
ENV MPLBACKEND=Agg

# ── Single-threaded numeric stack (sandbox pids-limit friendly) ───────────────
# OMP_NUM_THREADS / OPENBLAS_NUM_THREADS = 1: keep BLAS/OpenMP single-threaded so
# the science stack (numpy/scipy/sklearn) does not spawn a worker-thread pool
# inside the sandbox. Under the runtime pids limit + --cap-drop ALL, spawning
# pthreads either fails noisily or wastes the small pid budget; untrusted exam
# code does not need BLAS parallelism. Mirrors the R image's OPENBLAS rationale.
ENV OMP_NUM_THREADS=1
ENV OPENBLAS_NUM_THREADS=1

# MPLCONFIGDIR=/tmp/mplconfig: the runtime UID 65534 has home /nonexistent, so
# matplotlib would print a noisy "mkdir failed for /nonexistent/.config/matplotlib"
# warning and fall back to a temp dir. Point its cache at /tmp (a writable tmpfs
# in the sandbox; rootfs is read-only) to silence that warning. matplotlib only
# needs a writable cache dir and degrades gracefully if it is absent.
ENV MPLCONFIGDIR=/tmp/mplconfig

# ── Bake in data-science / HTTP / plotting libraries at build time ────────────
# Versions pinned to known-good releases.  These are top-tier PyPI packages
# (T-03-SC); --no-cache-dir keeps the layer lean.
# Runtime containers are --network=none, so all deps MUST be here (T-03-04).
# matplotlib 3.10.3 ships manylinux wheels and is compatible with numpy 2.2.6;
# under MPLBACKEND=Agg (above) it renders plot files headlessly with no GUI deps.
#
# University-exam scientific stack (all manylinux wheels, verified compatible
# with numpy 2.2.6 / matplotlib 3.10.3 / pandas 2.2.3, no conflicts):
#   scipy, statsmodels, scikit-learn, seaborn, cvxopt, swiglpk, PICOS.
# swiglpk BUNDLES GLPK, so PICOS solves LPs with solver='glpk' out of the box —
# NO apt libglpk package is needed and the image requires ZERO new apt deps.
# NOTE for the executor: network IS available during `docker build`; if any single
# pin ever fails to resolve, loosen that one pin to the nearest compatible release.
RUN set -eu; \
    pip install --no-cache-dir \
      numpy==2.2.6 \
      pandas==2.2.3 \
      requests==2.32.3 \
      matplotlib==3.10.3 \
      scipy==1.15.3 \
      statsmodels==0.14.4 \
      scikit-learn==1.6.1 \
      seaborn==0.13.2 \
      cvxopt==1.3.2 \
      swiglpk==5.0.12 \
      PICOS==2.6.0; \
    # ── In-RUN slim: only what we install here can be shrunk (a later-layer rm
    #    leaves the bytes in this layer, bloating the pull AND the worker's baked
    #    store). Drop the package test suites, bytecode caches, and bundled
    #    Fortran/C/Cython sources — none are imported by user code at runtime.
    #    CRITICAL: numpy's tests/ dirs MUST be kept. `numpy.testing` imports
    #    numpy/_core/tests/_natype, so pruning numpy's tests breaks scipy /
    #    scikit-learn / statsmodels / seaborn at import (they transitively pull
    #    numpy.testing). Excluding numpy from the dir-prune costs a few MB and
    #    buys a working scientific stack. --network=none at runtime is unaffected. ──
    sp="$(python -c 'import site; print(site.getsitepackages()[0])')"; \
    find "$sp" -depth -type d \( -name tests -o -name test -o -name __pycache__ \) -not -path '*/numpy/*' -prune -exec rm -rf '{}' +; \
    find "$sp" -type f \( -name '*.pyc' -o -name '*.pyx' -o -name '*.pxd' -o -name '*.c' -o -name '*.cpp' -o -name '*.f' -o -name '*.f90' \) -delete; \
    rm -rf /root/.cache; \
    # Build-time import smoke test (LOCKED): a broken prune MUST fail the build,
    # never ship a sandbox image where the advertised sci stack crashes at import.
    python -c "import numpy, numpy.testing, scipy, sklearn, statsmodels.api, seaborn, matplotlib; matplotlib.use('Agg'); from matplotlib import pyplot; print('cr: scientific-stack import smoke ok')"

# ── Auto-capture matplotlib figures as /workspace artifacts ───────────────────
# COPY (after the pip install so site-packages exists) the sitecustomize.py hook
# into site-packages; Python auto-imports `sitecustomize` at interpreter startup
# with no env var. It captures any open matplotlib figures to /workspace/figure_*.png
# at shutdown WITHOUT the user calling savefig(). COPY preserves 0644 so the
# runtime UID 65534 can read it. A run that never imports matplotlib emits zero
# figures (the saver only registers on the first matplotlib import).
COPY sitecustomize.py /usr/local/lib/python3.12/site-packages/sitecustomize.py

# ── Workspace directory ────────────────────────────────────────────────────────
# The runner (internal/runner/docker.go) sets WorkingDir=/workspace and
# tar-copies user files here before starting the process.
# Permissions 1777 (world-writable sticky) allow the runtime UID 65534 to
# create files without requiring a dedicated system user in the image, while
# the sticky bit prevents other UIDs from removing each other's files.
RUN mkdir -p /workspace && chmod 1777 /workspace

WORKDIR /workspace

# No USER instruction: the runner enforces --user 65534:65534 at run time
# (sandboxUser constant in internal/runner/docker.go). Baking a USER here
# would conflict with the runtime override and is not required.
# /workspace is world-writable so UID 65534 can write to it.
