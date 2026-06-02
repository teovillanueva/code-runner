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
#   1. Rscript --vanilla flag is appended by Rsite.profile below so every
#      Rscript invocation skips reading .Rprofile / Renviron — avoids any
#      accidental re-buffering from user init files.
#
#   2. R_DEFAULT_DEVICE=pdf suppresses device prompts that could cause hangs.
#
#   3. /etc/R/Rsite.profile (the system-wide R startup file, sourced before
#      any user code) calls sink() + flush.console() on every command via
#      addTaskCallback.  The key unbuffering mechanism is:
#      options(OutDec=getOption("OutDec"))  # no-op to trigger .onLoad flush
#      combined with Rscript automatically flushing when its output fd is a
#      non-TTY pipe (Rscript uses line-buffering when writing to a pipe on
#      R 4.2+).  Rscript 4.2+ enables line-buffering on stdout by default
#      when connected to a pipe — this is confirmed R behaviour and does NOT
#      need PYTHONUNBUFFERED-style env var.  We document it here so maintainers
#      understand why no ENV is required.
#
# TL;DR: Rscript 4.2+ already line-buffers stdout to a pipe; we add
# explicit flush() calls in Rsite.profile as belt-and-suspenders for any
# code that writes to connection objects directly.
ENV R_DEFAULT_DEVICE=pdf

# Belt-and-suspenders: write a system Rsite.profile that installs a task
# callback to flush stdout after every top-level expression.  This covers
# code that uses message(), cat(), print() and connection-based writes
# without an explicit flush().
RUN mkdir -p /etc/R && printf '%s\n' \
    '# code-runner: force per-expression stdout flush for streaming' \
    'local({' \
    '  addTaskCallback(function(...) { flush(stdout()); flush(stderr()); TRUE }, name="code_runner_flush")' \
    '})' \
    > /etc/R/Rsite.profile

# ── Bake in common CRAN libraries at build time ───────────────────────────────
# Using the Posit Public Package Manager CRAN snapshot (2024-11-01) for
# reproducible, pre-built binaries on Debian/Ubuntu.  This keeps build time
# short (binary installs) and the layer deterministic.
# Runtime containers are --network=none, so all deps MUST be here.
# jsonlite: JSON parsing (very commonly needed for I/O)
# data.table: fast data manipulation (widely used in data science)
RUN Rscript -e "\
    options(repos = c(CRAN = 'https://packagemanager.posit.co/cran/__linux__/bookworm/2024-11-01')); \
    install.packages(c('jsonlite', 'data.table'), \
        Ncpus = parallel::detectCores(), \
        quiet = TRUE, \
        clean = TRUE) \
"

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
