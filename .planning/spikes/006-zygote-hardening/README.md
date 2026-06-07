---
spike: 006
name: zygote-hardening
type: standard
validates: "Given children forked from a pre-imported parent, when each child is hardened per-child (distinct UID + PID-ns + cgroup-v2 sub-cgroup + private /tmp), then the 2.7x CoW density measured in spike 005 survives AND a child cannot read a sibling's /proc/mem, the parent's FDs, a neighbor's /tmp, or use an inherited Redis/soketi socket"
verdict: VALIDATED
related: [005, 001, 002, 003]
tags: [density, cow, fork, isolation, zygote, hardening, security, cgroup, namespaces]
---

# Spike 006: Zygote hardening — does the 2.7× survive per-child isolation?

## What This Validates

Spike 005 measured the headline density lever: fork heavy Python sandboxes from a
parent that pre-imported the science stack → **30 → 81 concurrent (2.7×)**,
marginal **110 → 41 MB/child** — but with **no per-child isolation** (one
container, `pids 1024`, all children same UID/PID-ns/mounts). That is unsafe for
untrusted exam code.

The `ZygoteRunner` design (`.planning/decisions/FAST-FOLLOW-zygote-runner.md`,
rule #2) mandates per-child hardening *inside* the shared pool: distinct UID +
PID namespace + cgroup-v2 sub-cgroup (own mem/pids) + private /tmp. The binding
question this spike answers empirically, on the same throwaway Fly perf-2x/4GB
box as 005:

1. **Density** — does the 2.7× / 41 MB-marginal survive the hardening, and *which*
   layer (if any) costs density? Measured layered: L0 plain-fork → L1 +UID →
   L2 +mount-ns/private-tmp → L3 +PID-ns → L4 +sub-cgroup.
2. **Isolation** — at full hardening, can a child read a sibling's
   `/proc/<pid>/mem`, list the parent's FDs, or read a neighbor's `/tmp`? And the
   single most important rule (#1): can a child reach a Redis/soketi connection
   the parent holds? (`fork()` inherits FDs.)
3. **Multi-language base cost** — python + R parents co-resident on one node:
   what does each language's import base cost, and how does that change density
   budgeting (the design's "fewer languages per node = more density" claim)?

## Research / design grounding

- **Threat model** (`DECISIONS-prod-launch.md` D6, `[[code-runner-threat-model]]`):
  only **sandbox→host escape** matters. Sandbox→sandbox is discounted (ephemeral;
  the exam answer key lives in edalef's backend, never in a sibling). So the
  per-child hardening here is about **exam integrity** (a student can't read
  another's work / forge a result), not host containment — on Fly the host
  boundary is the Firecracker microVM regardless of in-container posture.
- **Mechanics of per-child hardening from a forking parent:**
  - *PID namespace per child*: the parent calls `unshare(CLONE_NEWPID)` **before
    each fork**; the next child becomes PID 1 of a fresh pidns while the parent
    stays in the root ns (so it can still address children by real pid for cgroup
    assignment). The child then `unshare(CLONE_NEWNS)` + remounts `/proc` so its
    `/proc` reflects only its own ns.
  - *Private /tmp*: child `unshare(CLONE_NEWNS)` + `mount -t tmpfs` on `/tmp`.
  - *Distinct UID*: `setresuid/gid` to a per-child UID **after** the mount work
    (setuid drops the caps that mount/unshare need), plus `PR_SET_NO_NEW_PRIVS`.
  - *Sub-cgroup*: parent creates a cgroup-v2 leaf, sets `memory.max`+`pids.max`,
    writes the child's real pid to `cgroup.procs`.
  - **Privilege cost (a finding, not an accident):** to create namespaces +
    cgroups for its children the pool process needs `CAP_SYS_ADMIN` +
    `CAP_SETUID/SETGID` + a writable cgroupfs — i.e. the **pool container is more
    privileged than today's per-sandbox container** (which runs `--cap-drop ALL`).
    Under the threat model this is acceptable *only because* Firecracker, not the
    container caps, is the host boundary on Fly. The spike runs the pool
    `--privileged` to measure; privilege adds no RAM, so density stays comparable.
- **CLOEXEC does NOT help here (critical nuance):** the zygote runs user code via
  `fork()` **without `exec()`**. `O_CLOEXEC`/`FD_CLOEXEC` only closes fds on
  `execve()`. So an inherited Redis/soketi socket stays open and usable in the
  child. The only fork-safe mitigations are (a) a genuinely **credential-free
  parent** (rule #1) or (b) the child **scrubs all fds > 2** before running user
  code. The isolation probe proves all four cases (leak / CLOEXEC-myth / both
  mitigations).

## How to Run

Throwaway Fly box `cr-spike-006` (perf-2x/4096MB/gru, edalef), `docker:27-dind` +
ext4 volume at `/var/lib/docker`, `DOCKER_TLS_CERTDIR=` — same recipe as 005
(`../CONVENTIONS.md`). Harness in `../_harness/` (`zygote_hardened.py`,
`isolation_probe.py`, `zygote_r.R`) + this dir's `build.sh` / `run.sh`.

```sh
# on the box, /work has the build contexts + scripts
sh build.sh            # builds spike/python:3.12 + spike/r:4.4 from repo Dockerfiles
sh run.sh              # Sections A (layered density) + B (isolation) + C (multi-lang)
# results: /work/run.log, /work/iso.json, RESULTS6 block
```

## What to Expect

- **Section A**: a ceiling per level. If hardening is "free", L4 ≈ L0 ≈ 005's 81.
  If a layer costs density, the curve drops at that layer.
- **Section B**: every cross-child attack `blocked`; FD scenario A & B leak=true
  (danger demonstrated), C & D leak=false (mitigations).
- **Section C**: python core/full base RSS, R base RSS + marginal, and a combined
  python+R ceiling with both bases resident.

## Investigation Trail

Two non-obvious mechanics surfaced while building the hardened probe — both are
real constraints the production `ZygoteRunner` must handle, not just test bugs:

1. **`ctypes` mis-marshals the large `CLONE_*` flags without declared `argtypes`.**
   `libc.unshare(CLONE_NEWPID)` returned `EINVAL` until `unshare.argtypes =
   [c_int]` / `restype = c_int` were declared (same for `mount`/`prctl`). Without
   the prototype, ctypes guessed the marshalling of `0x20000000` wrong. (Fix:
   declare prototypes for every libc syscall wrapper.)

2. **A process can `unshare(CLONE_NEWPID)` only ONCE** — the 2nd call in the same
   process is `EINVAL` (measured: call A `rc=0`, call B `rc=-1 errno=22`). So the
   naive "unshare before each fork" gives every child the SAME pidns, not its own.
   To give EACH session its own PID namespace the pool must **double-fork**: fork
   a thin intermediate, which unshares once and forks the real session as PID 1 of
   a fresh pidns, then exits (the session reparents to the pool). Consequence for
   the real runner: **no `PR_SET_PDEATHSIG`** on the session (its immediate parent,
   the intermediate, is gone), and the supervisor must track the session's
   root-ns pid via the intermediate (a pipe in the spike) to place it in a cgroup.

3. **Cgroup delegation works** under `--privileged --cgroupns=host`: the probe
   built a delegated `…/<container>/zygote` subtree, moved itself into a `mgr`
   leaf (no-internal-process rule), enabled `+memory +pids`, and placed each
   session in its own `cN` leaf with `memory.max`/`pids.max` (`cg_ok` counts
   successes). Smoke at cap-3: `cg_ok=3`, marginal `~40 MB/child` — no inflation.

4. **The `/proc/<parent>/fd` probe hits a PID-1 collision artifact:** the pool's
   python is PID 1 of the container and each session is PID 1 of its own pidns, so
   targeting "parent pid 1" actually resolves to the session itself. The probe now
   detects the collision (and readlinks the fds to prove they're the session's
   own) — the real conclusion is that under a per-child pidns the parent is simply
   **not addressable** from the session.

## Results

**VERDICT: VALIDATED ✓ — the 2.7× survives full per-child hardening, isolation
holds, and design rule #1 is proven. `ZygoteRunner` is viable as a density tier
behind the `Runner` interface.** Raw data: `run.log`, `iso.json`.

### A. Hardening is free — the density curve is FLAT (the headline)

| Level | Per-child hardening added | Ceiling | Marginal / child | cgroup |
|-------|---------------------------|:-------:|:----------------:|:------:|
| L0 | none (plain fork = spike 005 baseline) | **82** | 40.5 MB | — |
| L1 | + distinct UID + `no_new_privs` | 81 | 40.9 MB | — |
| L2 | + mount-ns + private `/tmp` + remounted `/proc` | 82 | 39.5 MB | — |
| L3 | + per-child **PID namespace** (double-fork) | 81 | 41.4 MB | — |
| **L4** | **+ per-child cgroup-v2** (`memory.max`+`pids.max`) | **81** | **41.6 MB** | **81/81** |

Spike 005 (no isolation): **81 / 41 MB**. Fully-hardened L4: **81 / 41.6 MB** —
**identical within noise**. Every layer is flat (39–42 MB marginal, ceiling 81–82).
`cg_ok=81` means all 81 sessions got their own mem/pids-limited sub-cgroup. The
**2.7× (30→81) density is fully preserved under the complete per-child hardening
the design mandates.** Hardening consumes effectively zero RAM — CoW sharing of
the ~70 MB import base is untouched by UID/namespace/cgroup isolation.

### B. Isolation holds + design rule #1 proven (`iso.json`)

Cross-child, at full L4 hardening (each session its own UID + pidns + mount-ns +
cgroup + private `/tmp`):
- **read sibling `/proc/<pid>/mem` → blocked** (`FileNotFoundError`: the sibling
  isn't in the session's pidns).
- **`/proc` shows only `["1"]`** — the session sees only itself.
- **private `/tmp`** — each session reads only its own secret.
- the parent is **not addressable** across the pidns (the "parent fds" probe
  resolves to the session's *own* fds — `/dev/null` + its own pipes — confirmed by
  readlink; a PID-1 collision artifact, not a leak).

FD-inheritance (the single most important rule — the zygote forks **without
`exec`**):
| Scenario | Leak? | Meaning |
|----------|:-----:|---------|
| A. credentialed parent, child in its own netns | **YES** | child used the inherited socket *despite* an empty netns — **network isolation does not revoke an open fd** |
| B. socket marked `CLOEXEC`, fork (no exec) | **YES** | **`CLOEXEC` does NOT protect** — it acts only on `execve()` |
| C. credential-free parent (rule #1) | no | nothing to inherit |
| D. child scrubs all fds > 2 before user code | no | safe even with a credentialed parent |

⇒ **Mandatory for the build:** the zygote parent MUST be credential-free (no
Redis/soketi/queue FDs) **and** the session SHOULD scrub fds > 2 before exec'ing
user code as defence-in-depth. `CLOEXEC` is *not* a substitute (no exec happens).

### C. Multi-language base cost (informational — scheduling input)

Per-language parent **import base** (resident, paid once per language per node):

| Parent | Pre-import set | Base RSS |
|--------|----------------|:--------:|
| python (core) | numpy + pandas + matplotlib | **~104 MB** |
| python (full) | + scipy + sklearn + statsmodels + seaborn | **~199 MB** |
| R | jsonlite + data.table + lpSolve + ggplot2 | **~107 MB** |

Combined python + R pools ramping on ONE 4 GB node coexisted (python still
reached 75 sessions vs 82 solo; R reached 76) — adding a 2nd language cost ~7
python slots, dominated by its one-time base. This confirms the design's budgeting
rule: **worker RAM ≈ Σ(language bases) + Σ(session working sets)** → **pin workers
to a language (affinity) so a node pays one base, not four.** If the python parent
pre-imports the *full* exam stack, budget ~200 MB for its base (vs ~104 MB core) —
a real per-node cost worth deciding (pre-import everything for zero first-use
latency vs. import-on-demand for a smaller base).

> Caveat: the R *per-child* marginal via `parallel::mcparallel` was confounded
> (solo R hit the `HARD_CAP=260` loop bound, not the memory floor — children whose
> `runif(5e6)` failed to allocate under pressure exited and were not counted, and
> the concurrent combined run double-counts the shared MemAvailable drop). The
> reliable 006c numbers are the per-language **base RSS** above, which is what the
> scheduling decision needs. R single-language density is approximately
> python-like for a comparable ~40 MB working set; the precise R ceiling was not
> cleanly isolated and is not needed for the tier decision.

## Recommendation

**Build the `ZygoteRunner` as a density-vs-isolation tier** behind the `Runner`
interface (per the fast-follow design), routing heavy interpreted languages
(python first) to it while `DockerSocketRunner` stays the fallback/strong-isolation
path. The hardening that makes it safe for untrusted exam code is **free on
density** (2.7× fully preserved), and the isolation is sufficient for the threat
model (exam integrity; host boundary stays Firecracker).

Non-negotiables the spike turned up for the implementation:
1. **Credential-free parent (rule #1)** — proven necessary: an inherited
   Redis/soketi fd is usable by untrusted code even under a fresh netns, and
   `CLOEXEC` does not help (no exec). Pool process must hold zero worker
   connections; talk to the worker over a minimal pipe.
2. **Double-fork for per-child PID-ns** — a process can `unshare(CLONE_NEWPID)`
   only once; the pool forks an intermediate that unshares then forks the session
   as PID 1 of a fresh pidns. ⇒ no `PR_SET_PDEATHSIG` on the session; track the
   session's root-ns pid (via the intermediate) for cgroup placement.
3. **Pool runs privileged-ish** — needs `CAP_SYS_ADMIN` + `CAP_SETUID/SETGID` +
   writable cgroupfs (`--cgroupns=host`) to create namespaces/cgroups for
   children. Acceptable ONLY because Firecracker, not container caps, is the host
   boundary on Fly. (Document this explicitly; it inverts the per-sandbox
   `--cap-drop ALL` posture for the *pool* container.)
4. **Per-child cgroup-v2 sub-cgroup works** (`cg_ok=81/81`) and is free — use it
   for OOM/fork-bomb blast-radius containment (own `memory.max` + `pids.max`).
5. **Language affinity in scheduling** — one import base per language per node;
   pin workers per language (ties into DECISIONS D5 per-language autoscaling).
