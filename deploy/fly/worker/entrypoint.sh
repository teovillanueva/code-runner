#!/bin/sh
# Fly worker entrypoint: start the in-Machine dockerd against the volume-backed
# store, ensure the language images are present, then exec the worker.
#
# The data-root /var/lib/docker is a mounted ext4 VOLUME (overlay2 cannot run on
# the Machine's overlay rootfs — see the Dockerfile header). Cold-start speed
# comes from the volume already being populated:
#
#   Fast path (normal): the volume was forked from the golden snapshot, so it
#   already holds the images AND the .cr-images-loaded marker. We skip loading
#   entirely and dockerd is up in seconds. Warm restarts of an existing machine
#   hit this path too (the marker persists on the volume).
#
#   Bootstrap path (one-time): an EMPTY volume (used to build the golden snapshot,
#   or any unforked volume) has no marker, so we pull the images from GHCR once
#   and write the marker. Subsequent boots take the fast path.
set -eu

REGISTRY="${SANDBOX_IMAGE_REGISTRY:-ghcr.io/teovillanueva}"
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

# ── 3. Ensure the language images are present ─────────────────────────────────
# Fast path: marker present (volume forked from the golden snapshot or a warm
# restart) → nothing to do. Bootstrap path: pull from GHCR once, then mark.
if [ -f "$LOADED_MARKER" ]; then
  log "language images already present (marker) — skipping pull"
else
  log "no marker — bootstrap: pulling language images from GHCR (one-time, builds the golden volume)"
  if [ -n "${GHCR_TOKEN:-}" ]; then
    log "authenticating to ghcr.io"
    echo "${GHCR_TOKEN}" | docker login ghcr.io -u "${GHCR_USER:-teovillanueva}" --password-stdin
  fi
  pull() {
    local_ref="$1"; remote_ref="$2"
    if docker image inspect "$local_ref" >/dev/null 2>&1; then
      log "present: $local_ref"; return 0
    fi
    log "pulling $remote_ref -> $local_ref"
    docker pull "$remote_ref"
    docker tag "$remote_ref" "$local_ref"
  }
  pull "executor/python:3.12" "${REGISTRY}/executor-python:3.12"
  pull "executor/rust:1.83"   "${REGISTRY}/executor-rust:1.83"
  pull "executor/r:4.4"       "${REGISTRY}/executor-r:4.4"
  pull "executor/sqlite:3"    "${REGISTRY}/executor-sqlite:3"
  touch "$LOADED_MARKER" 2>/dev/null || true
  log "bootstrap complete — snapshot this volume to seed the pool (provision-pool.sh)"
fi
log "language images ready"

# ── 4. Hand off to the worker (PID replacement for clean signals) ─────────────
log "starting worker"
exec /usr/local/bin/worker
