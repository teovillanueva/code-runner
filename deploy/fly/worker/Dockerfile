# Fly.io worker image — "dockerd inside the Machine" deployment (Camino C).
#
# A Fly Machine is a Firecracker microVM that does NOT expose the host's Docker
# socket, and Docker-in-Docker on a shared host is disallowed. But a Machine is a
# full VM with its own kernel, so we run our OWN dockerd inside it and the worker
# launches sandboxes against that local daemon. The strong isolation boundary is
# the Firecracker microVM (the worker + all its sandboxes live inside it); the
# per-execution boundary is the hardened container.
#
# STORAGE — why a volume is REQUIRED (verified on Fly, 2026-06-04):
#   dockerd's overlay2 graphdriver needs its data-root on a NON-overlay filesystem
#   (ext4). A Fly Machine's rootfs `/` is itself overlayfs, and overlay2 refuses to
#   run there ("filesystem ... not supported as upperdir"). So /var/lib/docker MUST
#   be a mounted ext4 volume (see fly.toml [mounts]). The worker therefore canNOT
#   be made volume-less, and baking the dockerd store INTO this image is pointless
#   (the volume mount shadows it, and without the volume overlay2 won't start).
#
# COLD START — how it stays fast despite the volume:
#   A FRESH volume is empty, so its first boot must populate the store (the slow
#   path). To avoid that on every new pool machine, new volumes are forked from a
#   GOLDEN SNAPSHOT (a volume pre-populated once) — see deploy/fly/worker/
#   provision-pool.sh and docs/self-hosting/scaling. A snapshot-restored volume
#   already contains the images + the /var/lib/docker/.cr-images-loaded marker, so
#   the entrypoint skips loading entirely and dockerd is up in seconds. The
#   entrypoint's GHCR pull is the ONE-TIME bootstrap used only to build the golden
#   volume (or if a truly empty volume is ever booted).
#
# DEPLOY = PULL (no Fly build). This image is built in GitHub CI
# (.github/workflows/release-images.yml, job `worker-fly`) and published to
# ghcr.io/teovillanueva/code-runner-worker-fly; Fly just pulls it:
#   fly deploy -c deploy/fly/worker/fly.toml \
#     --image ghcr.io/teovillanueva/code-runner-worker-fly:latest
# Pin an immutable :sha-<short> tag for reproducible/rollback-able releases.
#
# Build context = repo root. Referenced from deploy/fly/worker/fly.toml.

# ── Stage 1: build the Go worker binary ──────────────────────────────────────
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/worker ./apps/worker

# ── Stage 2: docker-in-docker runtime + the worker ───────────────────────────
FROM docker:27-dind

# Manifests + seccomp profile the worker needs at runtime.
WORKDIR /app
COPY languages/ /app/languages/
COPY profiles/seccomp/runner.json /app/profiles/seccomp/runner.json
COPY --from=build /out/worker /usr/local/bin/worker
COPY deploy/fly/worker/entrypoint.sh /usr/local/bin/cr-entrypoint.sh
RUN chmod +x /usr/local/bin/cr-entrypoint.sh

ENV LANGUAGES_DIR=/app/languages \
    SECCOMP_PROFILE_PATH=/app/profiles/seccomp/runner.json \
    DOCKER_HOST=unix:///var/run/docker.sock \
    SANDBOX_IMAGE_REGISTRY=ghcr.io/teovillanueva

# cr-entrypoint.sh starts dockerd against the (volume-backed) store, ensures the
# language images are present, then execs the worker.
ENTRYPOINT ["/usr/local/bin/cr-entrypoint.sh"]
