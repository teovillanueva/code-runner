# Fly.io worker image — "dockerd inside the Machine" deployment (Camino C).
#
# A Fly Machine is a Firecracker microVM that does NOT expose the host's Docker
# socket, and Docker-in-Docker on a shared host is disallowed. But a Machine is a
# full VM with its own kernel, so we run our OWN dockerd inside it and the worker
# launches sandboxes against that local daemon. The strong isolation boundary is
# the Firecracker microVM (the worker + all its sandboxes live inside it); the
# per-execution boundary is the hardened container. The sandbox language images
# are pulled from GHCR on first boot and tagged as the manifest expects.
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

# cr-entrypoint.sh starts dockerd, warms the language images, then execs the worker.
ENTRYPOINT ["/usr/local/bin/cr-entrypoint.sh"]
