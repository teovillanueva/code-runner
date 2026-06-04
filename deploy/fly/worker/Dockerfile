# Fly.io worker image — "dockerd inside the Machine" deployment (Camino C).
#
# A Fly Machine is a Firecracker microVM that does NOT expose the host's Docker
# socket, and Docker-in-Docker on a shared host is disallowed. But a Machine is a
# full VM with its own kernel, so we run our OWN dockerd inside it and the worker
# launches sandboxes against that local daemon. The strong isolation boundary is
# the Firecracker microVM (the worker + all its sandboxes live inside it); the
# per-execution boundary is the hardened container.
#
# SELF-CONTAINED: the sandbox language images are baked INTO this image as
# docker-archive tarballs (Stage 2) and `docker load`ed on boot — no registry
# pull at runtime, no GHCR dependency, and no /var/lib/docker volume required for
# the image cache. That makes the worker stateless, so an autoscaler can
# create/destroy nodes freely and cold start is fast (local load, not a network
# pull). See docs/self-hosting/scaling#self-contained-worker-images.
#
# Build context = repo root. Referenced from deploy/fly/worker/fly.toml.

# ── Stage 1: build the Go worker binary ──────────────────────────────────────
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/worker ./apps/worker

# ── Stage 2: bake the language images into docker-archive tarballs ────────────
# skopeo copies straight from the registry to a docker-archive WITHOUT a Docker
# daemon, so it runs cleanly inside buildkit. Each archive is tagged with the
# manifest's LOCAL ref (executor/<lang>:<tag>), so `docker load` on boot imports
# it under exactly the name the worker resolves from manifest.json — no retag.
# Public GHCR packages need no creds; for PRIVATE ones pass at build time:
#   fly deploy ... --build-arg GHCR_SRC_CREDS=<user>:<token>
# To add a language: publish its image, then add a skopeo line here (and the
# entrypoint loads every *.tar automatically).
#
# --override-arch amd64 --override-os linux: the executor images are linux/amd64
# only (what Fly Machines run), and the Go binary above is built for amd64. The
# override pins the amd64 variant so the bake also works when building on an
# arm64 host (e.g. an Apple-Silicon dev machine), which would otherwise fail
# resolving a non-existent arm64 variant.
FROM quay.io/skopeo/stable AS images
ARG SANDBOX_IMAGE_REGISTRY=ghcr.io/teovillanueva
ARG GHCR_SRC_CREDS=
RUN set -eu; \
    creds=""; if [ -n "${GHCR_SRC_CREDS}" ]; then creds="--src-creds ${GHCR_SRC_CREDS}"; fi; \
    arch="--override-arch amd64 --override-os linux"; \
    mkdir -p /images; \
    skopeo copy $arch $creds docker://${SANDBOX_IMAGE_REGISTRY}/executor-python:3.12 docker-archive:/images/python.tar:executor/python:3.12; \
    skopeo copy $arch $creds docker://${SANDBOX_IMAGE_REGISTRY}/executor-rust:1.83  docker-archive:/images/rust.tar:executor/rust:1.83; \
    skopeo copy $arch $creds docker://${SANDBOX_IMAGE_REGISTRY}/executor-r:4.4      docker-archive:/images/r.tar:executor/r:4.4; \
    skopeo copy $arch $creds docker://${SANDBOX_IMAGE_REGISTRY}/executor-sqlite:3   docker-archive:/images/sqlite.tar:executor/sqlite:3

# ── Stage 3: docker-in-docker runtime + the worker ───────────────────────────
FROM docker:27-dind

# Manifests + seccomp profile the worker needs at runtime.
WORKDIR /app
COPY languages/ /app/languages/
COPY profiles/seccomp/runner.json /app/profiles/seccomp/runner.json
COPY --from=build /out/worker /usr/local/bin/worker
COPY deploy/fly/worker/entrypoint.sh /usr/local/bin/cr-entrypoint.sh
RUN chmod +x /usr/local/bin/cr-entrypoint.sh

# Baked language images (docker-archive tarballs), loaded on boot by the entrypoint.
COPY --from=images /images /app/images

ENV LANGUAGES_DIR=/app/languages \
    SECCOMP_PROFILE_PATH=/app/profiles/seccomp/runner.json \
    DOCKER_HOST=unix:///var/run/docker.sock \
    SANDBOX_IMAGE_REGISTRY=ghcr.io/teovillanueva

# cr-entrypoint.sh starts dockerd, loads the baked language images, then execs the worker.
ENTRYPOINT ["/usr/local/bin/cr-entrypoint.sh"]
