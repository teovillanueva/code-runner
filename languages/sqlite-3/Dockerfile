# SQLite 3 sandbox image for code-runner
#
# Security model:
#   - Runs as UID 65534 (nobody) at runtime; the runner overrides with
#     --user 65534:65534 at container start.
#   - /workspace is world-readable/writable (1777) so the runner can
#     CopyToContainer user files (e.g. main.sql) before the process starts.
#   - sqlite3 is fully baked in at build time; runtime containers run with
#     --network=none so no external fetch is possible (T-06-04-02).
#   - No CMD/ENTRYPOINT: the runner supplies argv from the manifest run field
#     ["sqlite3", "-batch", ":memory:", "-init", "main.sql"].
#   - :memory: DB is ephemeral — destroyed when sqlite3 exits. No host data
#     is reachable (T-06-04-02: read-only rootfs + network=none).

FROM alpine:3.20

# ── Install sqlite3 ───────────────────────────────────────────────────────────
# The alpine package repo provides an official sqlite build.
# --no-cache avoids saving the apk index layer, keeping the image small.
RUN apk add --no-cache sqlite

# ── Workspace directory ────────────────────────────────────────────────────────
# The runner (internal/runner/docker.go) sets WorkingDir=/workspace and
# tar-copies user files (main.sql) here before starting the process.
# Permissions 1777 (world-writable sticky) allow UID 65534 to create files
# without requiring a dedicated system user in the image.
RUN mkdir -p /workspace && chmod 1777 /workspace

WORKDIR /workspace

# No USER instruction: the runner enforces --user 65534:65534 at run time.
# No CMD/ENTRYPOINT: the runner supplies argv from the manifest run field.
