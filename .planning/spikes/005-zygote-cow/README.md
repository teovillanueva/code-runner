---
spike: 005
name: zygote-cow
type: frontier
validates: "Given heavy Python sandboxes forked from a parent that pre-imported the science stack, when ramped under 2cpu/4gb, then the ~70 MB of library pages are shared copy-on-write and the concurrent ceiling rises well above 30"
verdict: VALIDATED
related: [001, 002, 003]
tags: [density, cow, fork, isolation, zygote]
---

# Spike 005: Zygote / Copy-on-Write density

## What This Validates

Given heavy Python sandboxes **forked** from a single parent that already
imported numpy + pandas + matplotlib, when ramped to the RAM safety floor on a
2cpu/4gb worker, then copy-on-write shares the parent's library pages physically
across all children and the concurrent ceiling rises far above the 30 of the
container-per-sandbox model.

This spike was **discovered mid-session**: spikes 001–003 showed the cheap levers
move density by ~0, and the decomposition of a 110 MB sandbox (~70 MB shared
library pages + ~40 MB unique working set) pointed straight at CoW as the lever
that attacks the shared 70 MB.

## How to Run

`_harness/density-harness2.sh` (section B) + `_harness/zygote.py`. The parent
imports the stack once, then `os.fork()`s one child per "session"; each child
allocates a unique ~40 MB buffer and blocks. Ramps until host MemAvailable hits
the 220 MB floor.

```sh
docker run -d --name zygote --read-only --network none --user 65534:65534 \
  --memory 3600m --pids-limit 1024 --cap-drop ALL --security-opt no-new-privileges \
  -v /work/zygote.py:/zygote.py:ro spike/python:3.12 python /zygote.py
```

## Results — VALIDATED (the only lever that broke 30)

| Model | Ceiling | Marginal RAM / sandbox |
|---|---|---|
| container-per-sandbox (spike 001 baseline) | 30 | ~110 MB |
| **zygote / fork (this spike)** | **81** | **~41 MB** |

`ZYGOTE_CEILING=81  marginal_per_child_kb=41620  used_kb=3371240`

**2.7× density.** The marginal 41 MB ≈ the synthetic 40 MB unique buffer alone —
i.e. the ~70 MB of imported library pages are paid **once** and shared CoW across
all 81 children. You pay only for each session's *unique* working set. A smaller
real working set → an even higher ceiling.

## Investigation Trail

- Decomposed the 110 MB heavy sandbox: ~70 MB identical library/interpreter
  pages + ~40 MB unique user data. KSM (spike 001) tried to dedup the 70 MB
  *after the fact* and mostly failed (ASLR). CoW shares it *by construction*.
- Used a **unique random 40 MB buffer per child** so the result isn't inflated by
  identical buffers — the 2.7× is honest for a ~40 MB working set.
- Had to raise `--pids-limit` 64 → 1024: one container now hosts many sessions,
  so the prod per-sandbox pid budget doesn't apply unchanged.

## The cost (why this is not free)

- **Isolation downgrade.** Forked children share ONE container → the strong
  per-sandbox hardened-container boundary collapses to **in-container process
  isolation** (Piston's model). To stay safe for untrusted code you'd need
  per-child hardening *inside* the shared container: separate UIDs, per-child
  seccomp, prlimit, PID/mount/net namespaces, and careful fork hygiene.
- **Blast radius.** A child that corrupts or OOMs the parent can take down every
  session in that zygote. Needs supervision + per-child cgroup caps.
- **Workload sensitivity.** The 2.7× holds for a ~40 MB unique working set; the
  number scales inversely with how much *unique* memory each session touches.

## Signal for the build

This is the highest-leverage density lever for "heavy" (heavy-import) workloads,
because "heavy" is mostly *shared imports*, not unique data. It belongs in the
same design conversation as the planned `Runner` backends: a `ZygoteRunner`
(fork-per-session inside a hardened pool container) would sit alongside
`DockerSocketRunner` / `gVisorRunner` / `FlyMachinesRunner` as a
**density-vs-isolation tier**, not a drop-in replacement. Pair with the CRIU
research track (checkpoint idle-but-live sessions) for the compounding win.
