# R 4.4 sandbox image for code-runner
#
# Security model:
#   - Runs as UID 65534 (nobody) at runtime; the runner overrides with
#     --user 65534:65534 at container start.
#   - /workspace is world-readable/writable so the runner can tar-copy
#     user files into it (CopyToContainer) before the process starts.
#   - R_DEFAULT_DEVICE=pdf and --no-save flags are set to avoid interactive
#     graphics device prompts and saving sessions.
#   - All libs are baked in at build time; runtime containers run with
#     --network=none so no install.packages fetch is possible at runtime.
#   - No CMD/ENTRYPOINT: the runner supplies argv from the manifest
#     run field (["Rscript", "main.R"]).

FROM r-base:4.4.2

# ── Unbuffered output (LOCKED streaming requirement) ─────────────────────────
# R buffers stdout when stdout is not attached to a TTY (e.g. inside a
# Docker container pipe).  Without forcing line-buffered / unbuffered mode,
# interactive streaming would stall until the 4-8 KB user-space buffer fills.
#
# Three-layer defence (all applied here):
#
#   1. R_DEFAULT_DEVICE=pdf suppresses device prompts that could cause hangs.
#
#   2. /etc/R/Rprofile.site (the system-wide R startup file, auto-sourced before
#      any user code) calls flush(stdout())+flush(stderr()) after every
#      top-level expression via addTaskCallback.  This is the belt-and-suspenders
#      flush for code that writes to connection objects without an explicit flush().
#      (Carried forward from the old, DEAD /etc/R/Rsite.profile — R never sourced
#      a file by that name, so that callback never actually ran; it lives in the
#      real auto-sourced Rprofile.site now.)
#
#   3. Rscript 4.2+ already line-buffers stdout to a non-TTY pipe by default —
#      this is confirmed R behaviour and does NOT need PYTHONUNBUFFERED-style
#      env var.  The explicit flush callback in Rprofile.site covers edge cases.
#
# TL;DR: Rscript 4.2+ line-buffers stdout to a pipe; we add explicit flush()
# calls in Rprofile.site as belt-and-suspenders.
ENV R_DEFAULT_DEVICE=pdf

# OPENBLAS_NUM_THREADS=1: prevent OpenBLAS from trying to spawn worker threads
# inside the sandbox (which fails with pids-limit enforcement, producing noisy
# "pthread_create failed" warnings to stderr on every Rscript invocation).
# R does not need BLAS parallelism for typical scripts; single-threaded BLAS is
# the correct choice for an untrusted-code sandbox.
ENV OPENBLAS_NUM_THREADS=1

# XDG_CACHE_HOME=/tmp: silence a cosmetic fontconfig stderr warning under the
# read-only sandbox (UID 65534 home is /nonexistent). Harmless either way — PNGs
# render regardless — but keeps sandbox stderr clean. /tmp is a writable tmpfs.
ENV XDG_CACHE_HOME=/tmp

