#!/usr/bin/env bash
# provision-pool.sh — near-instant cold start for the code-runner Fly worker pool.
#
# WHY THIS EXISTS
#   The worker runs its own dockerd inside the Fly Machine, and dockerd's overlay2
#   store needs an ext4 volume (the Machine rootfs is overlayfs; overlay2 refuses
#   to run there — verified on Fly). A FRESH volume is empty, so its first boot
#   pays a slow one-time store-populate (pull the language images from GHCR). To
#   make a NEWLY CREATED pool machine boot in SECONDS instead, we keep a GOLDEN
#   SNAPSHOT of an already-populated volume and fork new volumes from it: Fly
#   attaches an existing UNATTACHED `docker_data` volume before creating an empty
#   one, so `fly scale count` lands machines on pre-populated volumes.
#
#   The autoscaler only start/stops this PRE-CREATED pool, so machine creation —
#   the only slow-without-snapshot step — happens here (a pre-warm action), never
#   on the hot path.
#
# SUBCOMMANDS
#   bake        Build/refresh the golden snapshot from the CURRENT image: boot a
#               throwaway machine on a fresh volume, let it pull the language
#               images + write the load marker, snapshot that volume, clean up,
#               and print the snapshot id. Run this whenever the language images
#               (or the worker image) change.
#   grow N      Ensure the pool has N machines that boot fast: pre-create enough
#               snapshot-forked `docker_data` volumes, then `fly scale count N`.
#   status      Show the pool machines, volumes, and golden snapshots.
#
# USAGE
#   APP=code-runner-worker REGION=gru VOL_SIZE=40 \
#   IMAGE=ghcr.io/teovillanueva/code-runner-worker-fly:latest \
#     deploy/fly/worker/provision-pool.sh bake
#
#   GOLDEN_SNAPSHOT=vs_xxxx deploy/fly/worker/provision-pool.sh grow 6
#   # (omit GOLDEN_SNAPSHOT to auto-pick the most recent snapshot of GOLDEN_VOL)
#
# Requires: flyctl (authenticated), jq.
set -euo pipefail

APP="${APP:-code-runner-worker}"
REGION="${REGION:-gru}"
VOL_NAME="${VOL_NAME:-docker_data}"
# Store is only ~2-3 GB (the language images) + thin per-sandbox writable layers;
# 40 GB volumes were ~94% empty. 16 GB gives generous headroom and snapshots/
# restores faster + cheaper. (Fly volumes can grow but NOT shrink, so existing
# 40 GB volumes only downsize when recreated from the golden snapshot at this size.)
VOL_SIZE="${VOL_SIZE:-16}"
IMAGE="${IMAGE:-ghcr.io/teovillanueva/code-runner-worker-fly:latest}"
# A dedicated, never-attached-to-the-pool volume name used only for baking, so a
# half-baked volume can never be grabbed by `fly scale count`.
GOLDEN_VOL="${GOLDEN_VOL:-docker_data_golden}"

log() { printf '\033[36m[provision-pool]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31m[provision-pool] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }

command -v flyctl >/dev/null || die "flyctl not found"
command -v jq >/dev/null || die "jq not found"

cmd="${1:-}"; shift || true

case "$cmd" in
bake)
  log "baking golden snapshot for $APP from image: $IMAGE"

  log "creating a fresh bake volume ($GOLDEN_VOL, ${VOL_SIZE}GB, $REGION)..."
  vol_json="$(flyctl volumes create "$GOLDEN_VOL" -a "$APP" -r "$REGION" -s "$VOL_SIZE" -y --json)"
  vol_id="$(echo "$vol_json" | jq -r '.id')"
  [ -n "$vol_id" ] && [ "$vol_id" != "null" ] || die "failed to create bake volume"
  log "bake volume: $vol_id"

  # Boot a throwaway machine on it with the real entrypoint (so it runs the exact
  # bootstrap path: dockerd → GHCR pull → write .cr-images-loaded marker). We
  # don't need the worker loop; --restart=no keeps it from looping on a missing
  # Redis after the marker is written.
  log "booting bake machine (will pull language images + write marker)..."
  # REDIS_URL is pointed at a dead address on purpose: the bootstrap (dockerd →
  # pull images → write marker) runs BEFORE the entrypoint execs the worker, and
  # we must NOT let the bake worker connect to the real Redis and claim prod jobs
  # (`*.internal` resolves org-wide). The worker simply fails to connect after the
  # marker is already written — which is all we need.
  # NOTE: `flyctl machine run` has no --json (unlike `volumes create`), so parse
  # the "Machine ID: <id>" line from its human output.
  m_out="$(flyctl machine run "$IMAGE" \
    -a "$APP" -r "$REGION" \
    --vm-cpu-kind performance --vm-cpus 2 --vm-memory 4096 \
    --volume "${GOLDEN_VOL}:/var/lib/docker" \
    -e REDIS_URL=redis://127.0.0.1:1 \
    --restart no 2>&1)"
  echo "$m_out" | tail -4 >&2
  mach_id="$(echo "$m_out" | grep -oiE 'Machine ID: [0-9a-f]+' | head -1 | awk '{print $NF}')"
  [ -n "$mach_id" ] && [ "$mach_id" != "null" ] || die "failed to launch bake machine"
  log "bake machine: $mach_id — waiting for 'language images ready' (one-time pull)..."

  ready=0
  for _ in $(seq 1 120); do  # up to ~10 min for the one-time pull
    if flyctl logs -a "$APP" --no-tail 2>/dev/null | grep -q "$mach_id.*language images ready"; then
      ready=1; break
    fi
    sleep 5
  done
  [ "$ready" = 1 ] || log "WARN: did not observe 'language images ready' in logs; continuing (check 'fly logs')"

  log "stopping bake machine for a consistent snapshot..."
  flyctl machine stop "$mach_id" -a "$APP" >/dev/null 2>&1 || true
  sleep 5

  log "snapshotting the bake volume..."
  flyctl volumes snapshots create "$vol_id" >/dev/null
  log "snapshot scheduled; waiting for it to finish..."
  snap_id=""
  for _ in $(seq 1 120); do
    snap_id="$(flyctl volumes snapshots list "$vol_id" --json 2>/dev/null \
      | jq -r 'map(select(.status=="created" or (.size>0))) | sort_by(.created_at) | last | .id // empty')"
    [ -n "$snap_id" ] && break
    sleep 10
  done
  [ -n "$snap_id" ] || die "snapshot did not complete in time (check 'fly volumes snapshots list $vol_id')"

  log "tearing down the bake machine + volume (the snapshot is what we keep)..."
  flyctl machine destroy "$mach_id" -a "$APP" --force >/dev/null 2>&1 || true
  flyctl volumes destroy "$vol_id" -y >/dev/null 2>&1 || true

  log "GOLDEN SNAPSHOT READY: $snap_id"
  echo "$snap_id"
  ;;

