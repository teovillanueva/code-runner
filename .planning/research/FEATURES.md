# Feature Research

**Domain:** Internal remote code execution engine (Piston-style) with live interactive stdin sessions
**Researched:** 2026-06-02
**Confidence:** HIGH (core model is fully specified in PROJECT.md; validated against Piston, Judge0, e2b, E2B PTY model)

## Context That Shapes Everything

This is **not** a public sandbox SaaS. The only client is a trusted TS API on a private network. Input is trusted (TS API has already authenticated/authorized the end user and the soketi channel). The *code being run* is untrusted, but everything entering our service — code, stdin, control calls — arrives via the trusted TS API. That single fact eliminates an entire category of features (auth, billing, quotas, channel authz, public rate limiting against abusers) that every competitor must build but we must deliberately **not**.

The genuinely hard, differentiating requirement is the **live interactive session**: a long-lived process with open stdin/stdout/stderr pipes, governed by three independent clocks, holding a capacity slot until it terminates — as opposed to the batch one-shot model that Piston/Judge0 ship by default.

## Competitor Reference Points

| System | Model | Relevance |
|--------|-------|-----------|
| **Piston** (engineer-man) | Batch `POST /execute` + experimental WebSocket `/connect` for interactive I/O; Isolate (namespaces, cgroups, chroot) for sandboxing; language packages with `run`/`compile` scripts and per-stage timeout/memory limits | **Primary model.** The manifest + "drop a folder" extensibility and three-stage (compile/run) execution come straight from here. |
| **Judge0** | Pure batch; `cpu_time_limit`, `cpu_extra_time`, `wall_time_limit`, `memory_limit`, `stack_limit`, `max_file_size`; multi-file; stdin (whole-buffer, not streamed); returns stdout/stderr/status/time/memory | Validates the resource-limit field set and the stage/result model. **No interactive streaming** — confirms that's the differentiator. |
| **e2b** | Firecracker microVMs; **PTY module** for interactive bidirectional terminal I/O with streaming callbacks; idle timeout (default ~5 min) + lifecycle (pause/resume/kill) | Validates the live-session model, idle timeout, and explicit lifecycle/kill semantics. Heavier (full VM + filesystem) than we need. |
| **Riza / Vercel Sandbox** | Managed one-shot/short-lived execution for AI tool-calling; minimal interactive support | Confirms most of the market is batch; interactive stdin is rare and valued. |

Key takeaway: **batch execution + resource limits + multi-file + stdin-as-buffer is table stakes** (every system has it). **Streaming interactive stdin with a start-handshake and three clocks is the differentiator** and the project's reason to exist.

---

## Feature Landscape

### Table Stakes (Expected of Any Code Execution Engine)

