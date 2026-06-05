#!/bin/sh
# Density measurement harness for code-runner workers.
# Runs INSIDE docker:27-dind (busybox ash) on a Fly perf-2x/4GB machine.
#
# Measures, faithfully against the prod sandbox config, how many concurrent
# "memory-active" Python sandboxes fit in 2cpu/4gb, and how each lever moves it:
#   - runc vs crun            (per-container overhead + startup latency)
#   - KSM off vs on           (same-image anon-page dedup; needs Firecracker kernel support)
#   - runtime footprint flags (-S / MALLOC_ARENA_MAX / idle-vs-active)
#   - dind vs containerd      (daemon RSS you'd reclaim by dropping dockerd)
#
# Emits a JSON blob between ===CR_RESULTS_BEGIN=== / ===CR_RESULTS_END===.
set -u

WORK=/work
SAFETY_KB=220000          # stop launching when MemAvailable drops below ~220 MB
SETTLE_S=45               # seconds to let ksmd scan before reading dedup stats
HARD_CAP=180              # never launch more than this many containers
IMG=spike/python:3.12
log() { echo "[harness] $*" >&2; }

mem_avail() { awk '/MemAvailable/{print $2}' /proc/meminfo; }
mem_total() { awk '/MemTotal/{print $2}' /proc/meminfo; }
rss_kb() { ps -o rss= -p "$1" 2>/dev/null | awk '{s+=$1} END{print s+0}'; }
proc_rss_by_name() { ps -eo rss,comm 2>/dev/null | awk -v n="$1" '$2 ~ n {s+=$1} END{print s+0}'; }

# ── 0. Environment probe ─────────────────────────────────────────────────────
KVER=$(uname -r)
KSM_PRESENT=no; [ -d /sys/kernel/mm/ksm ] && KSM_PRESENT=yes
KSM_RUN_WRITABLE=no
if [ "$KSM_PRESENT" = yes ]; then
  if ( echo 0 > /sys/kernel/mm/ksm/run ) 2>/dev/null; then KSM_RUN_WRITABLE=yes; fi
fi
MEMTOTAL_KB=$(mem_total)
log "kernel=$KVER ksm_present=$KSM_PRESENT ksm_writable=$KSM_RUN_WRITABLE memtotal_kb=$MEMTOTAL_KB"

# ── 1. Dependencies: crun + register it with dockerd ─────────────────────────
# coreutils gives a GNU `date` that supports %N (busybox date does not -> the
# startup-latency timing needs nanoseconds).
apk add --no-cache crun coreutils >/dev/null 2>&1
CRUN_BIN=$(command -v crun || echo "")
CRUN_PRESENT=no; [ -n "$CRUN_BIN" ] && CRUN_PRESENT=yes
CRUN_VER=$([ -n "$CRUN_BIN" ] && crun --version 2>/dev/null | head -1 | awk '{print $3}' || echo "")
RUNC_VER=$(runc --version 2>/dev/null | head -1 | awk '{print $3}' || echo "")
log "crun=$CRUN_PRESENT ($CRUN_VER) runc=$RUNC_VER"

if [ "$CRUN_PRESENT" = yes ]; then
  mkdir -p /etc/docker
  cat > /etc/docker/daemon.json <<EOF
{ "runtimes": { "crun": { "path": "$CRUN_BIN" } } }
EOF
  # live-reload: runtimes map is reloadable via SIGHUP, no full restart
  kill -HUP "$(pidof dockerd | awk '{print $1}')" 2>/dev/null
  sleep 3
fi

# ── 2. Build the faithful Python sandbox image (= the prod language image) ────
# BuildKit (docker 27 default) failed transiently on this box; the classic
# builder is reliable. Skip the build entirely if the image already exists.
if docker image inspect "$IMG" >/dev/null 2>&1; then
  log "image $IMG already present — skipping build."
else
  log "building $IMG (science stack) — a few minutes..."
  if ! DOCKER_BUILDKIT=0 docker build -t "$IMG" "$WORK/lang" >"$WORK/build.err" 2>&1; then
    log "image build FAILED:"; tail -5 "$WORK/build.err" >&2
    echo "===CR_RESULTS_BEGIN==="
    echo "{\"error\":\"image_build_failed\",\"kernel\":\"$KVER\",\"ksm_present\":\"$KSM_PRESENT\"}"
    echo "===CR_RESULTS_END==="
    exit 1
  fi
fi
log "image ready."

# Daemon overhead snapshot (RSS of dockerd vs containerd, before any sandbox).
DOCKERD_RSS=$(proc_rss_by_name dockerd)
CONTAINERD_RSS=$(proc_rss_by_name containerd)
log "daemon RSS: dockerd=${DOCKERD_RSS}kb containerd=${CONTAINERD_RSS}kb"

