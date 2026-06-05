---
spike: 001
name: ksm-dedup
type: standard
validates: "Given N same-image Python sandboxes with KSM enabled and pages marked mergeable, when ksmd settles, then identical anon pages dedup enough to raise the ~30 ceiling"
verdict: PARTIAL
related: [005]
tags: [density, ksm, memory]
---

# Spike 001: KSM same-page dedup

## What This Validates

Given N memory-active Python sandboxes from the same image, when KSM is enabled
and each process opts in via `prctl(PR_SET_MEMORY_MERGE)`, then identical
anonymous pages across the N processes are merged and the ~30 concurrent ceiling
rises. **Highest-risk lever** — and it had two unknowns: does KSM even exist in
Fly's Firecracker kernel, and does it dedup enough to matter.

## Research

- KSM only scans memory marked mergeable. Containers don't mark it by default →
  the workload calls `prctl(PR_SET_MEMORY_MERGE, 1)` (kernel ≥ 6.4) at startup.
- KSM does **not** dedup `.so` code (already shared via page cache) nor unique
  user data — only identical *anonymous* pages.
- Known limitation: **ASLR** randomizes heap pointers, so logically-identical
  heap pages differ byte-for-byte and KSM can't merge them.

## How to Run

`_harness/density-harness.sh` (configs `ksm-runc`, `ksm-crun`) and
`_harness/density-harness2.sh` (section A, ASLR off). `workload.py` with
`CR_KSM_MERGE=1`.

## Results — PARTIAL

**KSM IS present and writable** in the Fly Firecracker kernel (6.12.91-fly, incl.
the 6.7+ advisor / smart_scan knobs) — the kill-risk did not materialize. But the
dedup is small and **ASLR-bound**:

| Config | Sandboxes | KSM saved | Per container | Ceiling moved? |
|---|---|---|---|---|
| KSM, ASLR **on** (runc) | 29 | 36.5 MB | ~1.2 MB | no |
| KSM, ASLR **on** (crun) | 30 | 34.0 MB | ~1.1 MB | no |
| KSM, ASLR **off** | 14 | 110.9 MB | **~7.9 MB** | marginally (+3–5 projected) |

Turning ASLR off makes KSM **6.6× more effective** per container (1.2 → 7.9 MB),
confirming ASLR was the blocker. Projected over 30 containers that's ~300–500 MB
reclaimed → **+3 to +5 sandboxes**. Real but incremental — and it costs a
security mitigation (ASLR) on a host that runs untrusted code, plus ksmd CPU.

## Investigation Trail

- First full run: KSM saved ~36 MB total (~1.2 MB/cont) with default ASLR — near
  useless. Rather than conclude "KSM = meh", followed the ASLR hypothesis.
- Second run with `randomize_va_space=0`: per-container dedup jumped to ~7.9 MB,
  proving the heap pages *are* mergeable once layouts line up.
- Even the best case doesn't change the strategic picture: KSM attacks the shared
  70 MB *after the fact*; spike 005 (CoW) shares it *by construction* for 2.7×.

## Signal for the build

Skip KSM as a primary density lever. If you later run a runc/crun tier and can
accept ASLR-off (or per-cgroup KSM with the advisor) for a trusted/low-risk
language, it buys a modest +3–5 — but the structural levers (005 / CRIU) dominate.
