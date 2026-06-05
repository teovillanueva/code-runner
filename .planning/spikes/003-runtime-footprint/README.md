---
spike: 003
name: runtime-footprint
type: standard
validates: "Given the Python sandbox, when low-footprint flags are applied, then active (~110 MB) or idle (~10 MB) RSS drops measurably"
verdict: INVALIDATED
related: [001, 005]
tags: [density, memory, tuning]
---

# Spike 003: Runtime footprint flags

## What This Validates

Given the Python sandbox, when low-footprint runtime knobs are applied
(`MALLOC_ARENA_MAX=1`, `PYTHONMALLOC=malloc`, and an idle-vs-active comparison),
then the active (~110 MB) or idle (~10 MB) resident footprint drops measurably,
fitting more sandboxes.

## How to Run

`_harness/density-harness.sh` — config `footprint-runc` (active, with malloc
flags) and the `idle_footprint` probe (`CR_IDLE=1`, with/without flags).

## Results — INVALIDATED

| Config | Ceiling | Per container |
|---|---|---|
| baseline (no flags) | 30 | 116.8 MB |
| `MALLOC_ARENA_MAX=1` + `PYTHONMALLOC=malloc` | 30 | **117.8 MB (worse)** |

| Idle footprint | plain | flags |
|---|---|---|
| bare interpreter (`CR_IDLE=1`) | 4.44 MiB | 4.41 MiB (no change) |

**No measurable win — slightly worse on the active path.** The image already sets
`OMP_NUM_THREADS=1` / `OPENBLAS_NUM_THREADS=1`, so the numeric stack is
single-threaded and glibc allocates few arenas already; capping arenas buys
nothing. `PYTHONMALLOC=malloc` disables pymalloc's pooled allocator and made RSS
marginally worse. Idle footprint is already tiny (4.4 MiB) and flag-insensitive.

## Investigation Trail

- Confirmed the idle interpreter is ~4.4 MiB — far below the prod "~10 MB idle"
  rule of thumb (that number includes a slightly warmed session). Idle sessions
  are essentially free; they are not the constraint.
- The footprint that matters is the **active ~110 MB**, and it's dominated by
  imported-library pages, which no per-process malloc flag touches. The only way
  to cut that is to *share* it — spike 005 (CoW).

## Signal for the build

Drop runtime footprint flags as a density lever. The active footprint is library
pages, not allocator slack; share them (005) rather than trying to shrink them.
