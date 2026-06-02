# apps/worker/Dockerfile — Go worker binary (multi-stage)
#
# Stage 1: build the Go binary (golang:1.26)
# Stage 2: minimal runtime image (debian-slim) with the binary + runtime assets
#
# The worker mounts /var/run/docker.sock at runtime — it is a CLIENT of the
# host Docker daemon (no Docker-in-Docker). The sandbox images (executor/python:3.12
# etc.) must exist on the HOST daemon, not inside this container.
# Build them with: make python-image

# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.26 AS builder

WORKDIR /build

# Copy Go module files first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY internal/ ./internal/
COPY apps/worker/ ./apps/worker/
COPY tools/ ./tools/
# The Go contract types (generated from packages/contract/schema/wire.schema.json)
# are part of the same Go module. They live under packages/contract/gen/go/wire/
# and are imported by internal/jobstore (and others). Must be included in the
# build context so the module resolver can find them.
COPY packages/contract/gen/go/ ./packages/contract/gen/go/

# Build the worker binary (static-ish; CGO not required for pure Go)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /worker ./apps/worker

# ── Runtime stage ─────────────────────────────────────────────────────────────
# debian-slim (not distroless) so the Docker socket client libraries are available
# and we can run /bin/sh for health-check-style debugging if needed.
FROM debian:bookworm-slim AS runtime

# ca-certificates for any TLS calls (soketi HTTPS, if enabled)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy the compiled binary
COPY --from=builder /worker /app/worker

# Copy runtime assets: languages manifests + seccomp profile
# The worker loads manifests from LANGUAGES_DIR at boot.
# The seccomp profile is passed to the Docker daemon when creating sandboxes.
COPY languages/ /app/languages/
COPY profiles/ /app/profiles/

# The worker reads config from env vars (REDIS_URL, SOKETI_*, DOCKER_HOST, etc.)
# Default env values are baked in but should always be overridden via compose/k8s.
ENV LANGUAGES_DIR=/app/languages
ENV SECCOMP_PROFILE_PATH=/app/profiles/seccomp/runner.json

CMD ["/app/worker"]
