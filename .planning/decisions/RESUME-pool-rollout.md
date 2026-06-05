# RESUME HERE — finish the fixed-image rollout to the worker pool

Paused 2026-06-05 mid-rollout (train network blocked the Fly API). The CODE fix is
DONE: all 4 language images fixed + smoke-tested, validated in CI, **published to
GHCR**. What remains is purely getting those images onto the live `code-runner-worker`
pool. Org: **edalef** (staging). No prod traffic.

## ⚠️ STATE LEFT BEHIND (do #1 first)

- **The fly-autoscaler is PAUSED.** I stopped machine `e827941c39d598` in
  `code-runner-autoscaler` so the bake machine could survive (the autoscaler was
  killing it). It is STILL STOPPED. Keep it stopped DURING the rollout below, then
  restart it at the end.
- **Possible orphaned bake leftovers** (I killed the bake script before cleanup):
  machine `48e7452fd64968` + a `docker_data_golden` volume `vol_vwn6866q159m8kmv`
  in `code-runner-worker`. Destroy them in step 1.
- Pool = 3 machines with the OLD images: `48e3627b75ee48` (vol `vol_42ko1nnqyw0wl534`),
  `d891dddc5e2148` (vol `vol_4y89kdonj3k90z3r`), `d8d137dc0d3258` (vol `vol_r7y0kpwn9m2joj3r`).

## Steps (run when you have connectivity)

```bash
cd ~/code-runner
A=code-runner-worker

# 1. Clean orphaned bake leftovers
fly machine destroy 48e7452fd64968 -a $A --force || true
fly volumes list -a $A | grep golden | awk '{print $1}' | xargs -I{} fly volumes destroy {} -y || true

# 2. Confirm autoscaler is still paused (keep it paused through step 6)
fly machine status e827941c39d598 -a code-runner-autoscaler | grep State   # expect: stopped

# 3. Bake the golden snapshot from the FIXED images (script already has the --detach fix)
APP=$A REGION=gru VOL_SIZE=16 \
  IMAGE=ghcr.io/teovillanueva/code-runner-worker-fly:latest \
  bash deploy/fly/worker/provision-pool.sh bake
#   ^ note the printed GOLDEN SNAPSHOT READY: <snap_id>
SNAP=<snap_id>

# 4. Destroy the 3 old pool machines + their volumes (no precious data — image cache only)
for m in 48e3627b75ee48 d891dddc5e2148 d8d137dc0d3258; do fly machine destroy $m -a $A --force; done
for v in vol_42ko1nnqyw0wl534 vol_4y89kdonj3k90z3r vol_r7y0kpwn9m2joj3r; do fly volumes destroy $v -y || true; done

# 5. Grow the pool fresh from the FIXED snapshot (size to your exam peak; 3 restores prior)
GOLDEN_SNAPSHOT=$SNAP APP=$A REGION=gru VOL_SIZE=16 \
  bash deploy/fly/worker/provision-pool.sh grow 3

# 6. VERIFY the fix is live (this is the whole point — scipy/sklearn import must work)
fly ssh console -a $A -C "docker run --rm --network none executor/python:3.12 \
  python -c 'import scipy, sklearn, statsmodels.api, seaborn; print(\"sci-stack OK\")'"

# 7. Apply D3 (drain 300s / kill_timeout 330s — already staged in fly.toml)
fly deploy -c deploy/fly/worker/fly.toml \
  --image ghcr.io/teovillanueva/code-runner-worker-fly:latest

# 8. RESTART the autoscaler (don't forget!)
fly machine start e827941c39d598 -a code-runner-autoscaler

# 9. Push any local commits (if the train blocked the push)
git push origin main
```

## After this is done

- The launch-blocker is fully closed (fixed images live on the pool + a permanent
  CI smoke-test guard so it can't regress).
- Next: D3 API half (cap wallTimeMs ≤ 300s, reject>cap — needs YOUR call on the
  longest exam task duration). Then spike 006 (OOM containment + drain real-stop).
  Then the zygote fast-follow (FAST-FOLLOW-zygote-runner.md).