cleanup_sandboxes() {
  ids=$(docker ps -aq --filter "name=^crsx_" 2>/dev/null)
  [ -n "$ids" ] && docker rm -f $ids >/dev/null 2>&1
  # wait for memory to actually come back
  i=0; while [ "$i" -lt 10 ]; do
    a=$(mem_avail); [ "$a" -gt $((SAFETY_KB + 800000)) ] && break
    sleep 1; i=$((i+1))
  done
}

ksm_set() { # $1 = on|off
  [ "$KSM_RUN_WRITABLE" = yes ] || return 0
  if [ "$1" = on ]; then
    echo 1    > /sys/kernel/mm/ksm/run            2>/dev/null
    echo 2000 > /sys/kernel/mm/ksm/pages_to_scan  2>/dev/null
    echo 20   > /sys/kernel/mm/ksm/sleep_millisecs 2>/dev/null
  else
    echo 2 > /sys/kernel/mm/ksm/run 2>/dev/null   # 2 = stop & unmerge
    sleep 2
    echo 0 > /sys/kernel/mm/ksm/run 2>/dev/null
  fi
}

ksm_stat() { # echoes "shared sharing" in pages (4KB each)
  if [ "$KSM_PRESENT" = yes ]; then
    s=$(cat /sys/kernel/mm/ksm/pages_shared 2>/dev/null || echo 0)
    g=$(cat /sys/kernel/mm/ksm/pages_sharing 2>/dev/null || echo 0)
    echo "$s $g"
  else
    echo "0 0"
  fi
}

# launch one sandbox; returns 0 if it came up and stayed running
launch_one() { # $1 = index, $2 = runtime, $3 = extra env flags string
  name="crsx_$1"
  # shellcheck disable=SC2086
  docker run -d --name "$name" --runtime "$2" \
    --read-only --network none --user 65534:65534 \
    --memory 128m --memory-swap 128m --pids-limit 64 \
    --cap-drop ALL --security-opt no-new-privileges \
    --tmpfs /tmp:rw,size=24m \
    -v "$WORK/workload.py":/workload.py:ro \
    $3 \
    "$IMG" python /workload.py >/dev/null 2>&1 || return 1
  return 0
}

# ── 3. Density run: ramp sandboxes until the RAM safety floor ────────────────
# Args: label runtime envflags ksm_mode extend(yes/no)
# Echoes a JSON object for this config.
measure() {
  LABEL=$1; RT=$2; ENVF=$3; KSM=$4; EXTEND=$5
  cleanup_sandboxes
  ksm_set "$KSM"
  MEM_BASE=$(mem_avail)
  n=0
  while [ "$n" -lt "$HARD_CAP" ]; do
    avail=$(mem_avail)
    [ "$avail" -lt "$SAFETY_KB" ] && { log "$LABEL: hit safety floor at n=$n (avail=${avail}kb)"; break; }
    if ! launch_one "$n" "$RT" "$ENVF"; then
      log "$LABEL: docker run failed at n=$n"; break
    fi
    sleep 4   # let the science-stack imports fault in to steady state
    if [ "$(docker inspect -f '{{.State.Running}}' "crsx_$n" 2>/dev/null)" != "true" ]; then
      log "$LABEL: sandbox $n died (OOM?) — ceiling reached"
      docker rm -f "crsx_$n" >/dev/null 2>&1
      break
    fi
    n=$((n+1))
  done
  CEIL=$n
  MEM_AFTER=$(mem_avail)
  USED_KB=$((MEM_BASE - MEM_AFTER))
  PER_KB=0; [ "$CEIL" -gt 0 ] && PER_KB=$((USED_KB / CEIL))

  KSM_SHARED=0; KSM_SHARING=0; KSM_SAVED_KB=0; CEIL_EXT=$CEIL
  if [ "$KSM" = on ] && [ "$KSM_PRESENT" = yes ] && [ "$CEIL" -gt 0 ]; then
    log "$LABEL: letting ksmd settle ${SETTLE_S}s..."
    sleep "$SETTLE_S"
    set -- $(ksm_stat); KSM_SHARED=$1; KSM_SHARING=$2
    KSM_SAVED_KB=$((KSM_SHARING * 4))
    if [ "$EXTEND" = yes ]; then
      # KSM freed memory — try to push the ceiling higher with the reclaimed RAM
      m=$CEIL
      while [ "$m" -lt "$HARD_CAP" ]; do
        avail=$(mem_avail)
        [ "$avail" -lt "$SAFETY_KB" ] && break
        launch_one "$m" "$RT" "$ENVF" || break
        sleep 4
        [ "$(docker inspect -f '{{.State.Running}}' "crsx_$m" 2>/dev/null)" != "true" ] && { docker rm -f "crsx_$m" >/dev/null 2>&1; break; }
        m=$((m+1))
      done
      CEIL_EXT=$m
    fi
  fi

  SAMPLE=$(docker stats --no-stream --format '{{.MemUsage}}' "crsx_0" 2>/dev/null | awk '{print $1}')
  log "$LABEL: ceiling=$CEIL ext=$CEIL_EXT per=${PER_KB}kb ksm_saved=${KSM_SAVED_KB}kb sample=$SAMPLE"
  cat <<EOF
{"label":"$LABEL","runtime":"$RT","ksm":"$KSM","ceiling":$CEIL,"ceiling_extended":$CEIL_EXT,"mem_base_kb":$MEM_BASE,"mem_after_kb":$MEM_AFTER,"used_kb":$USED_KB,"per_container_kb":$PER_KB,"ksm_pages_shared":$KSM_SHARED,"ksm_pages_sharing":$KSM_SHARING,"ksm_saved_kb":$KSM_SAVED_KB,"sample_first":"$SAMPLE"}
EOF
}