grow)
  want="${1:?usage: grow N}"
  snap="${GOLDEN_SNAPSHOT:-}"
  if [ -z "$snap" ]; then
    log "GOLDEN_SNAPSHOT not set — auto-detecting most recent snapshot of any $VOL_NAME volume in $APP..."
    # Look across the app's volumes for the newest completed snapshot.
    snap="$(
      flyctl volumes list -a "$APP" --json 2>/dev/null \
        | jq -r '.[].id' \
        | while read -r v; do
            flyctl volumes snapshots list "$v" --json 2>/dev/null \
              | jq -r '.[] | select(.status=="created" or (.size>0)) | [.created_at,.id] | @tsv'
          done \
        | sort -r | head -1 | cut -f2
    )"
  fi
  [ -n "$snap" ] || die "no golden snapshot found; run 'bake' first or pass GOLDEN_SNAPSHOT"
  log "using golden snapshot: $snap"

  existing_machines="$(flyctl machines list -a "$APP" --json 2>/dev/null | jq 'length')"
  unattached="$(flyctl volumes list -a "$APP" --json 2>/dev/null \
    | jq "[.[] | select(.name==\"$VOL_NAME\" and (.attached_machine_id==null or .attached_machine_id==\"\"))] | length")"
  log "pool now: $existing_machines machines, $unattached unattached $VOL_NAME volumes; target=$want"

  # New machines we'll create = want - existing_machines; pre-create that many
  # snapshot-forked volumes (minus any unattached ones already lying around).
  need_new=$(( want - existing_machines ))
  need_vols=$(( need_new - unattached ))
  new_vol_ids=""
  if [ "$need_vols" -gt 0 ]; then
    log "pre-creating $need_vols snapshot-forked $VOL_NAME volume(s) so new machines boot fast..."
    for _ in $(seq 1 "$need_vols"); do
      vid="$(flyctl volumes create "$VOL_NAME" -a "$APP" -r "$REGION" -s "$VOL_SIZE" \
        --snapshot-id "$snap" -y --json | jq -r '.id')"
      log "  created $vid (restoring...)"
      new_vol_ids="$new_vol_ids $vid"
    done
    # A snapshot-forked volume is `restoring` (async data restore) and a machine
    # attaching it would HANG until that finishes. Wait for restore to complete
    # before scaling, so the new machines boot straight away.
    log "waiting for the new volume(s) to finish restoring (the pre-warm cost; boot afterwards is seconds)..."
    for vid in $new_vol_ids; do
      i=0
      while [ "$(flyctl volumes list -a "$APP" --json 2>/dev/null \
                  | jq -r ".[] | select(.id==\"$vid\") | .state")" = "restoring" ]; do
        i=$((i + 1)); [ "$i" -gt 180 ] && die "volume $vid stuck restoring (>30m)"
        sleep 10
      done
      log "  $vid restored"
    done
  else
    log "enough volumes already present; no new ones needed."
  fi

  log "scaling pool to $want (machines attach the pre-populated, restored volumes → seconds, no load)..."
  flyctl scale count "$want" -a "$APP" -r "$REGION" -y
  log "done. New machines should boot fast (entrypoint hits the marker, skips the pull)."
  ;;

status)
  log "machines:";  flyctl machines list -a "$APP" 2>&1 | sed 's/^/  /'
  log "volumes:";   flyctl volumes list  -a "$APP" 2>&1 | sed 's/^/  /'
  ;;

*)
  die "usage: provision-pool.sh {bake|grow N|status}"
  ;;
esac
