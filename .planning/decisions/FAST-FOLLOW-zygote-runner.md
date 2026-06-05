# Fast-follow #1: ZygoteRunner (per-language CoW density) — ready to execute

**Status:** designed + measured, NOT in the launch. This is the first post-launch
project. Goal: cut worker cost ~2.7× by sharing each language's heavy imports
across sessions via copy-on-write, behind the existing `Runner` interface as a
**density-vs-isolation tier** (not a replacement for DockerSocketRunner).

## Why (measured, not estimated)

Spike 005 (`.planning/spikes/005-zygote-cow/`) on a real perf-2x/4GB Fly machine:
fork heavy Python sandboxes from one parent that pre-imported the science stack →
**30 → 81 concurrent (2.7×)**, marginal RAM per sandbox **110 MB → 41 MB** (the
~70 MB of library pages are paid once, shared CoW; you pay only each session's
unique working set). The incremental levers (crun/footprint-flags/KSM) moved
density by ~0 — CoW is the only thing that broke 30.

This is the right architecture for code-runner's threat model (see [[code-runner-threat-model]]):
zygote weakens **sandbox→sandbox** isolation, which the owner explicitly discounts
(ephemeral; exam answer key lives in edalef's backend, never in a sibling). It does
**not** weaken **sandbox→host** isolation — the parent container stays hardened and
on Fly everything stays inside the Firecracker microVM.

## Non-negotiable design rules

1. **Parent is bare + credential-free.** `fork()` inherits the parent's open FDs.
   The zygote parent must NEVER hold the worker's Redis/soketi connections, or
   untrusted children inherit them and can forge another student's passing result
   / read the queue. The zygote pool is a separate process from the worker; the
   worker talks to it over a minimal pipe. (This is the single most important rule.)
2. **Per-child hardening inside the shared container:** separate UID per child +
   PID namespace + `no_new_privs`/no-ptrace + **private /tmp** (own tmpfs) +
   **per-child sub-cgroup** (cgroup v2 nested: own memory + pids limits). Without
   the sub-cgroup, one child can fork-bomb / OOM and starve siblings.
3. **Per-language pools.** CoW only shares among children of the SAME parent (same
   image). One warm parent per (language, version). The 2.7× applies only to
   **interpreted, heavy-import** languages (python, r, node); compiled (rust) gets
   ~0 from import-sharing — keep it on the container-per-sandbox path.
4. **Pre-import the manifest's common set** in the parent so the shared set is
   maximal/resident once. Add a `preimport` field to `manifest.json`.
   PREREQUISITE: the python image's numpy.testing fix (shipped pre-launch) — a
   parent that pre-imports scipy/sklearn would have crashed before that fix.

## Budgeting + scheduling implication

Worker RAM = `Σ_language(parent base ≈ import footprint) + Σ_sessions(unique WS)`.
You pay one import-base **per language present on a node** → **fewer languages per
worker = more density**. Pin workers to a language (language affinity) so a node
doesn't pay 4 bases. This changes autoscaling to **per-language pool scaling**
(see DECISIONS-prod-launch D5) and adds a **zygote warm-up** cost to scale-up
(pre-warm the parent, don't wait for the first session).

## Execution plan (do in this order)

1. **Spike 006-style hardening probe** (throwaway Fly): does the measured 2.7×
   survive per-child UID + PID-ns + sub-cgroup + private-tmp? Measure the multi-
   language case (python + r parents on one node) for the real base cost. Verify a
   child CANNOT see a sibling's `/proc/<pid>/mem`, FDs, or /tmp, and CANNOT reach
   any Redis/soketi FD.
2. Implement `ZygoteRunner` behind the `Runner` interface: warm per-language parent
   pool, fork-per-session with the rule-2 hardening, credential-free parent +
   worker pipe (rule 1), idle-parent reaping.
3. Wire language affinity into scheduling + per-language autoscaler metrics.
4. Roll out as a **tier**: keep DockerSocketRunner as the fallback/strong-isolation
   path; route heavy interpreted languages to ZygoteRunner. Canary one language
   first (python), measure cost delta, then expand.

## Compounding next steps (after zygote)

- **CRIU** checkpoint of idle-but-live sessions → density AND live-migration for
  consolidation/scale-down with stateful sessions (DECISIONS roadmap #4).
- crun default runtime (−19% cold start), KSM+ASLR-off only for trusted/low-risk
  languages (+3–5, ASLR trade-off).
