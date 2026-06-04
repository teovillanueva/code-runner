#!/bin/sh
# Fly worker entrypoint: start the in-Machine dockerd, make the sandbox language
# images available, then exec the worker.
#
# Self-contained path (default): the language images are baked into this image as
# docker-archive tarballs at /app/images/*.tar (see the Dockerfile's skopeo
# stage) and `docker load`ed here — no registry pull, no network needed at boot.
# A marker on the docker storage skips the (idempotent) load on warm restarts.
# If no baked tarballs are present, we fall back to pulling from GHCR.
set -eu

REGISTRY="${SANDBOX_IMAGE_REGISTRY:-ghcr.io/teovillanueva}"
IMAGES_DIR="${SANDBOX_IMAGES_DIR:-/app/images}"
LOADED_MARKER="/var/lib/docker/.cr-images-loaded"

log() { echo "[cr-entrypoint] $*"; }

# ── 1. Start dockerd (the docker:dind image ships dockerd-entrypoint.sh) ──────
log "starting dockerd..."
dockerd-entrypoint.sh dockerd \
  --host=unix:///var/run/docker.sock \
  --storage-driver=overlay2 \
  >/var/log/dockerd.log 2>&1 &

# ── 2. Wait for the daemon ───────────────────────────────────────────────────
i=0
until docker info >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 90 ]; then
    log "dockerd failed to start; last log lines:"
    tail -n 40 /var/log/dockerd.log || true
    exit 1
  fi
  sleep 1
done
log "dockerd is up"

# ── 3. Make the language images available ─────────────────────────────────────
# Prefer the baked tarballs (self-contained, no network). docker load is
# idempotent (skips layers it already has); the marker avoids re-reading the
# tarballs on warm restarts when a /var/lib/docker volume persists them.
if [ -f "$LOADED_MARKER" ]; then
  log "language images already loaded (marker present) — skipping"
elif ls "$IMAGES_DIR"/*.tar >/dev/null 2>&1; then
  for tar in "$IMAGES_DIR"/*.tar; do
    log "loading baked image: $tar"
    docker load -i "$tar"
  done
  touch "$LOADED_MARKER" 2>/dev/null || true
  log "baked language images loaded"
else
  # Fallback: no baked tarballs — pull from GHCR (the pre-self-contained path).
  log "no baked images at $IMAGES_DIR — falling back to GHCR pull"
  if [ -n "${GHCR_TOKEN:-}" ]; then
    log "authenticating to ghcr.io"
    echo "${GHCR_TOKEN}" | docker login ghcr.io -u "${GHCR_USER:-teovillanueva}" --password-stdin
  fi
  warm() {
    local_ref="$1"; remote_ref="$2"
    if docker image inspect "$local_ref" >/dev/null 2>&1; then
      log "present: $local_ref"; return 0
    fi
    log "pulling $remote_ref -> $local_ref"
    docker pull "$remote_ref"
    docker tag "$remote_ref" "$local_ref"
  }
  warm "executor/python:3.12" "${REGISTRY}/executor-python:3.12"
  warm "executor/rust:1.83"   "${REGISTRY}/executor-rust:1.83"
  warm "executor/r:4.4"       "${REGISTRY}/executor-r:4.4"
  warm "executor/sqlite:3"    "${REGISTRY}/executor-sqlite:3"
  touch "$LOADED_MARKER" 2>/dev/null || true
fi
log "language images ready"

# ── 4. Hand off to the worker (PID replacement for clean signals) ─────────────
log "starting worker"
exec /usr/local/bin/worker
