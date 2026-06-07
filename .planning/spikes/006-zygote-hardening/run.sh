#!/bin/sh
# Spike 006 orchestrator. Run AFTER build.sh succeeds. Detached + log-polled (see
# CONVENTIONS). Sections:
#   A. layered density   -- zygote_hardened.py at LEVEL 0..4 (which layer costs density?)
#   B. isolation proof   -- isolation_probe.py (cross-child + FD-inheritance / rule #1)
#   C. multi-language    -- python + R per-language base cost + combined ceiling
# Appends human lines to /work/run.log and a machine block between RESULTS markers.
set -u
WORK=/work
PY=spike/python:3.12
R=spike/r:4.4
LOG=$WORK/run.log
: > "$LOG"
log(){ echo "[run $(date -u +%H:%M:%S)] $*" | tee -a "$LOG"; }
memavail(){ awk '/MemAvailable/{print $2}' /proc/meminfo; }
settle(){ i=0; while [ "$i" -lt 30 ]; do [ "$(memavail)" -gt 3400000 ] && break; sleep 1; i=$((i+1)); done; }

POOL_FLAGS="--privileged --cgroupns host --memory 3600m --memory-swap 3600m --pids-limit 4096 -v $WORK:/work:ro"

log "=== SECTION A: layered hardened density ==="
A_OUT=""
for L in 0 1 2 3 4; do
  settle
  log "density LEVEL=$L starting (MemAvailable=$(memavail) kB)"
  out=$(docker run --rm --name zh$L -e LEVEL=$L $POOL_FLAGS $PY python /work/zygote_hardened.py 2>&1)
  line=$(echo "$out" | grep ZYGOTE_HARD | tail -1)
  [ -z "$line" ] && line="FAILED: $(echo "$out" | tail -3 | tr '\n' '|')"
  log "LEVEL=$L => $line"
  A_OUT="$A_OUT
$line"
  docker rm -f zh$L >/dev/null 2>&1
done

log "=== SECTION B: isolation + FD-inheritance (rule #1) ==="
settle
iso=$(docker run --rm --name iso $POOL_FLAGS $PY python /work/isolation_probe.py all 2>&1)
echo "$iso" | sed -n '/===ISO_BEGIN===/,/===ISO_END===/p' > "$WORK/iso.json"
log "isolation block written ($(wc -l < "$WORK/iso.json") lines)"
docker rm -f iso >/dev/null 2>&1

log "=== SECTION C: multi-language base cost ==="
settle
# per-language parent base (import footprint paid once per language on a node)
PY_BASE_CORE=$(docker run --rm $PY python -c "import numpy,pandas,matplotlib;matplotlib.use('Agg');from matplotlib import pyplot;print([l.split()[1] for l in open('/proc/self/status') if l.startswith('VmRSS')][0])" 2>&1 | tail -1)
PY_BASE_FULL=$(docker run --rm $PY python -c "import numpy,pandas,matplotlib;matplotlib.use('Agg');from matplotlib import pyplot;import scipy,sklearn,statsmodels.api,seaborn;print([l.split()[1] for l in open('/proc/self/status') if l.startswith('VmRSS')][0])" 2>&1 | tail -1)
log "python base RSS: core(numpy/pandas/mpl)=${PY_BASE_CORE}kB  full(+scipy/sklearn/sm/seaborn)=${PY_BASE_FULL}kB"

# R solo fork ramp (base RSS + marginal)
settle
log "R solo fork ramp..."
rout=$(docker run --rm --name zr $POOL_FLAGS $R Rscript /work/zygote_r.R 2>&1)
RLINE=$(echo "$rout" | grep ZYGOTE_R | tail -1)
RBASE=$(echo "$rout" | grep R_BASE_RSS_KB | tail -1)
log "R => $RBASE | $RLINE"
docker rm -f zr >/dev/null 2>&1

# combined: python + R pools ramping concurrently to the shared host floor
settle
log "combined python+R concurrent ramp (two bases resident on one node)..."
docker run -d --rm --name cpy -e HARD_CAP=400 $POOL_FLAGS $PY python /work/zygote_hardened.py >/dev/null 2>&1
docker run -d --rm --name cr  -e HARD_CAP=400 $POOL_FLAGS $R Rscript /work/zygote_r.R >/dev/null 2>&1
# wait for both to print their ceilings (they self-stop at the shared floor)
i=0; CPY=""; CR=""
while [ "$i" -lt 200 ]; do
  CPY=$(docker logs cpy 2>&1 | grep ZYGOTE_HARD | tail -1)
  CR=$(docker logs cr 2>&1 | grep ZYGOTE_R | tail -1)
  pyrun=$(docker inspect -f '{{.State.Running}}' cpy 2>/dev/null || echo false)
  rrun=$(docker inspect -f '{{.State.Running}}' cr 2>/dev/null || echo false)
  { [ -n "$CPY" ] || [ "$pyrun" != "true" ]; } && { [ -n "$CR" ] || [ "$rrun" != "true" ]; } && break
  sleep 3; i=$((i+1))
done
log "combined python => $CPY"
log "combined R      => $CR"
docker rm -f cpy cr >/dev/null 2>&1

# ---- machine-readable results block -----------------------------------------
{
  echo "===CR_RESULTS6_BEGIN==="
  echo "section_A_layered_density:"
  echo "$A_OUT"
  echo "section_C_python_base_core_kb: $PY_BASE_CORE"
  echo "section_C_python_base_full_kb: $PY_BASE_FULL"
  echo "section_C_R_solo_base: $RBASE"
  echo "section_C_R_solo: $RLINE"
  echo "section_C_combined_python: $CPY"
  echo "section_C_combined_R: $CR"
  echo "===CR_RESULTS6_END==="
} | tee -a "$LOG"
log "ALL_DONE"
