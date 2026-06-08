# Zygote density tier (warm-pool copy-on-write)

The **zygote tier** is an optional, opt-in execution backend for the worker. Instead
of one hardened container per job, it routes each job to a long-lived,
per-`(language, version)` **warm parent** pool container that `fork()`s one hardened
child per job. The children share the parent's pre-imported library pages
**copy-on-write**, so a node fits far more concurrent "heavy" interpreted sandboxes
in the same RAM.

It sits **alongside** the default `DockerSocketRunner` behind the same `Runner`
interface — a density-vs-isolation tier, not a replacement. It is **OFF by default**
(`ZYGOTE_ENABLED`); dev, CI, and any worker that doesn't flip the flag behave exactly
like a plain per-job-container worker.

> **TL;DR for operators:** Set `ZYGOTE_ENABLED=true` only on a Fly worker (where the
> Firecracker microVM is the real host boundary). Python runs on the warm pool;
> R / Rust / SQLite stay on the per-job Docker tier. If anything goes wrong the job
> transparently falls back to Docker — enabling zygote can't break Python.

---

## Why it exists

RAM is the hard ceiling on how many sandboxes a worker node can hold. A heavy Python
sandbox (numpy + pandas + matplotlib imported) is ~110 MB resident, and a `perf-2x`/4 GB
worker OOMs at ~30 concurrent. But that 110 MB decomposes into **~70 MB of identical
library/interpreter pages + ~40 MB of unique user working set**. The shared 70 MB is the
lever.

A zygote parent imports the science stack **once**; every forked child shares those
~70 MB physically via copy-on-write and pays only for its own ~40 MB working set. The
measured result (spike 005):