Missing these = the service is not a credible code runner.

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Batch one-shot execution (`POST /execute` → run to completion → result) | The baseline contract of every engine (Piston, Judge0). Interactive is a superset. | MEDIUM | Interactive session that never receives stdin and hits EOF immediately *is* the one-shot case. Implement one model, get both. |
| Multi-file submissions | Judge0 and Piston both accept a file list; real programs are not one file. Rust especially (`main.rs` + modules). | LOW | Manifest `entrypoint` names the file to run; write all files into the sandbox workdir. First file conventionally the entrypoint. |
| stdin delivery to the process | Universal. Even batch engines accept a stdin buffer. | LOW (batch) / HIGH (streaming) | Batch = write buffer + close. Streaming = the hard part (see differentiators). |
| Separate stdout / stderr capture | Callers need to distinguish program output from errors. | LOW | Two pipes, two event types. Never merge. |
| Exit code + signal in result | Standard process result; signal distinguishes OOM-kill (SIGKILL) / timeout-kill from clean exit. | LOW | `result{exitCode, signal}`. |
| Wall-clock timeout (kills runaway jobs) | Infinite loops are the #1 abuse case; Judge0 `wall_time_limit`, Piston `run_timeout`. | LOW | One of the three clocks. Non-negotiable. |
| Memory cap (OOM kill) | Prevents one job from exhausting the host; Judge0 `memory_limit`, Piston `run_memory_limit`. | MEDIUM | cgroup memory limit + **no swap** (swap defeats the cap). Manifest `memoryMb`. |
| Output byte cap with truncation flag | Unbounded output exhausts memory/network; competitors cap file size (`max_file_size`). | MEDIUM | Truncate at `outputKb`, set `result.truncated=true`, stop reading. Must not lose the exit event. |
| Compiled vs interpreted handling (compile stage) | Piston's compile/run split; Rust/Go/C++ need a compile step that can fail before run. | MEDIUM | Nullable `compile` in manifest. Compile failure → terminal result with compiler stderr, never reaches run stage. |
| Per-language packaging with versions + aliases | Piston `language`/`version`/`aliases`; callers request `python` or `py`. | MEDIUM | Manifest-driven. Core reads manifests at boot; nothing hardcoded. |
| Pre-baked language images (no runtime dep install) | Piston model; predictable, fast, no network needed. numpy/pandas/requests baked into the Python image. | MEDIUM | Out of scope to install at runtime (see anti-features). |
| Network isolation (`network=none`) | Untrusted code must not reach the internet or internal services. | LOW | Sandbox hardening flag. Table stakes for *any* untrusted execution. |
| Process/pids limit (fork-bomb protection) | A fork bomb takes down the worker otherwise. | LOW | cgroup pids limit. Manifest `pids`. |
| Deterministic cleanup on terminal events | A leaked container/slot/subscription is a capacity leak that compounds. | HIGH | On any terminal event: unsubscribe stdin, close pipes, remove sandbox, free slot. The reliability core. |
| Stage/lifecycle events (`queued`/`compiling`/`running`) | Callers need to know where a job is, especially with a queue + compile step. | LOW | `stage` event. Drives the start-handshake (see below). |

### Differentiators (Why This Service Exists)

These are where the project competes against just running Piston. They align directly with Core Value.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| **Live interactive stdin session** (process stays alive, pipes open, awaiting input) | The hard requirement. Enables REPLs, interactive prompts (`input()`), interactive SQL shells — impossible with batch engines (Judge0) and only experimental in Piston. | HIGH | The central technical challenge. Process is *not* batch-ephemeral; lives until a clock fires, EOF+exit, or kill. |
| **Start-handshake** (queued → client subscribes to realtime channel → explicit `start`) | Prevents the classic race: a program that prints a prompt immediately would emit it before the client subscribed, losing early output. The handshake guarantees no early prompts/stdout are lost. | MEDIUM | Job sits in `queued`; worker does NOT begin process execution until the explicit `/run/:jobId/start` call confirms the client is subscribed to `private-run-<jobId>`. This is the subtle correctness feature competitors skip. |
| **Three independent clocks** (wall-clock, idle, CPU/cgroup) | Wall-clock alone is insufficient for interactive: a session waiting on stdin should die from *idle*, not wall-clock; a CPU-spinning job should die from *CPU* even if it produces no output. Each clock kills independently. | HIGH | `wallTimeMs` (total lifetime), `idleMs` (since last stdout/stderr **or** stdin activity), `cpuMs` (cgroup CPU accounting). Distinct kill reasons in result: `timedOut` (wall), `idleTimedOut` (idle). CPU exceed = signal-killed. |
| **Streaming output event model** | Real-time `stdout`/`stderr` events over soketi as they happen, not a final buffer dump. Essential for interactive UX. | MEDIUM | Events: `stage`, `stdout`, `stderr`, `result`. Published via Pusher HTTP API to `private-run-<jobId>`. |
| **Slot/capacity accounting in concurrent live sandboxes** | Because sessions are long-lived, capacity is "concurrent live sandboxes," not request rate. Correct accounting prevents over-subscription and host exhaustion. | HIGH | A session holds a slot from `start` until terminal cleanup. Stateless workers + Redis make the count observable. Leak = phantom slot consumed forever. |
| **stdin backpressure + rate limiting** | A client flooding stdin into a slow/blocked process can exhaust memory. Pending-stdin byte cap (→ 429) + API-layer rate limit bounds it. | MEDIUM | `pending-stdin` byte cap returns 429 when exceeded; rate limit at the API layer (not the worker). Distinct from output caps. |
| **Explicit EOF / stdin-close semantics** | `input()`/`read()` loops and the SQLite shell need a real EOF to terminate cleanly rather than blocking until idle-timeout. | MEDIUM | `/run/:jobId/stdin/close` closes the write end of the pipe → process sees EOF. Without it, interactive programs hang to idle-timeout. |
| **Explicit kill semantics** | Caller-initiated termination (user cancels). Must tear down cleanly and free the slot like any terminal event. | LOW | `/run/:jobId/kill` → SIGKILL the process group → terminal `result` with the kill signal → full cleanup. |
| **Manifest-driven "drop a folder" extensibility** | Adding a language = add `languages/<lang-version>/{manifest.json, Dockerfile}` + build image, **zero Go changes**. Piston-style but enforced as a core invariant. | MEDIUM | Core reads manifests at boot. `interactive` flag and `defaultLimits` per language. The extensibility promise is a differentiator vs hardcoded engines. |
| **Pluggable runner interface (Docker-hardened → gVisor)** | Runtime can be swapped for stronger isolation without touching core logic. | MEDIUM | Interface boundary, not a user-facing feature, but a durability differentiator. |

