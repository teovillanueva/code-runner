# C++ (G++ 14) sandbox image for code-runner
#
# Security model:
#   - Runs as UID 65534 (nobody) at runtime; the runner overrides with
#     --user 65534:65534 at container start.
#   - /workspace is world-readable/writable so the runner can tar-copy
#     user files into it (CopyToContainer) before the process starts.
#   - Only the toolchain and the standard library are installed; runtime
#     containers run with --network=none, so no package fetch is possible.
#   - No CMD/ENTRYPOINT: the runner supplies argv from the manifest fields
#     (compile writes /workspace/prog, run executes it).
#   - The run step wraps the binary in `stdbuf -oL` so stdout is line-buffered
#     on the attach pipe: interactive prompts (printf/cout without an explicit
#     flush) reach the client before the program blocks on stdin.
#   - /tmp is a noexec tmpfs at run time; the compiler only writes
#     intermediates there, the binary itself goes to /workspace.

# Debian trixie ships GCC 14. The C and C++ images share this base layer.
FROM debian:trixie-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends g++ \
 && rm -rf /var/lib/apt/lists/*

# ── Build-time smoke test (LOCKED) ────────────────────────────────────────────
# Compile + run a trivial program through the EXACT manifest path (same flags,
# then execute the binary under stdbuf). A broken toolchain fails the build.
RUN set -eu; \
    printf '#include <bits/stdc++.h>\nint main(){ std::cout << "cr: cpp compile+run smoke ok" << std::endl; }\n' > /tmp/smoke.cpp; \
    g++ -O2 -std=gnu++20 -pipe -o /tmp/smoke /tmp/smoke.cpp; \
    stdbuf -oL /tmp/smoke; \
    rm -f /tmp/smoke.cpp /tmp/smoke

# ── Workspace directory ────────────────────────────────────────────────────────
# The runner (internal/runner/docker.go) sets WorkingDir=/workspace and
# tar-copies user files here before starting the process.
# Permissions 1777 (world-writable sticky) allow the runtime UID 65534 to
# create files without requiring a dedicated system user in the image.
RUN mkdir -p /workspace && chmod 1777 /workspace

WORKDIR /workspace

# No USER instruction: the runner enforces --user 65534:65534 at run time.
# No CMD/ENTRYPOINT: the runner supplies argv from the manifest fields.