| Model | Concurrent ceiling | Marginal RAM / sandbox |
|---|---|---|
| container-per-sandbox (today's Docker tier) | 30 | ~110 MB |
| **zygote / fork** | **81** | **~41 MB** |

**2.7× density** on the same 2 CPU / 4 GB box, and the marginal ~41 MB is essentially
the unique working set alone. A smaller real working set → an even higher ceiling.

The critical follow-up question — *does that win survive making it safe for untrusted
code?* — was answered by spike 006: **per-child hardening is free.** Layering distinct
UID, no-new-privs, mount/PID/network namespaces, and a per-child cgroup-v2 sub-cgroup on
top of the fork left the curve flat:

| Hardening level | Concurrent ceiling | Marginal / child |
|---|:---:|:---:|
| L0 — plain fork (spike 005) | 82 | 40.5 MB |
| L1 — + distinct UID + `no_new_privs` | 81 | 40.9 MB |
| L2 — + mount-ns + private `/tmp` + remounted `/proc` | 82 | 39.5 MB |
| L3 — + per-child PID namespace (double-fork) | 81 | 41.4 MB |
| **L4 — + per-child cgroup-v2 (`memory.max`+`pids.max`)** | **81** | **41.6 MB** |

Hardening consumes effectively zero RAM (it doesn't touch the CoW-shared import base),
and the isolation it buys was proven sufficient (no sibling can read another's
`/proc/<pid>/mem`, `/tmp`, or FDs — see [Security posture](#security-posture)).

---

## Tiered model

The worker (when zygote is enabled) wraps two runners in a `TieredRunner` and routes
each job by a single, **manifest-driven** predicate — there is **no language-name
branching anywhere**:

```
                          ┌─ zygote-eligible?  ── yes ──▶  ZygoteRunner   (warm-pool CoW tier)
  job ─▶ TieredRunner ────┤   (manifest has a
                          │    non-empty preimport)
                          └─ everything else ──────────▶  DockerSocketRunner (per-job hardened container)
```

A job is routed to the zygote tier only when **both**:

1. a `ZygoteRunner` is configured (i.e. `ZYGOTE_ENABLED=true`), **and**
2. the job's resolved manifest is **zygote-eligible** — `manifest.ZygoteEligible(m)`,
   which is simply "the manifest declares a **non-empty `preimport`** array".

When the zygote runner is nil (the default) every job goes to Docker, so an unset
`ZYGOTE_ENABLED` worker is byte-for-byte the old behaviour for all four languages.

### What runs where (today)

| Language | Tier | Why |
|---|---|---|
| **Python 3.12** | **zygote** | Interpreted, heavy imports (`numpy, pandas, scipy, sklearn, matplotlib.pyplot` in its manifest) → big CoW win. |
| R 4.4 | Docker | See [R status](#r-status) — Docker-tier for now. |
| Rust | Docker | Compiled, no import base → zero CoW benefit. |
| SQLite | Docker | No import base → zero CoW benefit. |

### Opting a language in

The tier decision is entirely in the manifest. To make a language zygote-eligible, add
a non-empty `preimport` array to its `languages/<lang>-<version>/manifest.json` — the
modules/libraries the parent should import once at warm time:

```json
{
  "language": "python",
  "version": "3.12",
  "preimport": ["numpy", "pandas", "scipy", "sklearn", "matplotlib.pyplot"]
}
```

Remove the `preimport` key (or leave it empty) and the language routes to the Docker
tier instead.

**Constraint:** this only makes sense for **interpreted, heavy-import** languages.
Compiled languages (Rust) and no-import workloads (SQLite) get no CoW benefit, and the
zygote tier does not support a compile step — a compile-bearing manifest is routed to
the Docker tier. The image must also ship a zygote **agent** for that language (Python
ships `zygote_agent.py`; see below) and run it as the pool container's command.

---

## Architecture

```
worker (Go, dind on Fly)                 pool container (privileged, per language+version)
┌───────────────────────────┐            ┌─────────────────────────────────────────────┐
│ TieredRunner              │  TCP/relay │ zygote agent = the language runtime, warm:    │
│  ├─ ZygoteRunner ─────────┼───────────▶│  - pre-imported preimport set (CoW base)      │
│  │   └─ pool manager      │  one conn  │  - PR_SET_CHILD_SUBREAPER, cgroup base set up │
│  │      (1 warm parent /  │  per job   │  - accept(job): write files, socketpairs,     │
│  │       language+version)│            │     double-fork + harden child, dup2 stdio,   │
│  └─ DockerSocketRunner    │            │     cgroup-place, run entrypoint in-process    │
│     (R/Rust/SQLite +      │            │  - relay: conn ⇄ child stdio, cpu push, EXIT  │
│      zygote fallback)     │            │  - conn close ⇒ cgroup.kill (worker Kill)     │
└───────────────────────────┘            └─────────────────────────────────────────────┘
```

**The pool container** is launched by the worker via the same Docker SDK the
`DockerSocketRunner` uses, but it is **long-lived, privileged, and network-attached**
(so the worker can reach its relay port), and there is **one per `(language, version)`**
(CoW only shares within the same image). It is *not* a per-job container.

The worker reaches the agent over **plain TCP** on the pool container's Docker-network
IP (inspected via the SDK), on `ZYGOTE_RELAY_PORT`. **One TCP connection carries one
job** end-to-end.

### Worker ↔ agent relay protocol

Framed, length-prefixed: each frame is `[1-byte type][4-byte big-endian length][payload]`.
One connection per job.

**Worker → agent:**

| Frame | Type | Payload |
|---|---|---|
| `HELLO` | `0x01` | JSON `{jobId, entrypoint, files:[{name,content}], uid, memMaxBytes, pidsMax, tmpfsBytes}` — first frame |
| `STDIN` | `0x02` | raw stdin bytes |
| `STDIN_CLOSE` | `0x03` | deliver EOF to the child's stdin |
| `KILL` | `0x04` | kill the child (agent does `cgroup.kill`); also implied by the connection closing |

**Agent → worker:**

| Frame | Type | Payload |
|---|---|---|
| `STARTED` | `0x10` | JSON `{realpid, cgroupEnforced}` — after fork + harden + cgroup placement succeed |
| `STDOUT` | `0x11` | raw stdout bytes |
| `STDERR` | `0x12` | raw stderr bytes |
| `CPU` | `0x13` | JSON `{cpuMs}` cumulative, pushed ~every 100 ms (feeds the session CPU clock) |
| `EXIT` | `0x14` | JSON `{exitCode, signal}` terminal; the agent then closes the connection |

The Go side wraps this so the `Sandbox` interface looks identical to the Docker tier:
`Stdin()` is an `io.WriteCloser` emitting `STDIN`/`STDIN_CLOSE`; `Stdout()`/`Stderr()`
are readers fed by demuxed frames; `Wait()` blocks on `EXIT`; `Kill()` sends `KILL` (or
closes the conn); `CPUReader()` returns the latest pushed `cpuMs`. The three clocks
(wall / idle / CPU) are enforced by `internal/session` exactly as on the Docker tier.

### Per-job child hardening

For each job the agent (running inside the privileged pool container) performs a
**double-fork** and hardens the child *before* user code runs:

- **double-fork for a private PID namespace** — a process may `unshare(CLONE_NEWPID)`
  only once, so a thin intermediate unshares once and forks the real session as PID 1
  of a fresh pidns, then exits; the session reparents to the agent
  (`PR_SET_CHILD_SUBREAPER`).
- **distinct UID per child** (`UID_BASE + n`, `setgroups([])`, `setresgid`/`setresuid`).
- **`prctl(PR_SET_NO_NEW_PRIVS, 1)`**.
- **private network namespace** (`unshare(CLONE_NEWNET)`) — the child has **no network**
  even though the pool container does.
- **private mount namespace** (`unshare(CLONE_NEWNS)`) + rec-private `/` +
  a **private `/tmp` tmpfs** (size from the job limits) + remounted `/proc`.
- **per-child cgroup-v2 leaf** with `memory.max` + `pids.max` (placed by the agent using
  the session's real root-ns pid, returned over the double-fork pipe).
- **fd scrub** — every fd `> 2` is closed before user code, then the child's stdio
  socketpair ends are `dup2`'d onto 0/1/2.

Job files are **never** written to a shared disk dir: they ride into the child via
`fork()` and are materialized into the child's **own** private `/tmp` tmpfs, so sibling
children can never read them. CoW sharing of the pre-imported pages is preserved
throughout.

---

## Security posture

The zygote tier deliberately **inverts** the per-sandbox privilege posture for the
*pool* container, and that is acceptable only under a specific deployment + threat model.

### Why the pool container is privileged

To create namespaces and cgroup leaves for its children, the agent needs
`CAP_SYS_ADMIN` + `CAP_SETUID`/`CAP_SETGID` and a writable cgroupfs — i.e. the pool
container runs **privileged with host cgroups** (`--cgroupns=host`). This is the
opposite of the per-job Docker sandbox, which runs `--cap-drop ALL`.

This is acceptable **only because** on Fly the worker runs its own dockerd **inside a
Fly Machine, which is a Firecracker microVM** — the **microVM is the real host
boundary**. The [threat model](redis-constraint.md) for code-runner is
**host-escape-only**: sandbox→sandbox is discounted (sessions are ephemeral; no secret
lives in a sibling). A privileged pool container can at worst escape into the worker's
*inner* dockerd / Machine — never onto the Fly host. So in-container caps are not the
boundary that matters.

This is also why the tier is **gated behind `ZYGOTE_ENABLED`, default OFF**: dev and CI
keep the strong per-sandbox `DockerSocketRunner` posture, and the privileged pool only
ever runs where Firecracker backs it.

The **per-job Docker-tier hardening is unchanged** — R / Rust / SQLite (and any zygote
fallback) still run `--cap-drop ALL`, `no-new-privileges`, read-only root,
`network=none`, seccomp. Only the warm-pool *parent* is privileged, and the per-child it
forks is re-hardened by the agent (distinct UID + namespaces + cgroup, above).

### Isolation guarantees (spike 006, validated)

At full hardening, a child:

- **cannot** read a sibling's `/proc/<pid>/mem` (the sibling isn't in its pidns);
- sees only itself in `/proc` (`["1"]`);
- has its **own** `/tmp` (reads only its own files);
- cannot address the parent across the pidns.

### The credential-free-parent rule (rule #1)

The single most important rule. The zygote runs user code via `fork()` **without
`exec()`**, and **`fork()` inherits open file descriptors**. Critically:

> **`CLOEXEC` does NOT help here.** `O_CLOEXEC`/`FD_CLOEXEC` only close fds on
> `execve()` — and no `exec()` happens. An inherited Redis/soketi socket stays open and
> **usable** by untrusted child code, even inside an empty network namespace (a fresh
> netns does not revoke an already-open fd). This was demonstrated, not assumed.

The two fork-safe mitigations, both applied:

1. **The parent/agent is credential-free** — it holds **zero** Redis / soketi / job-queue
   FDs or secrets. The relay socket it does hold carries no secrets. The worker talks to
   the agent only over that relay.
2. **Defense in depth:** the child **scrubs every fd `> 2`** before running user code, so
   even an accidentally-inherited socket can't survive into user code.

---

## Resilience: fallback to Docker

Enabling the zygote tier **cannot break Python.** If a zygote-eligible job's `Create`
fails for any reason — the warm pool won't start, the agent dial fails, the image lacks
the agent, etc. — the `TieredRunner` **transparently falls back** to the always-present
hardened Docker tier rather than failing the job. The fallback is:

- **logged loudly** (a `WARN`: `zygote Create failed; falling back to Docker tier`), and
- **counted** via `code_runner.zygote.fallback.count`.

A degraded zygote tier silently serving everything via Docker is therefore **observable,
never invisible** — watch that counter. Only `Create`-time failures fall back; once a
sandbox is returned the session owns its lifecycle. A cancelled context is not treated as
a zygote fault (no fallback).

---

## Operations

### Config knobs

All zygote knobs are only meaningful (and only validated) when `ZYGOTE_ENABLED=true`; a
Docker-only worker never trips on them.

| Env var | Meaning | Default |
|---|---|---|
| `ZYGOTE_ENABLED` | Master switch. `true`/`1` builds the `TieredRunner` (Docker + zygote). Otherwise the worker is plain `DockerSocketRunner`. Set `true` **only on a Fly worker**. | `false` |
| `ZYGOTE_RELAY_PORT` | TCP port the agent listens on inside each pool container; the worker dials the pool container's Docker-network IP here. Must match the agent's build. | `7000` |
| `ZYGOTE_POOL_IDLE_MS` | Idle window: a warm parent with no in-flight jobs for this long is reaped to reclaim RAM. | `300000` (5 min) |
| `ZYGOTE_UID_BASE` | Base UID for per-child UID assignment inside the pool container (child uid = base + n). | `100000` |
| `ZYGOTE_POOL_MEMORY_MB` | Memory cap (MiB) for each **warm parent** pool container itself. Per-child `memory.max` is derived independently from each job's own `Limits.MemoryMb`. | `1024` |

### Pool lifecycle

- **Warm (lazy).** A parent is created on the **first job** for a `(language, version)`
  and kept warm. Pre-warming on boot for zygote-eligible manifests is acceptable too.
- **Idle reap.** After `ZYGOTE_POOL_IDLE_MS` with no in-flight jobs, the parent is torn
  down to reclaim RAM (counted: `code_runner.zygote.parent.reap.count`).
- **Dead-parent detect + respawn.** If the agent/container dies, it is detected on the
  next dial/health check, dropped, and respawned on the next request (counted:
  `code_runner.zygote.parent.respawn.count`). In-flight jobs that lose their connection
  fail cleanly (`EXIT` with error) and the worker slot is released — no leak.
- **Slot accounting** is unchanged: the worker's existing semaphore
  (`WORKER_MAX_SANDBOXES`) still bounds concurrency; the pool is orthogonal.

### The privileged-pool requirement on Fly

The pool container needs to run privileged with host cgroups. On the Fly reference
deploy this needs **no extra Machine flag**: the worker already runs its **own dockerd
inside the Machine** (it *is* the dind daemon, already privileged), so launching a
privileged pool container needs **no `[vm]` change and no extra cap**. See
[deploy-fly.md](deploy-fly.md#zygote-density-tier).

### Verifying zygote is actually engaged (not silently falling back)

"`ZYGOTE_ENABLED=true`" does **not** by itself prove jobs run on the warm pool. Confirm
with three signals:

1. **`code_runner.zygote.pool.warm_parents`** gauge > 0 for the expected
   `(language, version)` — at least one parent is alive.
2. **`code_runner.zygote.fallback.count`** is **flat** (a rising rate = zygote is
   degraded and Python is silently being served by Docker).
3. The agent's own startup log line (`[zygote-agent] listening on 0.0.0.0:<port>` /
   `pre-imported: [...]`) appears, and you do **not** see the worker `WARN`
   `zygote Create failed; falling back to Docker tier`.

Note also the one-time **`WARN`** the worker emits when the pool host can't delegate
cgroups (`per-child cgroup enforcement unavailable ... expected on Docker Desktop;
enforced on Fly/Linux`). On Fly/Linux with `--cgroupns=host` this should **not** appear;
seeing it on Fly means per-child `memory.max`/`pids.max`/`cpu.stat` are not being
enforced.

---

## Observability

All instruments are emitted through the same OTel meter scope as the rest of the worker
(`OTEL_EXPORTER_OTLP_ENDPOINT` is the on switch). `job_id` is never a metric attribute;
only the low-cardinality `language` / `version` are attached.

| Metric | Kind | Meaning |
|---|---|---|
| `code_runner.zygote.pool.warm_parents` | gauge | Live warm parents, per language+version. |
| `code_runner.zygote.fork.duration` | histogram (s) | Wall time of the fork+harden handshake (Create→`STARTED`), per language. |
| `code_runner.zygote.parent.reap.count` | counter | Warm parents torn down by the idle reaper. |
| `code_runner.zygote.parent.respawn.count` | counter | Warm parents dropped + respawned after dead-parent detection. |
| `code_runner.zygote.fallback.count` | counter | Zygote-eligible jobs that fell back to the Docker tier after a zygote `Create` error. |
| `code_runner.sandbox.terminal.count` | counter | **Runner-agnostic** terminal outcomes (`exited` / `killed`) by language — the same instrument the Docker tier feeds, so dashboards stay uniform across tiers. |

The kill path also feeds the existing runner-agnostic `sandbox` kill-latency histogram
shared with the Docker tier — the zygote work reuses the existing domain taxonomy rather
than inventing a parallel one.

---

## Testing & validation

The zygote safety/abuse/isolation/density/no-leak suite is:

```bash
make zygote-suite          # full suite
bash scripts/zygote-suite.sh --unit   # Docker-free metrics unit tests only
```

It runs in two parts:

1. **Pool-observability metrics unit tests** — Docker-free, always run.
2. **Privileged integration suite** — drives the **real** privileged Python pool agent
   through the relay protocol under the `internal/session` three-clock supervisor: abuse
   parity (fork bomb, OOM, infinite loop, idle, EOF, giant output), cross-child isolation
   (no sibling mem/proc/tmp/FD), CoW density (one parent for N children), and a no-leak
   sweep.

In CI this is the **`zygote` job** in `.github/workflows/abuse.yml`, run on
`ubuntu-latest` (native Linux). It builds `executor/python:3.12` (the agent is baked at
`/opt/zygote/zygote_agent.py`) and runs `make zygote-suite`. This is the **Fly-free
gate** that proves the tier before it is trusted in production.

> **macOS Docker Desktop limitation.** On Docker Desktop the worker process can't reach
> the pool container's docker-bridge IP, and cgroup-v2 delegation via `--cgroupns=host`
> isn't available. So host→bridge and cgroup-enforced cases (fork bomb, OOM, CPU-clock)
> **SKIP cleanly** locally — the code still compiles and the metrics unit tests still
> pass. On **native Linux / Fly** (the worker runs inside dind, the bridge IP is
> reachable, cgroups delegate) every assertion runs for real. SKIPs do not fail the run.

---

## R status

**R runs on the Docker tier for now — it does NOT run on the zygote tier in v1.1.**

Python is the validated zygote language; R was the second candidate behind an explicit
risk valve, and that valve was exercised. R's `manifest.json` has **no `preimport`**, so
`manifest.ZygoteEligible(r)` is false and the `TieredRunner` routes R to the
`DockerSocketRunner` — R works in prod exactly as today (per-job hardened container),
just without the warm-pool CoW tier.

Why R is deferred (short version): R cannot call `unshare`/`mount`/`prctl` without native
code, so it needs a C helper, and **evaluating embedded R in a double-forked,
UID-dropped, freshly-namespaced child** is exactly the fragility the design flagged
(R's signal handlers, allocator, IO layer, and `longjmp` error handling are all suspect
post-fork). The native C helper (`languages/r-4.4/zygote_hard.c`) already **compiles
cleanly** in the R image, which de-risks a future revisit, but job-file staging and an
end-to-end relay self-test to Python's bar are not yet done. The full reasoning lives in
`.planning/decisions/ZYGOTE-R-STATUS.md`.

**To move R onto the zygote tier later:** implement HELLO `files[]` staging into the
child's private `/tmp/work`, validate embedded-R `source()` from a double-forked /
UID-dropped / namespaced child (or fall back to `execve("Rscript", ...)`, which loses the
CoW pre-import benefit), drive the R agent through the same relay self-test the Python
agent passes, then add a `preimport` array back to `languages/r-4.4/manifest.json`.

---

## See also

- [`docs/deploy-fly.md`](deploy-fly.md) — the Fly reference deploy (zygote is enabled there).
- [`docs/scaling.md`](scaling.md) — the worker-node scaling model and RAM ceiling.
- [`docs/redis-constraint.md`](redis-constraint.md) — native-Redis requirement + threat-model context.
- `internal/runner/zygote.go`, `zygote_pool.go`, `tiered.go`, `zygote_metrics.go` — the implementation.
- `languages/python-3.12/zygote_agent.py` — the Python zygote agent.