# ── Bake in common CRAN libraries at build time ───────────────────────────────
# Using the Posit Public Package Manager CRAN snapshot (2024-11-01) for
# reproducible, pre-built binaries on Debian/Ubuntu.  This keeps build time
# short (binary installs) and the layer deterministic.
# Runtime containers are --network=none, so all deps MUST be here.
# jsonlite: JSON parsing (very commonly needed for I/O)
# data.table: fast data manipulation (widely used in data science)
# lpSolve: linear-programming solver (university-exam optimization problems)
# ggplot2: grammar-of-graphics plotting (G3) — pulls deps, all from the same snapshot
RUN set -eu; \
    Rscript -e "\
    options(repos = c(CRAN = 'https://packagemanager.posit.co/cran/__linux__/bookworm/2024-11-01')); \
    install.packages(c('jsonlite', 'data.table', 'lpSolve', 'ggplot2'), \
        Ncpus = parallel::detectCores(), \
        quiet = TRUE, \
        clean = TRUE) \
"; \
    # ── In-RUN slim: strip help/HTML/vignette/man payloads from the packages we
    #    just installed (Rscript never serves interactive help). Only the
    #    site-library populated by THIS layer can shrink; r-base's own docs live
    #    in lower layers and stay. Saves a chunk of the ggplot2 dependency tree. ──
    for lib in /usr/local/lib/R/site-library /usr/lib/R/site-library; do \
      [ -d "$lib" ] || continue; \
      find "$lib" -mindepth 2 -maxdepth 2 -type d \
        \( -name help -o -name html -o -name doc -o -name man -o -name tests -o -name demo \) \
        -prune -exec rm -rf '{}' +; \
    done; \
    rm -rf /var/lib/apt/lists/* /tmp/* /root/.cache 2>/dev/null || true; \
    # Build-time smoke test (LOCKED): every baked CRAN lib must load AFTER the
    # slim, or the build fails — never ship an R image where library() crashes
    # because the slim pruned something a package needs at load time.
    Rscript -e "library(jsonlite); library(data.table); library(lpSolve); library(ggplot2); cat('cr: R baked-lib smoke ok\n')"

# ── Auto-source the site profile: lazy png device + flush + finalizer ─────────
# /etc/R/Rprofile.site is the ONLY auto-sourced R site profile (R_HOME's
# etc/Rprofile.site symlinks here on Debian r-base). COPY (preserves 0644 so the
# runtime UID 65534 can read it). It opens a /workspace png device LAZILY on the
# first real plot (so a no-plot run emits ZERO PNGs) and flushes the in-progress
# frame on normal AND error exit.
COPY Rprofile.site /etc/R/Rprofile.site

# ── Zygote agent (Phase 11, P1 — WIP / Docker-tier fallback) ──────────────────
# The R zygote agent + its native helper. ONLY invoked when the worker launches
# this image as a long-lived, PRIVILEGED pool container with an explicit command:
#   docker run -d --privileged --cgroupns=host -p 7000:7000 \
#       executor/r:4.4 Rscript /opt/zygote/zygote_agent.R
# The image's normal run path (DockerSocketRunner -> `Rscript main.R`) is
# UNCHANGED (still no CMD/ENTRYPOINT). The .so is compiled at build time via
# `R CMD SHLIB`. NOTE: the embedded-R fork callback is WIP and NOT yet validated;
# the recommendation is to ship R on the Docker tier (see
# .planning/decisions/ZYGOTE-R-STATUS.md). The compile below proves the helper
# builds; it does NOT run it.
RUN mkdir -p /opt/zygote
COPY zygote_hard.c /opt/zygote/zygote_hard.c
COPY zygote_agent.R /opt/zygote/zygote_agent.R
RUN set -eu; \
    cd /opt/zygote; \
    R CMD SHLIB zygote_hard.c -o libzygote_hard.so; \
    test -f /opt/zygote/libzygote_hard.so; \
    echo "cr: zygote R native helper compiled ok"

# R_DEFAULT_PACKAGES: restrict default package loading to a reconciled list.
# Must be set AFTER the install.packages step above (which requires the utils
# package to be available during the build).
#
# In the hardened sandbox (--cap-drop ALL + custom seccomp profile), R's 'utils'
# package initialization calls system("which uname") via popen(), which requires
# fork+exec in a subprocess.  With all capabilities dropped, this popen fails with
# EPERM, producing confusing "package utils/stats not found" warnings to stderr on
# every Rscript invocation even though R itself works correctly.
#
# R11 (Phase 9): a file graphics device must be usable for plot artifacts.  The
# base r-base:4.4.2 image already reports capabilities()[c('png','cairo')] == TRUE
# (cairo is present — NO extra apt graphics libs are required).  The only missing
# piece is that grDevices (the device, e.g. png()) and graphics (high-level plot()
# / lines() / etc.) are not auto-loaded under a bare 'base' default.  grDevices
# and graphics do NOT trigger the popen("which uname") path — ONLY 'utils' does
# (see the EPERM rationale above) — so adding them back keeps the hardened-sandbox
# stderr SILENT while making png('chart.png'); plot(...); dev.off() work end to end.
# 'utils'/'stats'/'methods'/'datasets' stay OUT to preserve that silence.
#
# No auto-device / show()-interception shim (D-10): the convention is that users
# open a file device with a RELATIVE name into the cwd (/workspace), e.g.
#   png('chart.png'); plot(1:3); dev.off()
# and the worker's workspace-diff capture (Phase-9 plan 04) picks chart.png up as
# an artifact.  R_DEFAULT_DEVICE stays pdf (above) — moot here since the user opens
# png() explicitly.  The baked-in packages (jsonlite, data.table) are unaffected;
# all baked at build, runtime stays --network=none.
ENV R_DEFAULT_PACKAGES=base,grDevices,graphics

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