### Manifest Field Reference (validated against Piston + project spec)

The project's manifest schema is a superset of Piston's runtime model, extended for the interactive case:

| Field | Source | Purpose |
|-------|--------|---------|
| `language` | Piston | Canonical name (`python`). |
| `version` | Piston | Runtime version (`3.12`). |
| `aliases` | Piston | Alternative names (`py`, `python3`). |
| `image` | This project | Pre-built container image for the language. |
| `entrypoint` | This project | File to run (`main.py`). |
| `compile` (nullable) | Piston (compile stage) | Compile command; `null` for interpreted langs. |
| `run` | Piston (run stage) | Run command. |
| `defaultLimits.wallTimeMs` | Piston `run_timeout` analog | Wall clock. |
| `defaultLimits.idleMs` | **New** (interactive) | Idle clock — no analog in batch engines. |
| `defaultLimits.cpuMs` | cgroup CPU | CPU clock. |
| `defaultLimits.memoryMb` | Piston `run_memory_limit` / Judge0 `memory_limit` | Memory cap (no swap). |
| `defaultLimits.pids` | cgroup pids | Fork-bomb cap. |
| `defaultLimits.outputKb` | Judge0 `max_file_size` analog | Output truncation threshold. |
| `interactive` | **New** | Whether the language supports live stdin sessions. |

Compile vs interpreted: `compile == null` → skip compile stage, go straight to run (Python, R, SQLite). `compile != null` → run compile first (Rust: `rustc -O main.rs -o main`); compile failure is terminal with compiler stderr surfaced and the run stage never entered.

### Output Event Model (validated)

| Event | Fields | Notes |
|-------|--------|-------|
| `stage` | `queued` \| `compiling` \| `running` | Drives the start-handshake; `running` only after `start`. |
| `stdout` | chunk | Streamed; subject to `outputKb` cap. |
| `stderr` | chunk | Streamed; includes compiler errors during `compiling`. |
| `result` | `exitCode`, `signal`, `timedOut`, `idleTimedOut`, `truncated`, `durationMs` | Single terminal event. Exactly one per job. Distinct boolean flags let the caller render the right reason. |

### Anti-Features (Deliberately NOT Built)

The trusted-internal posture is the justification for each exclusion. Building these would add attack surface, complexity, or duplicate the TS API.