# ── 4. Startup latency: 8 trivial runs per runtime ───────────────────────────
startup_ms() { # $1 runtime -> median-ish avg ms
  rt=$1; total=0; iters=8
  i=0; while [ "$i" -lt "$iters" ]; do
    t0=$(date +%s%N)
    docker run --rm --runtime "$rt" --network none --read-only "$IMG" python -c "pass" >/dev/null 2>&1
    t1=$(date +%s%N)
    total=$(( total + (t1 - t0) / 1000000 ))
    i=$((i+1))
  done
  echo $(( total / iters ))
}

# ── 5. Idle-interpreter footprint (the ~10 MB idle-session number) ───────────
idle_rss_kb() { # $1 runtime, $2 envflags
  docker rm -f crsx_idle >/dev/null 2>&1
  # shellcheck disable=SC2086
  docker run -d --name crsx_idle --runtime "$1" --read-only --network none \
    --user 65534:65534 --memory 128m --memory-swap 128m --pids-limit 64 \
    --cap-drop ALL --security-opt no-new-privileges --tmpfs /tmp:rw,size=8m \
    -v "$WORK/workload.py":/workload.py:ro -e CR_IDLE=1 $2 \
    "$IMG" python /workload.py >/dev/null 2>&1
  sleep 3
  v=$(docker stats --no-stream --format '{{.MemUsage}}' crsx_idle 2>/dev/null | awk '{print $1}')
  docker rm -f crsx_idle >/dev/null 2>&1
  echo "$v"
}

# ════════════════════════════════════════════════════════════════════════════
log "=== measuring configs ==="
R_BASELINE=$(measure baseline-runc runc "" off no)
R_CRUN=$(measure crun crun "" off no)
R_FOOTPRINT=$(measure footprint-runc runc "-e MALLOC_ARENA_MAX=1 -e PYTHONMALLOC=malloc" off no)
if [ "$KSM_RUN_WRITABLE" = yes ]; then
  R_KSM_RUNC=$(measure ksm-runc runc "-e CR_KSM_MERGE=1" on yes)
  R_KSM_CRUN=$(measure ksm-crun crun "-e CR_KSM_MERGE=1" on yes)
else
  R_KSM_RUNC='{"label":"ksm-runc","skipped":"ksm_not_available"}'
  R_KSM_CRUN='{"label":"ksm-crun","skipped":"ksm_not_available"}'
fi

cleanup_sandboxes
log "=== startup latency ==="
SU_RUNC=$(startup_ms runc)
SU_CRUN=$([ "$CRUN_PRESENT" = yes ] && startup_ms crun || echo -1)
log "=== idle footprint ==="
IDLE_PLAIN=$(idle_rss_kb runc "")
IDLE_FLAGS=$(idle_rss_kb runc "-e MALLOC_ARENA_MAX=1")
cleanup_sandboxes

echo "===CR_RESULTS_BEGIN==="
cat <<EOF
{
 "env": {
   "kernel": "$KVER",
   "memtotal_kb": $MEMTOTAL_KB,
   "ksm_present": "$KSM_PRESENT",
   "ksm_run_writable": "$KSM_RUN_WRITABLE",
   "crun_present": "$CRUN_PRESENT",
   "crun_version": "$CRUN_VER",
   "runc_version": "$RUNC_VER"
 },
 "daemon_rss_kb": { "dockerd": $DOCKERD_RSS, "containerd": $CONTAINERD_RSS },
 "startup_ms": { "runc": $SU_RUNC, "crun": $SU_CRUN },
 "idle_footprint": { "plain": "$IDLE_PLAIN", "flags": "$IDLE_FLAGS" },
 "configs": [
   $R_BASELINE,
   $R_CRUN,
   $R_FOOTPRINT,
   $R_KSM_RUNC,
   $R_KSM_CRUN
 ]
}
EOF
echo "===CR_RESULTS_END==="
log "done."
