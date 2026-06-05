# Prod launch decisions — edalef (university exams)

**Bar: cannot fail during an exam. Priority: go live ASAP. Optimize cost after.**
Owner handed full decision authority (2026-06-05). These are locked unless the
owner overrides.

## North star

Exam reliability comes from **operational posture (fixed pre-warm + no scale-down
during the exam window)**, NOT from clever autoscaling. Density/cost optimization
(zygote) is a **fast-follow**, not a launch dependency. Every change to prod
before launch is risk; ship the minimum that protects the exam.

## Locked decisions

**D1 — Launch execution = container-per-sandbox (the proven path).**
Today's `DockerSocketRunner` on dind, measured at ~30 heavy Python sandboxes /
perf-2x-4GB node. **Zygote/CoW is the right density architecture and is DEFERRED
to the immediate post-launch fast-follow** (see roadmap). Rationale: zygote is new
security-critical code (per-child UID/PID-ns/sub-cgroup, credential-free parent,
fork hygiene); introducing it to a cannot-fail launch is the failure mode. Eat the
extra nodes at launch; swap to zygote for the 2.7× cost cut once live and stable.

**D2 — Exam reliability = fixed pre-warm + freeze scale-down in the window.**
Pre-provision the pool to peak concurrency (provision-pool.sh) and pin the
autoscaler floor (`FAS_STARTED_MACHINE_COUNT` min) ≥ peak for the exam window.
No machine stops while exams run. Scale-down (cost) happens only between exams.
The "smart" autoscaler is a cost tool, never the reliability mechanism.

**D3 — Drain authoritative + wallTime cap.** Invariant asserted at boot:
`kill_timeout ≥ WORKER_DRAIN_TIMEOUT_MS ≥ maxWallMs`. Cap `wallTimeMs` server-side
in the API at **300s**; a request over the cap is **rejected 400** (explicit
contract — no silent clamp that surprises a student mid-exam). Worker is
`cpu_kind=performance` so Fly allows kill_timeout far above today's 130s. Today
the drain is wired (`WORKER_DRAIN_TIMEOUT_MS`, default 120s) but wallTime is
uncapped (`mergeLimits` in `apps/api/src/routes/execute.ts`) → a long session can
outlive the drain → fix is the cap. Set: cap 300s → drain 300s → kill_timeout 330s.

**D4 — Memory protection (GATED on spike 006 OOM containment).**
- If OOM is **cgroup-contained** (only the offending sandbox dies, dockerd/worker
  survive): use **memory-weighted slots** — each job consumes
  `ceil(memoryMb / unit)` of a larger pool. Simple, no variable reservation, no
  re-queue churn. Preferred.
- If OOM **cascades** (kills dockerd/worker → takes down every co-located student):
  mandatory **explicit MB reservation** at claim (reserve the cap, since
  memory==memory-swap, no swap relief) against `WORKER_MEM_BUDGET_MB`, re-queue on
  no-fit, lower effective per-node capacity.
Either way: a burst of heavy R(256MB)/Rust(512MB) jobs must never OOM a node below
the flat slot count. Today the slot is a flat semaphore (no memory weight).

**D5 — Autoscaler signal (between exams) = max(slot-pressure, mem-pressure).**
Export `code_runner_mem_reserved_bytes` (Σ caps of live sandboxes — drops only on
session end → scale-down-safe) + per-node budget; desired =
`max(ceil(inflight/slots), ceil(Σmem_reserved/budget))`. Fast-follow; not
launch-critical given D2.

**D6 — Threat model (recorded).** Only sandbox→host escape matters; sandbox→
sandbox is discounted (ephemeral; exam answer key lives in edalef's backend, never
in a sibling). Mandatory rule for the future ZygoteRunner: **fork from a bare,
credential-free parent** — never inherit the worker's Redis/soketi FDs into
untrusted code (would let a student forge another's passing result / read the
queue). Cheap per-child hardening (UID + PID-ns + no-ptrace + private /tmp) covers
the exam-integrity edge.

## LAUNCH-BLOCKER (not deferred): prod image `numpy.testing` is broken

`languages/python-3.12` prunes every `tests/` dir → deletes
`numpy/_core/tests/_natype`, so `numpy.testing` fails to import → **any user code
importing scipy / scikit-learn / statsmodels / seaborn crashes at import**. If any
exam uses the scientific stack (the image's whole purpose), it fails today. **Fix +
build-time import smoke test before launch.** See
`../spikes/FINDINGS-prod-image-numpy-testing.md`.

## Pre-warm sizing

`peak_nodes = ceil(expected_concurrent_students / safe_per_node)`,
`safe_per_node = 24` (headroom under the measured 30; flat until D4 lands).
Examples: 150 students → 7 nodes; 300 → 13; 500 → 21.
With zygote (fast-follow): `ceil(students / ~70)` → 300 → 5 nodes (the 2.7× cut).

## Launch checklist

- [ ] **Fix numpy.testing image bug + import smoke test** (launch-blocker)
- [ ] wallTime cap 300s (reject>cap) in API; boot invariant kill_timeout≥drain≥maxWall
- [ ] Pre-warm pool sized to peak + ~20% margin; autoscaler floor pinned for window
- [ ] Memory protection per D4 (after spike 006)
- [ ] **Spike 006**: OOM containment + drain-under-real-stop + 9am burst latency
- [ ] Rollback rehearsed: `fly releases` + redeploy previous `:sha-` image

## Deferred roadmap (post-launch, ordered by leverage)

1. **ZygoteRunner per-language** (hardened) → 2.7× density → ~minimize cost. Biggest win.
2. Memory-aware autoscaler metric (D5) + scale-down reconciler (fly-autoscaler up,
   own controller picks idle nodes to stop).
3. crun as default runtime (−19% cold start; one-line `HostConfig.Runtime`).
4. CRIU checkpoint → idle-session density **and** live-migration/consolidation for
   true scale-down with stateful sessions.