| Feature | Why Requested / Tempting | Why Problematic Here | Alternative |
|---------|--------------------------|----------------------|-------------|
| End-user authentication / API keys | Every public sandbox has it. | The TS API already authenticated the user. Re-doing it adds surface and a second source of truth. | Trust the private network boundary; the TS API is the only client. |
| Pusher/soketi channel authorization | Interactive channels usually need authz. | The TS API signs/authorizes the `private-run-<jobId>` channel; soketi is **output-only** toward the client — nothing trusted enters through it. | TS API owns channel auth. |
| Runtime package install in the sandbox (`pip install` at run time) | Users want arbitrary deps. | Needs network in the sandbox (breaks `network=none`), is slow, non-deterministic, and an exfiltration vector. | Pre-bake common libs into the image (numpy/pandas/requests for Python). Add a lib = rebuild the image. |
| Network access from sandboxed code | Some workloads want HTTP/DB. | Untrusted code + network = SSRF into the private network, exfiltration, abuse. | `network=none` always. `requests` is baked in but has nowhere to go — fine for teaching/eval. |
| Persistent filesystem / stateful sandboxes (pause/resume like e2b) | e2b offers it; enables long-lived workspaces. | We need ephemeral, single-execution sandboxes. Persistence = cleanup/leak risk and contradicts the per-execution hardening model. | Read-only rootfs + tmpfs, destroyed on terminal event. State lives in the caller. |
| Per-user quotas / billing / usage metering | SaaS staples. | Internal service; the TS API owns business logic. | Capacity is just concurrent-slot accounting; the TS API decides who runs. |
| Public-internet exposure / TLS termination / WAF | Needed if internet-facing. | It is never internet-facing; it sits behind the private network. | Bind to the private network only. |
| Arbitrary language config from the request (custom Docker flags, custom run command) | Flexibility. | Lets the caller weaken the sandbox or run arbitrary host commands. | Manifest is the only source of run/compile/limits. Request may override *limits* within bounds, never the run command or hardening. |
| Guaranteed-delivery stdin (Redis Streams) **for MVP** | Stronger reliability. | pub/sub is simpler and sufficient for MVP; Streams adds complexity before it's proven needed. | Redis pub/sub now; Redis Streams + `XREAD BLOCK` documented as an upgrade path. |
| Docker-in-Docker | Obvious way for a containerized worker to launch containers. | Privileged, fragile, security-poor. | Worker talks to the host runtime via a mounted socket (dev); runner behind an interface. |
| WebSocket fan-out from the executor itself | Could stream output directly. | Duplicates soketi and couples the executor to client connections. | Publish to soketi via Pusher HTTP API; soketi handles fan-out. |

---

## Language-Specific Feature Expectations

| Language | Mode | Entrypoint / Run | Interactive Notes | Baked-in Libs |
|----------|------|------------------|-------------------|---------------|
| **Python 3.12** | Interpreted | `python main.py` | `input()` blocks on stdin → needs streaming stdin + EOF. Idle clock catches a program blocked on `input()` with no client input. Unbuffered stdout (`-u` or `PYTHONUNBUFFERED=1`) is **essential** — otherwise streamed output is held in libc buffers and the interactive UX breaks. | numpy, pandas, requests |
| **Rust** | **Compiled** | `rustc -O main.rs -o main` → run `./main` | Compile stage can fail (terminal, surface compiler stderr). Compile has its own time/memory budget separate from run. `println!` is line-buffered to a pipe — generally fine; flush on interactive prompts. | std only (assume; confirm if crates wanted — would need vendored registry, likely out of scope) |
| **R 4.4** | Interpreted | `Rscript main.R` | Common libs baked in. `readLines("stdin")` / `scan()` for stdin. R buffers output → may need explicit `flush()` or line-buffered stdout for interactive feel. | common CRAN libs (confirm exact set when building image) |
| **SQLite 3** | Interpreted (shell) | `sqlite3` reading SQL from stdin against an ephemeral in-memory DB | **The canonical interactive case.** The `sqlite3` shell is a REPL: each line of stdin is a statement, output streams back. Supports both a `.sql` file (one-shot) and an interactive session. EOF (`stdin/close`) ends the session cleanly; without it the shell blocks to idle-timeout. `.mode`/`.headers` dot-commands affect output format. | n/a (sqlite3 binary) |

**Cross-cutting language gotcha (HIGH importance for interactive):** output buffering. Interpreters buffer stdout when not attached to a TTY (which our pipes are not). Without forcing line/unbuffered output, streamed `stdout` events arrive in clumps or only at exit, defeating the interactive model. This must be handled per-language in the manifest/run command (e.g. `python -u`, `stdbuf`, or a PTY). **Consider allocating a PTY** for the run process — e2b uses a PTY precisely for this reason; it gives line-buffered, TTY-like behavior for free across languages and makes the SQLite shell behave naturally. Trade-off: PTY merges stdout/stderr by default, complicating separate capture. Flag for architecture decision.

---

## Feature Dependencies

