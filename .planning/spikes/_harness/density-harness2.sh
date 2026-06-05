#!/bin/sh
# Follow-up density probes that the first harness flagged as worth measuring:
#   A. KSM with ASLR DISABLED — does randomize_va_space=0 unlock the heap-page
#      dedup that ASLR was blocking? (security trade-off, measured not assumed)
#   B. Zygote / copy-on-write — fork heavy sandboxes from a pre-imported parent
#      so the ~70 MB of library pages are shared physically, not duplicated.
# Emits JSON between ===CR_RESULTS2_BEGIN=== / ===CR_RESULTS2_END===.
set -u
WORK=/work
IMG=spike/python:3.12
SAFETY_KB=220000
log() { echo "[harness2] $*" >&2; }
mem_avail() { awk '/MemAvailable/{print $2}' /proc/meminfo; }

cleanup() {
  ids=$(docker ps -aq --filter "name=^crsx_" --filter "name=^zygote" 2>/dev/null)
  [ -n "$ids" ] && docker rm -f $ids >/dev/null 2>&1
  i=0; while [ "$i" -lt 12 ]; do a=$(mem_avail); [ "$a" -gt 3000000 ] && break; sleep 1; i=$((i+1)); done
}

# ── A. KSM with ASLR off ─────────────────────────────────────────────────────
cleanup
ASLR_BEFORE=$(cat /proc/sys/kernel/randomize_va_space 2>/dev/null || echo "?")
echo 0 > /proc/sys/kernel/randomize_va_space 2>/dev/null
ASLR_NOW=$(cat /proc/sys/kernel/randomize_va_space 2>/dev/null || echo "?")
log "ASLR: was=$ASLR_BEFORE now=$ASLR_NOW"
echo 1 > /sys/kernel/mm/ksm/run 2>/dev/null
echo 2000 > /sys/kernel/mm/ksm/pages_to_scan 2>/dev/null
echo 20 > /sys/kernel/mm/ksm/sleep_millisecs 2>/dev/null
NK=14
i=0
while [ "$i" -lt "$NK" ]; do
  [ "$(mem_avail)" -lt "$SAFETY_KB" ] && break
  docker run -d --name "crsx_$i" --read-only --network none --user 65534:65534 \
    --memory 128m --memory-swap 128m --pids-limit 64 --cap-drop ALL \
    --security-opt no-new-privileges --tmpfs /tmp:rw,size=24m \
    -e CR_KSM_MERGE=1 -v "$WORK/workload.py":/workload.py:ro \
    "$IMG" python /workload.py >/dev/null 2>&1 || break
  sleep 4
  i=$((i+1))
done
ASLR_N=$i
log "ASLR-off KSM: launched $ASLR_N sandboxes, settling 50s..."
sleep 50
KS=$(cat /sys/kernel/mm/ksm/pages_shared 2>/dev/null || echo 0)
KG=$(cat /sys/kernel/mm/ksm/pages_sharing 2>/dev/null || echo 0)
ASLR_SAVED_KB=$((KG * 4))
ASLR_SAVED_PER=0; [ "$ASLR_N" -gt 0 ] && ASLR_SAVED_PER=$((ASLR_SAVED_KB / ASLR_N))
log "ASLR-off KSM: pages_sharing=$KG saved=${ASLR_SAVED_KB}kb (~${ASLR_SAVED_PER}kb/cont over $ASLR_N)"
echo 2 > /sys/kernel/mm/ksm/run 2>/dev/null; sleep 2; echo 0 > /sys/kernel/mm/ksm/run 2>/dev/null
echo "$ASLR_BEFORE" > /proc/sys/kernel/randomize_va_space 2>/dev/null
cleanup

# ── B. Zygote / CoW ──────────────────────────────────────────────────────────
log "zygote: forking heavy children from a pre-imported parent..."
docker rm -f zygote >/dev/null 2>&1
docker run -d --name zygote --read-only --network none --user 65534:65534 \
  --memory 3600m --memory-swap 3600m --pids-limit 1024 --cap-drop ALL \
  --security-opt no-new-privileges --tmpfs /tmp:rw,size=24m \
  -v "$WORK/zygote.py":/zygote.py:ro \
  "$IMG" python /zygote.py >/dev/null 2>&1
# wait for it to finish ramping (it self-stops at the safety floor, then sleeps 20s)
zline=""
i=0
while [ "$i" -lt 120 ]; do
  zline=$(docker logs zygote 2>&1 | grep ZYGOTE_CEILING | tail -1)
  [ -n "$zline" ] && break
  [ "$(docker inspect -f '{{.State.Running}}' zygote 2>/dev/null)" != "true" ] && break
  sleep 3; i=$((i+1))
done
log "zygote result: $zline"
ZCEIL=$(echo "$zline" | sed -n 's/.*ZYGOTE_CEILING=\([0-9]*\).*/\1/p'); ZCEIL=${ZCEIL:-0}
ZPER=$(echo "$zline" | sed -n 's/.*marginal_per_child_kb=\([0-9]*\).*/\1/p'); ZPER=${ZPER:-0}
ZUSED=$(echo "$zline" | sed -n 's/.*used_kb=\([0-9]*\).*/\1/p'); ZUSED=${ZUSED:-0}
docker rm -f zygote >/dev/null 2>&1
cleanup

echo "===CR_RESULTS2_BEGIN==="
cat <<EOF
{
 "aslr_off_ksm": {
   "sandboxes": $ASLR_N,
   "pages_sharing": $KG,
   "saved_kb": $ASLR_SAVED_KB,
   "saved_per_container_kb": $ASLR_SAVED_PER
 },
 "zygote_cow": {
   "ceiling": $ZCEIL,
   "marginal_per_child_kb": $ZPER,
   "used_kb": $ZUSED
 }
}
EOF
echo "===CR_RESULTS2_END==="
log "done."
