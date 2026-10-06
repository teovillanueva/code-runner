#!/bin/sh
# Fly worker entrypoint: start the in-Machine dockerd against the volume-backed
# store, ensure the language images are present, then exec the worker.
#
# The data-root /var/lib/docker is a mounted ext4 VOLUME (overlay2 cannot run on
# the Machine's overlay rootfs — see the Dockerfile header). Cold-start speed
# comes from the volume already being populated: new volumes are forked from a
# golden snapshot (provision-pool.sh), and a warm restart reuses its own volume.
#
# Images: ensure-images.sh checks every image pinned in images.lock on its own
# and pulls only what the volume is missing (a language added after the golden
# snapshot was taken, or an image whose pinned tag changed). A pull that keeps
# failing does not stop the node: the worker starts with what it has.
set -eu

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
# Never fatal: a missing image only fails that language's jobs until the next
# boot, so the node keeps serving the rest instead of crash-looping.
/usr/local/bin/cr-ensure-images.sh || log "WARN: starting the worker with an incomplete image set"

# ── 4. Hand off to the worker (PID replacement for clean signals) ─────────────
log "starting worker"
exec /usr/local/bin/worker