```
Live interactive stdin session
    └──requires──> Streaming output event model (stdout/stderr as they happen)
    └──requires──> Three independent clocks (idle clock only meaningful for live sessions)
    └──requires──> Start-handshake (else early prompts lost)
                       └──requires──> Stage events (queued/compiling/running)
                       └──requires──> Client subscribed to realtime channel before `start`
    └──requires──> stdin transport (Redis pub/sub stdin:<jobId> → owning worker → pipe)
    └──requires──> EOF / stdin-close semantics (clean termination)
    └──requires──> stdin backpressure + rate limit (flood protection)

Slot/capacity accounting
    └──requires──> Deterministic cleanup (terminal event frees slot)
    └──requires──> Live session model (slots only meaningful when sessions are long-lived)

Deterministic cleanup
    └──requires──> Single terminal `result` event (exactly-once)
    └──requires──> Sandbox hardening (the thing being torn down)

Compile-stage handling
    └──requires──> Manifest compile/run split
    └──enables──> Rust (compiled)

Manifest-driven packaging
    └──enables──> "drop a folder" extensibility (zero core changes)
    └──enables──> Per-language defaultLimits + interactive flag

Output buffering control (per-language / PTY)
    └──enhances──> Streaming output event model (without it, interactive UX breaks)

Start-handshake ──conflicts──> Auto-start on POST
    (cannot both auto-run and wait for explicit start; the whole point is to NOT auto-start)
```

### Dependency Notes

- **Interactive session requires the start-handshake:** without an explicit `start` gated on client subscription, a program that prints immediately loses its first output. This forces a `queued` stage and a two-step API (`/execute` then `/start`).
- **Idle clock only exists because of the interactive model:** a batch engine has no use for it. It's the clock that kills a session blocked waiting for stdin that never comes.
- **Slot accounting depends on deterministic cleanup:** if cleanup leaks, the slot count drifts upward and capacity silently shrinks — the single worst failure mode of a long-lived-session system.
- **Streaming output depends on buffering control:** the feature is technically present but *useless* if interpreters buffer output to exit. This dependency is easy to miss and a likely source of "it works in batch but the interactive demo shows nothing until the end" bugs.
- **Compile handling enables Rust specifically:** the only compiled language in the initial four; validates the compile/run split before more compiled langs are added.

---

## MVP Definition

### Launch With (v1) — Python end-to-end first, then fan out

Per PROJECT.md build order: prove the hard parts on Python, then reuse for Rust/R/SQLite.

- [ ] Batch + interactive unified execution model (one-shot is the no-stdin case) — core contract
- [ ] Start-handshake (`/execute` → `queued` → client subscribes → `/start`) — correctness, no lost prompts
- [ ] Three clocks (wall, idle, CPU) each killing independently — the interactive safety model
- [ ] Streaming output events (`stage`, `stdout`, `stderr`, `result`) via soketi — interactive UX
- [ ] stdin transport (Redis pub/sub) + EOF/close + kill — interactive control surface
- [ ] Output byte cap + truncation flag; stdin backpressure (429) + rate limit — abuse bounds
- [ ] Full sandbox hardening per execution (network=none, ro+tmpfs, no-swap mem cap, pids, cpus, cap-drop, no-new-privs, seccomp) — untrusted-code safety
- [ ] Deterministic cleanup + slot accounting — no leaks
- [ ] Manifest-driven packaging with `compile`/`run`/`defaultLimits`/`interactive` — extensibility invariant
- [ ] Per-language output buffering control (`python -u` / PTY decision) — makes streaming actually work
- [ ] Four language packages: Python 3.12, Rust (compiled), R 4.4, SQLite 3
- [ ] Abuse test suite (fork bomb, OOM, infinite loop, stdin-blocked idle, EOF, giant output)

### Add After Validation (v1.x)

- [ ] Redis Streams + `XREAD BLOCK` for guaranteed stdin delivery — trigger: pub/sub message loss observed under load
- [ ] gVisor runner via the runner interface — trigger: stronger isolation required by a workload
- [ ] Per-request limit overrides (within manifest-defined bounds) — trigger: callers need shorter/longer budgets per job
- [ ] Additional language packages — trigger: demand; proves the "drop a folder" promise

