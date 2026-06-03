#!/bin/sh
# Fly worker entrypoint: start the in-Machine dockerd, warm the sandbox language
# images from GHCR, then exec the worker. Images persist on the mounted volume
# (/var/lib/docker) so warming only happens on a cold/first boot.
set -eu

REGISTRY="${SANDBOX_IMAGE_REGISTRY:-ghcr.io/teovillanueva}"

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

# ── 3. Optional GHCR login (only needed if the packages are PRIVATE) ──────────
if [ -n "${GHCR_TOKEN:-}" ]; then
  log "authenticating to ghcr.io"
  echo "${GHCR_TOKEN}" | docker login ghcr.io -u "${GHCR_USER:-teovillanueva}" --password-stdin
fi

# ── 4. Warm the language images (pull + tag to the manifest's local ref) ──────
# Format: "<manifest-image-ref> <ghcr-repo>:<tag>"
warm() {
  local_ref="$1"
  remote_ref="$2"
  if docker image inspect "$local_ref" >/dev/null 2>&1; then
    log "present: $local_ref"
    return 0
  fi
  log "pulling $remote_ref -> $local_ref"
  docker pull "$remote_ref"
  docker tag "$remote_ref" "$local_ref"
}

warm "executor/python:3.12" "${REGISTRY}/executor-python:3.12"
warm "executor/rust:1.83"   "${REGISTRY}/executor-rust:1.83"
warm "executor/r:4.4"       "${REGISTRY}/executor-r:4.4"
warm "executor/sqlite:3"    "${REGISTRY}/executor-sqlite:3"
log "language images ready"

# ── 5. Hand off to the worker (PID replacement for clean signals) ─────────────
log "starting worker"
exec /usr/local/bin/worker
