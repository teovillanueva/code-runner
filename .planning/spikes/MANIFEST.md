# Spike Manifest

## Idea

Increase how many concurrent sandboxes a single **2cpu/4gb** code-runner worker
can hold, looking ahead to scale. The binding question: is "drop Docker / find
something lighter" the lever, or is it something else? Measured empirically on a
**throwaway Fly perf-2x/4GB Machine** (the exact prod VM shape), not estimated.

The prod baseline (documented in `deploy/fly/worker/fly.toml`, 2026-06-04): ~30
**memory-active** Python sandboxes (~110 MB each) before OOM; idle sessions ~10 MB;
the 2 CPUs never bound the slot count. `WORKER_MAX_SANDBOXES=24` leaves headroom.

## Headline result

**RAM is the hard ceiling, not CPU — and the cheap levers don't move it.** On
heavy (science-stack) Python sandboxes:

| Lever | Ceiling (was 30) | Δ density | Cost |
|---|---|---|---|
| crun vs runc | 30 | **0** | (wins −19% cold start) |
| footprint flags (malloc) | 30 | **0** | none |
| KSM, ASLR on | ~30 | **~0** (+1.2 MB/cont) | ksmd CPU |
| KSM, ASLR **off** | ~33–35 | **+3–5** | weakens ASLR + ksmd CPU |
| **zygote / CoW (fork)** | **81** | **+170% (2.7×)** | **breaks container-per-sandbox** |

The structural lever (fork heavy sandboxes from a pre-imported parent so the
~70 MB of library pages are shared copy-on-write) is the only thing that moves
the needle — and it trades the per-sandbox hardened-container boundary for
Piston-style in-container process isolation.

## Requirements / decisions that emerged

- **Do NOT invest in crun/footprint-flags/KSM for density on the heavy path** —
  measured ~0. crun is still worth adopting for its **−19% cold-start** and lower
  per-op overhead, just not as a density play.
- **The density roadmap is structural, not incremental:** zygote/CoW (2.7×),
  CRIU checkpoint of idle-but-live sessions, zram for blocked sessions, and the
  boring linear lever — more RAM (8 GB ≈ ~60).
- **"30 concurrent sandboxes" ≠ "30 computing at once."** Held-open interactive
  sessions are mostly blocked on stdin; the 30 ceiling is *simultaneous
  memory-active*. zram/CRIU let live-session count exceed physical-RAM count.
- **Prod image bug (P1, separate from density):** `languages/python-3.12` prunes
  every dir named `tests`, deleting `numpy/_core/tests/_natype` that
  `numpy.testing` imports → **any user code importing scipy / scikit-learn /
  statsmodels / seaborn crashes at import**. See `FINDINGS-prod-image-numpy-testing.md`.

## Spikes

| # | Name | Type | Validates | Verdict | Tags |
|---|------|------|-----------|---------|------|
| 001 | ksm-dedup | standard | KSM dedups same-image anon pages enough to raise the ceiling | ⚠ PARTIAL — works, small; ASLR-bound | density, ksm, memory |
| 002 | crun-vs-runc | comparison | crun lowers per-container RAM and/or startup vs runc | ✓ crun WINS startup (−19%); density TIE | density, runtime, crun |
| 003 | runtime-footprint | standard | malloc/idle flags cut active or idle footprint | ✗ INVALIDATED — no measurable effect | density, memory, tuning |
| 004 | dind-vs-containerd | standard | dropping dockerd reclaims meaningful RAM for slots | ⚠ INCONCLUSIVE (probe unreliable); density-neutral | density, daemon, containerd |
| 005 | zygote-cow | frontier | fork-from-pre-imported-parent shares library pages, raising the ceiling | ✓ VALIDATED — 2.7× (30→81) | density, cow, fork, isolation |

## Environment (all measurements)

Fly Machine `performance-2x`, 4096 MB, region gru. Kernel **6.12.91-fly**
(Firecracker guest). MemTotal 4010640 kB (~3.83 GiB). dockerd 27.5.1, **runc
1.2.4**, **crun 1.20**. **KSM present and writable** in the Firecracker kernel
(incl. the 6.7+ KSM advisor / smart_scan knobs). Per-sandbox config mirrors prod:
`--read-only --network none --user 65534:65534 --memory 128m --pids-limit 64
--cap-drop ALL --security-opt no-new-privileges`, science-stack image built from
`languages/python-3.12/Dockerfile`. Shared harness in `_harness/`.