### Future Consideration (v2+)

- [ ] Crate/CRAN vendoring for richer Rust/R (offline registry) — defer: needs a vendoring story without violating network=none
- [ ] PTY-based universal interactive mode if per-language buffering proves too fiddly — defer: architectural change
- [ ] Multi-process / background-job sandboxes — defer: contradicts single-execution ephemeral model

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| Unified batch+interactive execution | HIGH | HIGH | P1 |
| Start-handshake (no lost prompts) | HIGH | MEDIUM | P1 |
| Three clocks (wall/idle/CPU) | HIGH | HIGH | P1 |
| Streaming output events via soketi | HIGH | MEDIUM | P1 |
| stdin transport + EOF + kill | HIGH | HIGH | P1 |
| Sandbox hardening | HIGH | MEDIUM | P1 |
| Deterministic cleanup + slot accounting | HIGH | HIGH | P1 |
| Output cap / stdin backpressure | HIGH | MEDIUM | P1 |
| Manifest-driven packaging | HIGH | MEDIUM | P1 |
| Output buffering control (per-lang/PTY) | HIGH | MEDIUM | P1 |
| Compile-stage handling (Rust) | MEDIUM | MEDIUM | P1 |
| Multi-file submissions | MEDIUM | LOW | P1 |
| Redis Streams guaranteed delivery | MEDIUM | MEDIUM | P2 |
| gVisor runner | MEDIUM | MEDIUM | P2 |
| Per-request limit overrides | MEDIUM | LOW | P2 |
| Crate/CRAN vendoring | LOW | HIGH | P3 |

## Competitor Feature Analysis

| Feature | Piston | Judge0 | e2b | Our Approach |
|---------|--------|--------|-----|--------------|
| Batch execution | Yes (`/execute`) | Yes (core) | Yes | Yes (no-stdin case of interactive) |
| Interactive stdin streaming | Experimental WS `/connect` | No | Yes (PTY) | **Core** — Redis stdin + soketi out |
| Start-handshake (no lost early output) | No | n/a | No | **Yes** — `queued`→subscribe→`start` |
| Idle clock | No | No | Yes (idle timeout) | **Yes** — one of three clocks |
| CPU clock (separate from wall) | Partial (timeouts) | Yes (`cpu_time_limit`) | Limited | **Yes** — cgroup CPU |
| Manifest/package extensibility | Yes (`run`/`compile` scripts) | Limited | Image-based | **Yes** — manifest + Dockerfile, zero core changes |
| Compile/run split | Yes | Yes | n/a | Yes (nullable `compile`) |
| Multi-file | Yes | Yes | Yes (filesystem) | Yes |
| network=none default | Yes (Isolate) | Yes | No (network on) | **Yes, always** |
| Persistent/pausable sandbox | No | No | Yes | **No** (anti-feature; ephemeral) |
| Auth / quotas / billing | Some | Yes (RapidAPI) | Yes | **No** (anti-feature; TS API owns it) |

## Sources

- Piston API v2 docs (manifest fields: language/version/aliases/files/stdin/args/compile_timeout/run_timeout/memory limits; Isolate sandboxing; WebSocket `/connect` for interactive I/O) — https://piston.readthedocs.io/en/latest/api-v2/ , https://github.com/engineer-man/piston/blob/master/docs/api-v2.md (HIGH for batch fields; MEDIUM for interactive WS — corroborated by search, full protocol from training data)
- Judge0 (resource limit field set: cpu_time_limit, cpu_extra_time, wall_time_limit, memory_limit, stack_limit, max_file_size; multi-file; stdin; status/time/memory result; **no streaming**) — https://ce.judge0.com/ , https://github.com/judge0/judge0 (HIGH)
- e2b PTY (interactive bidirectional terminal, streaming callbacks, idle timeout, pause/resume/kill lifecycle, Firecracker isolation) — https://e2b.dev/docs/sandbox/pty (HIGH)
- PROJECT.md (project-specific contract: event model, three clocks, manifest schema, anti-features, language set) — authoritative for this service (HIGH)

---
*Feature research for: internal Piston-style remote code execution engine with live interactive stdin sessions*
*Researched: 2026-06-02*
