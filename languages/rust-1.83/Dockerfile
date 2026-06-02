# Rust 1.83 sandbox image for code-runner
#
# Security model:
#   - Runs as UID 65534 (nobody) at runtime; the runner overrides with
#     --user 65534:65534 at container start.
#   - /workspace is world-readable/writable so the runner can tar-copy
#     user files into it (CopyToContainer) before the process starts.
#   - rustc and the standard library ship in the base image; no crate
#     vendoring at build time (deferred to v2). Runtime containers run with
#     --network=none so no cargo fetch is possible.
#   - No CMD/ENTRYPOINT: the runner supplies argv from the manifest fields
#     (compile: ["rustc", "-O", "main.rs", "-o", "/tmp/prog"],
#      run:     ["/tmp/prog"]).
#   - /tmp is mounted as a writable tmpfs by the runner at run time; the
#     compile output (/tmp/prog) persists in the same container for the
#     subsequent run step.

FROM rust:1.83-slim

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
# /tmp is provided as a writable tmpfs by the runner; rustc writes the
# compiled binary there and the run step executes it.
