---
spike: 004
name: dind-vs-containerd
type: standard
validates: "Given the worker daemon, when dockerd is dropped for containerd-direct, then daemon RSS overhead drops and frees RAM for slots"
verdict: INCONCLUSIVE
related: [002, 005]
tags: [density, daemon, containerd]
---

# Spike 004: dind vs containerd-direct

## What This Validates

Given the worker's container daemon, when `dockerd` is dropped in favor of talking
to `containerd` directly, then the reclaimed daemon RSS frees RAM for more
sandbox slots.

## How to Run

`_harness/density-harness.sh` — `daemon_rss_kb` snapshot (RSS of dockerd vs
containerd before any sandbox).

## Results — INCONCLUSIVE (probe unreliable), density-neutral

The harness's `ps`-based RSS probe returned implausible values
(`dockerd=70kb containerd=37kb`) — busybox `ps` on the dind base does not report a
usable RSS column for these processes, so the **absolute MB were not captured**.
What the rest of the run shows indirectly: with ~30 sandboxes consuming ~3.4 GB
and the ceiling matching the pure-arithmetic prediction (usable RAM ÷ 110 MB),
the daemon overhead is a **small fixed cost** (low hundreds of MB at most),
already absorbed in the `mem_base` baseline — it is **not** what limits the 30.

## Investigation Trail

- The probe needs `/proc/<pid>/status` (VmRSS) or `cat /sys/fs/cgroup/.../memory.current`
  rather than busybox `ps -o rss` — a harness fix for any re-run.
- Strategically this is moot for density: even a generous 200–300 MB daemon
  reclaim is ~+2 sandboxes, the same order as the other incremental levers and
  far below the structural 005 result.

## Signal for the build

Dropping dockerd (dind → containerd-direct, or the planned containerd `Runner`)
is worth it for **operational** reasons — fewer moving parts, faster ops, and on
Fly it removes the dind-on-Firecracker + ext4-volume contortion documented in
`deploy/fly/worker/`. It is **not** a density lever. Treat it as an architecture
cleanup, not a capacity play. (Re-measure daemon RSS with VmRSS if a precise
number is ever needed.)
