#!/bin/sh
# ensure-images.sh — make sure every language image pinned in images.lock is on
# this machine's docker volume. Called by entrypoint.sh once dockerd is up.
#
# Each image is checked on its own (there used to be an all-or-nothing marker,
# /var/lib/docker/.cr-images-loaded, that skipped the check entirely — so a volume
# forked from an older golden snapshot never got a language added later):
#
#   - pinned ref already on the volume → one local `docker image inspect` (no
#     registry call) + re-point the local name at it. The warm path stays fast.
#   - missing (new language, or its pinned tag changed) → pull it, with retries.
#   - a pull that keeps failing (GHCR down, rate limit) is logged and SKIPPED:
#     the worker still starts and serves every language it has, keeping the
#     previous build of that image if the volume has one. One image must never
#     crash-loop the whole node.
#
# When every image is in place, executor images that are no longer pinned (an
# older tag, or the floating tags the old bootstrap pulled) are removed, so the
# volume does not fill up across updates. Nothing is removed after a failure.
#
# Prints "language images ready" on success — provision-pool.sh bake waits for
# exactly that line before snapshotting — or "language images INCOMPLETE".
# Exit status: 0 when all are in place, 1 otherwise (entrypoint.sh carries on).
#
# Env: SANDBOX_IMAGE_REGISTRY (default ghcr.io/teovillanueva), IMAGES_LOCK (path
# to the lock file), PULL_ATTEMPTS (default 3), PULL_BACKOFF_S (default 5),
# GHCR_TOKEN / GHCR_USER (optional, for private registries).
set -u

REGISTRY="${SANDBOX_IMAGE_REGISTRY:-ghcr.io/teovillanueva}"
IMAGES_LOCK="${IMAGES_LOCK:-/app/images.lock}"
PULL_ATTEMPTS="${PULL_ATTEMPTS:-3}"
PULL_BACKOFF_S="${PULL_BACKOFF_S:-5}"

log() { echo "[cr-entrypoint] $*"; }

if [ ! -r "$IMAGES_LOCK" ]; then
  log "WARN: $IMAGES_LOCK not found — no language images checked"
  log "language images INCOMPLETE"
  exit 1
fi

if [ -n "${GHCR_TOKEN:-}" ]; then
  log "authenticating to ghcr.io"
  echo "${GHCR_TOKEN}" | docker login ghcr.io -u "${GHCR_USER:-teovillanueva}" --password-stdin >/dev/null \
    || log "WARN: ghcr.io login failed; trying anonymous pulls"
fi

# ensure <local_ref> <remote_ref> — 0 when remote_ref is on the volume and
# local_ref points at it. docker reads no stdin here (</dev/null) so it cannot
# eat the lock file the caller's `while read` loop is reading.
ensure() {
  local_ref="$1"
  remote_ref="$2"
  if docker image inspect "$remote_ref" >/dev/null 2>&1 </dev/null; then
    docker tag "$remote_ref" "$local_ref" </dev/null
    log "present: $remote_ref"
    return 0
  fi
  attempt=1
  while [ "$attempt" -le "$PULL_ATTEMPTS" ]; do
    log "pulling $remote_ref -> $local_ref (attempt $attempt/$PULL_ATTEMPTS)"
    if docker pull -q "$remote_ref" >/dev/null </dev/null \
      && docker tag "$remote_ref" "$local_ref" </dev/null; then
      log "pulled: $remote_ref"
      return 0
    fi
    [ "$attempt" -lt "$PULL_ATTEMPTS" ] && sleep $((attempt * PULL_BACKOFF_S))
    attempt=$((attempt + 1))
  done
  if docker image inspect "$local_ref" >/dev/null 2>&1 </dev/null; then
    log "WARN: could not pull $remote_ref — keeping the $local_ref already on the volume"
  else
    log "WARN: could not pull $remote_ref — $local_ref is MISSING; its jobs fail until the next boot"
  fi
  return 1
}

failed=0
wanted=" "
while read -r local_ref image rest; do
  case "$local_ref" in '' | '#'*) continue ;; esac
  if [ -z "$image" ]; then
    log "WARN: malformed line in $IMAGES_LOCK: $local_ref"
    failed=$((failed + 1))
    continue
  fi
  remote_ref="${REGISTRY}/${image}"
  wanted="${wanted}${remote_ref} "
  ensure "$local_ref" "$remote_ref" || failed=$((failed + 1))
done <"$IMAGES_LOCK"

if [ "$failed" -ne 0 ]; then
  log "language images INCOMPLETE: $failed image(s) missing or stale — keeping everything on the volume"
  exit 1
fi

# Every pinned image is in place: drop superseded executor tags, then the
# dangling layers they leave behind.
docker image ls --format '{{.Repository}}:{{.Tag}}' --filter "reference=${REGISTRY}/executor-*" </dev/null \
  | while read -r ref; do
      case "$ref" in *'<none>'*) continue ;; esac
      case "$wanted" in
        *" $ref "*) ;;
        *)
          log "removing superseded $ref"
          docker image rm "$ref" >/dev/null 2>&1 </dev/null || true
          ;;
      esac
    done
docker image prune -f >/dev/null 2>&1 </dev/null || true

log "language images ready"
exit 0
