# code-runner

## What This Is

An internal, horizontally-scalable remote code execution service (Piston-style) written in Go. It accepts code + interactive stdin from an existing public TypeScript API (over a private network), runs the code inside ephemeral hardened sandboxes, and streams stdout/stderr/results in real time to clients via soketi (a Pusher-compatible server). It is **never exposed to the internet directly** — all trusted input arrives through the TS API; soketi is output-only toward the client.

It exists to safely run untrusted user code in multiple languages (Python, Rust, R, SQLite to start) with **live interactive sessions** (stdin kept open), strict resource limits, and an extensibility model where adding a language is "drop a folder + an image" with no core changes.

## Core Value

Run untrusted code in a hardened, resource-bounded sandbox with a **live interactive stdin session** and reliable real-time output — without ever leaking a container, a subscription, or a session slot.

## Requirements

### Validated

(None yet — ship to validate)

### Active

<!-- All hypotheses until shipped and validated. Detailed REQ-IDs live in REQUIREMENTS.md. -->

- [ ] Executor API (private, Go) exposing the internal contract: `POST /execute`, `/run/:jobId/start`, `/run/:jobId/stdin`, `/run/:jobId/stdin/close`, `/run/:jobId/kill`
- [ ] Redis-backed job queue decoupling reception from execution (backpressure)
- [ ] Redis pub/sub stdin channel `stdin:<jobId>` routing client input to the worker holding the sandbox (no service discovery)
- [ ] Stateless worker pool (N replicas) that launches ephemeral hardened sandboxes via the host container runtime (no Docker-in-Docker)
- [ ] Runner behind an interface so the runtime can change (Docker hardened → gVisor) without touching core logic
- [ ] Live interactive session model: process stays alive with open pipes; start-handshake (queued → subscribe → start) so no early prompts are lost
- [ ] Output publisher to soketi over the Pusher HTTP API on channel `private-run-<jobId>`: `stage`, `stdout`, `stderr`, `result` events
- [ ] Three independent clocks per session: wall-clock, idle, CPU (cgroup) — each kills the sandbox
- [ ] Full sandbox hardening per execution (network=none, read-only + tmpfs, no-swap memory cap, pids-limit, cpus, cap-drop=ALL, no-new-privileges, restrictive seccomp)
- [ ] Output byte caps (truncate + `truncated=true`), pending-stdin byte cap (backpressure → 429), stdin rate limit at the API layer
- [ ] Deterministic lifecycle/cleanup: on any terminal event unsubscribe stdin, close pipes, remove sandbox, free the slot — no leaks
- [ ] Manifest-driven language packages (`manifest.json` + `Dockerfile` per language); core reads manifests at boot, nothing hardcoded in Go
- [ ] Four initial language packages: Python 3.12, Rust, R 4.4, SQLite 3 (incl. interactive SQL shell)
- [ ] `docker compose` dev stack: executor + workers + redis + soketi + TS API stub
- [ ] Abuse test suite: fork bomb, OOM, infinite loop (wall-clock), stdin-blocked (idle), EOF (stdin/close), giant output (truncation)
- [ ] README: how to run, API contract, and the "add a new language" guide

### Out of Scope

- Building the public TypeScript API — **already exists**; we only respect its contract and ship a local stub/mock for dev
- Authenticating end users / authorizing Pusher channels — done by the TS API
- Exposing this service to the internet — it is internal-only, behind the private network
- Runtime dependency resolution inside sandboxes — images are pre-built with common libs baked in (Piston model)
- Inbound trust from soketi — soketi is output-only; nothing we trust enters through it
- Docker-in-Docker — workers talk to the host container runtime via a mounted socket in dev

## Context

- **Service position:** Internet → TS API (public, external, exists) → Executor API (private, Go) → Redis queue → Worker pool (N replicas) → hardened sandbox. stdout/stderr → soketi trigger; stdin → TS API → Redis pub/sub → owning worker → process pipe.
- **Trust boundary:** Every trusted input (code, stdin, control) enters via the TS API. soketi only carries output to the client.
- **Extensibility is a central requirement**, modeled on Piston: `languages/<lang-version>/{manifest.json, Dockerfile}`. The manifest declares `language`, `version`, `aliases`, `image`, `entrypoint`, `compile` (nullable), `run`, `defaultLimits` (`wallTimeMs`, `idleMs`, `cpuMs`, `memoryMb`, `pids`, `outputKb`), `interactive`.
- **Interactive session is the hard part:** the sandbox is NOT batch-ephemeral; it keeps the process alive with pipes open awaiting input, governed by three clocks.
- **Initial languages:** Python 3.12 (interpreted, `python main.py`, numpy/pandas/requests baked in), Rust (compiled, `rustc -O` then run binary), R 4.4 (interpreted, `Rscript main.R`, common libs), SQLite 3 (SQL against an ephemeral in-memory DB; `sqlite3` shell reading stdin; supports both `.sql` file and interactive session).
- **Build order (per user):** propose folder structure + manifest schema → implement core with Python end-to-end (interactive stdin + three timeouts) → validate → add Rust, R, SQLite reusing the same model. Atomic commits per milestone.

## Constraints

- **Tech stack**: Executor API + workers in **Go** — non-negotiable.
- **Tech stack**: **Redis** for both the job queue and the per-job interactive stdin channel.
- **Tech stack**: **soketi** (Pusher-compatible) for real-time output; publish via the Pusher HTTP API.
- **Architecture**: Executor API and workers must be **stateless** → scalable to N replicas. Capacity counted in concurrent live sandboxes (an interactive session holds a slot until it expires), not in request bursts.
- **Security**: Internal-only; never internet-facing. Full sandbox hardening applied to every execution. Restrictive seccomp profile.
- **Architecture**: **No Docker-in-Docker.** Worker talks to the host container runtime via mounted socket (dev). Runner behind an interface for future gVisor swap.
- **Extensibility**: Adding a language = add folder + pre-built image, zero core changes. No languages hardcoded in Go.
- **Dev**: Whole system runs with `docker compose up`, including a TS API stub.
- **Reliability**: stdin delivery via Redis pub/sub for MVP; Redis Streams + `XREAD BLOCK` left as an upgrade option for guaranteed delivery.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go for executor + workers | Required by spec; strong concurrency + container-runtime integration | — Pending |
| Redis pub/sub for stdin (MVP) | Simple, no service discovery; Streams upgrade path left open | — Pending |
| Runner behind an interface | Allow Docker-hardened → gVisor swap without touching core | — Pending |
| Manifest-driven languages (Piston model) | Extensibility without core changes is a central requirement | — Pending |
| Python implemented end-to-end first | De-risk interactive stdin + three timeouts before fanning out to other langs | — Pending |
| soketi output-only; all trust via TS API | Clear trust boundary; nothing trusted enters via real-time channel | — Pending |
| Pre-built language images, no runtime dep resolution | Piston model; predictable, fast, no network in sandbox | — Pending |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd:complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-06-02 after initialization*
