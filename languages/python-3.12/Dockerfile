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

# ── Bake in data-science / HTTP libraries at build time ───────────────────────
# Versions pinned to known-good releases.  These are top-tier PyPI packages
# (T-03-SC); --no-cache-dir keeps the layer lean.
# Runtime containers are --network=none, so all deps MUST be here (T-03-04).
RUN pip install --no-cache-dir \
    numpy==2.2.6 \
    pandas==2.2.3 \
    requests==2.32.3

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
