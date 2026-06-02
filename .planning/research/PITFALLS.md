# Pitfalls Research

**Domain:** Sandboxed remote code-execution engine (Piston-style, Go) running untrusted code with interactive stdin, Redis-backed queue + stdin routing, soketi output, stateless workers
**Researched:** 2026-06-02
**Confidence:** HIGH (hardening flags, cgroup v1/v2 OOM scope, gVisor ENOSYS behavior, soketi/Pusher 10KB limit verified against official docs; interactive/leak/scaling pitfalls from domain experience + Piston/isolate references)

> Phase names below reference the PROJECT.md build order: **P1 Foundation** (folder/manifest schema, core skeleton), **P2 Sandbox Hardening** (runner interface + Docker hardening + three clocks), **P3 Interactive Python E2E** (queue, stdin pub/sub, start-handshake, soketi output, Python package), **P4 Lifecycle/Cleanup hardening**, **P5 Scale/Statelessness**, **P6 Language fan-out** (Rust/R/SQLite + compile-step), **P7 Abuse test suite + docs**. Adjust to the actual roadmap; the *ordering relationships* are what matter.

---

## Critical Pitfalls

### Pitfall 1: Interactive mode lets heavy compute slip past the wall-clock — the CPU clock is the real limit

**What goes wrong:**
You implement a single wall-clock timeout (e.g. 10s) thinking it bounds compute. In an interactive session the process legitimately *idles* waiting for stdin, so wall-clock is generous or reset on activity. Malicious code spins a tight CPU loop, periodically reads one byte of stdin to look "interactive," and burns a full core indefinitely while never tripping wall-clock. Wall-clock measures elapsed real time; it does NOT measure CPU consumed. A 100ms wall-clock budget can still hide unbounded CPU if the process is mostly "waiting."

**Why it happens:**
Devs conflate "the session has been alive for N seconds" with "the code has computed for N seconds." For batch execution they're nearly the same; for interactive sessions they diverge completely. The idle timer makes it worse — resetting idle on stdin activity also tends to reset the only guard people built.

**How to avoid:**
Three *independent* clocks, none derived from the others:
- **Wall-clock**: hard ceiling on total session lifetime (kills even a well-behaved idle session eventually).
- **Idle**: time since last *meaningful* activity (stdin received OR output produced); fires when a session is parked doing nothing.
- **CPU clock**: read cgroup CPU usage (`cpu.stat` → `usage_usec` on v2; `cpuacct.usage` on v1) and kill when cumulative CPU exceeds `cpuMs`. This is the only clock that catches compute hidden behind interactivity. Poll it on a ticker (e.g. every 100–250ms) and compare cumulative usage; do NOT try to infer CPU from wall time.
All three kill the *whole sandbox*, not just the entrypoint PID (see Pitfall 5).

**Warning signs:**
A fork-bomb/infinite-loop abuse test passes under batch mode but a "read one byte, then loop" variant runs forever. Worker CPU pegged at 100% with sessions that never expire. Idle timer configured but CPU clock absent from the runner interface.

**Phase to address:** P2 (three clocks built into the runner from day one), verified in P7 abuse suite.

---

### Pitfall 2: Wall-clock/timeout kills the entrypoint PID, not the process tree — orphans keep running

**What goes wrong:**
On timeout you `kill` the PID you launched (the shell/entrypoint), but the untrusted code already `fork()`ed children or spawned a subprocess (e.g. compiler → linker → binary, or a Python `subprocess`). The parent dies; children reparent to PID 1 inside the container and keep consuming CPU/memory. You report "killed" but the sandbox is still hot. Worse, if you don't tear down the *container*, the slot is freed in your bookkeeping while real work continues.

**Why it happens:**
`exec.Cmd.Process.Kill()` in Go signals only the direct child. PID namespaces and process groups are not handled automatically.

**How to avoid:**
Never rely on killing a PID. The kill primitive must be **destroy the container** (`docker rm -f` / `docker kill`), which the PID-namespace guarantees takes the whole tree with it. If you ever run processes directly (not recommended here), use a dedicated process group (`Setpgid`) and signal the negative PGID, plus a PID namespace. Treat "stop the sandbox" as a single atomic operation that all three clocks, the result handler, and the explicit `/kill` endpoint funnel into.

**Warning signs:**
CPU stays high after a "timeout killed" log line. `docker ps` shows containers that your slot accounting thinks are gone. Zombie processes accumulating on workers.

**Phase to address:** P2 (kill = destroy container). Reinforced in P4 (single terminal-path cleanup) and P6 (compile spawns a child process — high risk).

---

### Pitfall 3: Container hardening gaps — the escape vectors

**What goes wrong:**
Untrusted code breaks out of the sandbox or attacks the host because one flag was forgotten. Common gaps, each individually sufficient for escape or privilege escalation:
- Missing `--security-opt=no-new-privileges` → setuid binaries / file capabilities inside the image can re-gain privilege; defeats much of `cap-drop`.
- Incomplete `--cap-drop=ALL` (dropping "some" caps, or leaving `CAP_SYS_ADMIN`, `CAP_NET_RAW`, `CAP_DAC_OVERRIDE`, `CAP_SYS_PTRACE`) → escape / host interaction.
- Default/permissive seccomp (or `--security-opt seccomp=unconfined`) → dangerous syscalls (`keyctl`, `ptrace`, `mount`, `clone` with namespace flags, `bpf`, `userfaultfd`) reachable. Docker's *default* seccomp is decent but still allows more than untrusted code needs; you want a *restrictive* allowlist.
- `--network=none` omitted → SSRF into the private network, data exfil, or reaching Redis/soketi/the host metadata endpoint.
- Not `--read-only` (or writable image layers) → tamper, persistence, fill disk.
- Running as root inside the container (no `--user` / non-root image user) → much larger blast radius if a kernel bug is hit.

**Why it happens:**
Hardening is a long checklist; flags get added incrementally and "it works" before all of them are on. Defaults are permissive-by-design for general workloads, not for untrusted code. No-new-privileges and seccomp interactions are subtle.

**How to avoid:**
Codify the *full* flag set in one place in the runner (not scattered), apply it to **every** execution unconditionally, and assert it in a test. Baseline per execution:
`--network=none --read-only --tmpfs /tmp:rw,noexec,nosuid,size=<cap> --memory=<m> --memory-swap=<m> --pids-limit=<n> --cpus=<c> --cap-drop=ALL --security-opt=no-new-privileges --security-opt seccomp=<restrictive.json> --user=<non-root>`.
Build a restrictive seccomp allowlist from the syscalls your runtimes actually need (start from Docker default, remove `ptrace`, `mount`, `keyctl`, `bpf`, `clone3`/namespace clone flags, `userfaultfd`, `unshare`, `setns`). NEVER mount the Docker socket *into the sandbox* (see Pitfall 4). Keep the runner behind an interface so gVisor can wrap this later (Pitfall 11).

**Warning signs:**
Any code path that constructs run flags conditionally. A seccomp profile of `unconfined` or "default." `id` inside the sandbox prints `uid=0(root)`. A test that runs untrusted code can reach Redis on the private network.

**Phase to address:** P2 — this is the core of the hardening phase. Re-audited in P6 when new language images are added (a new base image may run as root or need a syscall you must consciously allow).

---

### Pitfall 4: Mounting the Docker socket — worker compromise = host root

**What goes wrong:**
Workers talk to the host runtime via a mounted `/var/run/docker.sock` (per PROJECT.md, this is the chosen dev model). The socket is **root-equivalent**: anyone who can reach it can launch a privileged container, bind-mount `/`, and own the host. The fatal mistake is letting the *sandbox* (untrusted code) reach that socket — e.g. mounting it into the sandbox, or not using `--network=none`, or the worker proxying socket access. A secondary mistake is treating the *worker* process itself as low-trust while it holds socket access.

**Why it happens:**
The worker legitimately needs the socket to spawn sandboxes; it's easy to be sloppy about the boundary between "worker uses socket" and "sandbox can never touch socket." Docker-in-Docker tutorials normalize socket mounting.

**How to avoid:**
- The socket is mounted ONLY into the **worker**, never into a sandbox container. Sandboxes get `--network=none` and no socket bind.
- Treat the worker as a trusted component on the private network; the trust boundary for untrusted input is the TS API, and the sandbox is the untrusted thing — keep them apart.
- This is *why DinD is avoided*: DinD either needs `--privileged` (huge attack surface) or the socket mount (host-root). Talking to the host runtime from a trusted worker is strictly safer than nesting Docker.
- Consider a rootless/socket-proxy (e.g. restricting the socket API surface) as a hardening upgrade, behind the runner interface.

**Warning signs:**
`docker.sock` appearing in any sandbox `--volume`/`--mount`. A sandbox that can resolve or connect to anything on the network. `--privileged` anywhere.

**Phase to address:** P2 (runner spawn model + socket scoping). Reconfirmed in P5 when scaling workers (each worker still scopes its own socket; never share into sandboxes).

---

### Pitfall 5: Resource-limit evasion — fork bombs, OOM scope, tmpfs fill, PID exhaustion across sandboxes

**What goes wrong:**
Several distinct evasions, each a separate failure:
- **Fork bomb** with no `--pids-limit` → exhausts host PIDs, wedges the worker and its neighbors.
- **OOM scope confusion (cgroup v1 vs v2):** On cgroup **v2**, hitting `memory.max` SIGKILLs the *entire cgroup* (good — the whole sandbox dies). On cgroup **v1**, only the *offending process* is killed, so a multi-process sandbox can lose one child and keep running, or thrash. If you tuned behavior on a v2 host and deploy on v1 (or vice-versa), OOM semantics silently change. (Verified: docker docs / OOM guides.)
- **Swap masks the limit:** if `--memory != --memory-swap`, the container can swap, turning a hard memory cap into soft thrashing — unpredictable latency and no clean OOM. Set them equal to disable swap.
- **tmpfs fill:** `/tmp` on tmpfs with no `size=` cap lets code write until host RAM is gone (tmpfs is RAM-backed). An uncapped tmpfs defeats your memory limit from a different angle.
- **PID exhaustion across concurrent sandboxes:** per-sandbox `--pids-limit` is necessary but not sufficient — N sandboxes × per-sandbox limit can still exhaust the host's global PID space. You need a concurrency cap (slots) sized against host PID/memory headroom.
- **Output flooding:** code prints gigabytes; without output byte caps you OOM the worker (buffering), saturate Redis/soketi, and blow the 10KB event limit (Pitfall 8).

**Why it happens:**
Each limit guards one resource; devs add the obvious ones (memory, CPU) and miss tmpfs, PID-across-sandboxes, output, and the cgroup-version OOM semantics. macOS dev (cgroup v2 in the Linux VM) vs a Linux prod host on v1 hides the divergence until prod.

**How to avoid:**
- Always `--pids-limit` AND a global concurrency/slot cap; size slots so `slots × (pids, memory, tmpfs)` fits host headroom with margin.
- `--memory == --memory-swap` (no swap), every execution.
- `--tmpfs /tmp:size=<cap>,noexec,nosuid` — always size-capped; the cap counts against your memory budget.
- **Standardize on cgroup v2 in prod** and document it; assert the host is v2 at worker startup, or explicitly handle v1 OOM semantics. Don't let dev (v2 in Docker Desktop VM) and prod diverge silently.
- Output byte caps with truncation (`truncated=true`) enforced in the worker as it reads pipes — count bytes, stop forwarding past the cap, keep draining the pipe so the process doesn't block (Pitfall 6).

**Warning signs:**
Abuse suite missing a tmpfs-fill or PID-across-sandboxes test. No assertion of cgroup version. `--memory-swap` unset. Worker memory climbing with output volume. OOM behaves differently on CI (Linux) vs laptop.

**Phase to address:** P2 (per-sandbox limits + cgroup v2 assertion), P5 (global slot cap vs host headroom), P7 (abuse tests: fork bomb, OOM, tmpfs fill, output flood).

---

### Pitfall 6: Interactive stdin — deadlocks, backpressure, lost early prompts, missing EOF

**What goes wrong:**
The interactive session is the hard part and has several independent traps:
- **Lost early prompts:** the process prints a prompt and starts reading stdin *before* the client has subscribed to the soketi channel. The "first prompt" is published into the void. Without a start-handshake the session looks broken (client sees no prompt, sends no input, idle-times out).
- **stdin pipe deadlock / head-of-line blocking:** the process fills its stdout pipe buffer (64KB) and blocks on write; meanwhile the worker is blocked trying to write the next stdin chunk into the stdin pipe (also full because the process isn't reading). Classic two-pipe deadlock. Or the worker's single goroutine does read-output-then-write-stdin serially and stalls.
- **Never receiving EOF:** an interactive REPL (SQLite shell, Python `input()` loop) only terminates when stdin closes. If `/stdin/close` doesn't actually close the underlying pipe (just stops forwarding), the process blocks on `read()` forever → only the idle/wall clock saves you, and you report a timeout instead of a clean exit.
- **Partial writes:** writing a stdin chunk to the pipe may write fewer bytes than requested; not looping → truncated/corrupted input, especially for large pastes.
- **Pending-stdin unboundedness:** client floods stdin faster than the process consumes it; without a pending-byte cap you buffer unbounded (OOM) — this is why PROJECT.md specifies a pending-stdin byte cap (→429) and stdin rate limit.

**Why it happens:**
Pipe backpressure is invisible until buffers fill; small test inputs never trigger it. EOF semantics for REPLs are easy to get subtly wrong (forwarding stops != FD closed). The subscribe-vs-start race only shows up under real network timing.

**How to avoid:**
- **Start-handshake:** job is *queued*, client subscribes to `private-run-<jobId>`, then the API calls `/run/:jobId/start` which actually launches the process. The process never produces output before a subscriber exists.
- Dedicate **separate goroutines** for stdout, stderr, and stdin so none blocks the others; always keep draining stdout/stderr even after output caps are hit (forward nothing, but read-and-discard so the process never blocks).
- `/stdin/close` must close the *write end of the stdin FD* (real EOF), not just stop forwarding. Test that an `input()`/REPL exits cleanly on close.
- Loop on `Write` for partial writes (Go's `io.Copy`/`WriteString` handle this; raw `Write` does not guarantee full write).
- Enforce pending-stdin byte cap (429 from API) and stdin rate limit at the API layer; the worker applies its own bound too.

**Warning signs:**
A program that prints a prompt shows nothing to the client. Large output + large stdin hangs. SQLite/Python REPL never exits on close and always idle-times-out. Big pasted input arrives garbled. Worker memory grows with un-consumed stdin.

**Phase to address:** P3 (start-handshake + stdin pub/sub + Python REPL E2E is the canonical interactive test). EOF/close + partial-write correctness verified in P3, re-tested for SQLite shell in P6.

---

### Pitfall 7: Redis pub/sub loses stdin — no delivery guarantee, dead-worker routing

**What goes wrong:**
stdin is routed via Redis **pub/sub** (`stdin:<jobId>`). Pub/sub is fire-and-forget: if no subscriber is connected at publish time, or the subscriber's connection blips, the message is **dropped silently**. Failure modes:
- Worker subscribes to `stdin:<jobId>` slightly after the API publishes the first chunk → first input lost.
- Worker dies mid-session; the API keeps publishing stdin to a channel nobody listens to → input vanishes, session hangs until idle-timeout, client never knows.
- Redis reconnect (failover, network blip) drops the subscription; messages during the gap are gone.
- "Job claimed but never started": a worker `BRPOP`s the job, then crashes before launching → job is gone from the queue but no sandbox exists; client subscribed and waiting forever.

**Why it happens:**
Pub/sub is the simplest thing and works in dev where everything is up. The "no delivery guarantee" property is documented but easy to ignore until a worker restarts under load.

**How to avoid:**
- For MVP (per PROJECT.md): make the *ordering* safe — only start the process after the worker has subscribed (the start-handshake covers the *first-prompt* race; apply the same discipline to stdin so the worker subscribes to `stdin:<jobId>` before signalling ready/started).
- Detect dead sessions: if the owning worker dies, the session must be torn down and the client told (a `result`/`error` event), not left hanging. Use a worker heartbeat / job ownership key with TTL so an orphaned job is reaped.
- "Claimed but never started": use a reliable-queue pattern (`BRPOPLPUSH`/`LMOVE` into a processing list, or a Redis Stream consumer group with explicit ack) so a crashed worker's job is reclaimed instead of lost.
- **Document the upgrade path:** Redis **Streams** + `XREAD BLOCK` + consumer groups give at-least-once delivery, replay from last-read ID, and survive reconnects — the right answer when stdin loss is unacceptable. Keep the stdin transport behind an interface so pub/sub → Streams is a swap.

**Warning signs:**
Sessions hang after a worker restart. First keystroke occasionally ignored. No mechanism to notice a worker died mid-session. Jobs disappearing under chaos testing.

**Phase to address:** P3 (subscribe-before-start ordering), P5 (dead-worker detection, reliable claim, heartbeat/TTL reaping). Streams upgrade flagged as a known follow-up.

---

### Pitfall 8: soketi/Pusher pitfalls — 10KB event cap, ordering, publish-after-unsubscribe

**What goes wrong:**
- **10KB payload limit:** Pusher/soketi reject (or in some clients silently drop) events whose data exceeds ~10KB. A single `stdout` event carrying a big chunk of program output is rejected → output silently lost. (Verified: Pusher docs and soketi limits docs.)
- **Ordering / interleaving:** publishing `stdout` and `stderr` as separate events via separate HTTP triggers gives no guaranteed cross-event ordering; rapid output can arrive interleaved or out of order at the client.
- **Publishing after unsubscribe / before subscribe:** events triggered when no client is subscribed are simply not delivered (no buffering) — the early-prompt problem again, and also a late-result problem if the client already navigated away.
- **Backpressure to soketi:** flooding soketi with thousands of tiny events (one per line) overruns it / hits its rate limits; soketi is not a high-throughput log pipe.
- **Channel auth assumptions:** `private-` channels require auth; the TS API owns auth (per scope). If the worker publishes to a channel the client was never authorized for, the client never sees it. Don't assume the worker can mint subscriptions.

**Why it happens:**
The 10KB limit isn't hit in small demos. Pusher's "small messages, fast" design is mismatched with streaming program output. Event ordering is assumed because in-order is the common case at low volume.

**How to avoid:**
- **Chunk + cap output events** below 10KB *before* publishing; combine with the global output byte cap + truncation. Coalesce many small lines into batched events on a short flush interval (e.g. every 50–100ms or 8KB, whichever first) to bound event rate AND stay under 10KB.
- Carry a monotonic **sequence number** per session in each event so the client can reorder/detect gaps; or use a single `output` event type with a `stream: stdout|stderr` field to keep one ordered stream.
- Rely on the start-handshake so nothing is published before subscribe; for terminal `result`, accept that a departed client won't get it (the TS API can also fetch final result via the API contract if needed).
- Treat soketi as output-only and rate-limit your publish rate; never trust anything inbound from it (per PROJECT.md trust boundary).

**Warning signs:**
Large outputs truncated or missing on the client but present in worker logs. stderr appearing before the stdout that caused it. soketi CPU spiking / dropping events under chatty programs. Events published to channels clients can't subscribe to.

**Phase to address:** P3 (output publisher: batching, <10KB chunking, sequence numbers, handshake). Load/ordering verified in P7.

---

### Pitfall 9: Leak & cleanup failures — orphaned containers, leaked subscriptions, slots never freed

**What goes wrong:**
Cleanup is done on the *happy path* (normal result) but not on every terminal path. Each missed path leaks something:
- **Orphaned containers:** worker crashes / panics between launch and cleanup → `docker rm -f` never runs → containers accumulate, consuming PIDs/memory/disk until the host degrades.
- **Leaked Redis subscriptions:** the `stdin:<jobId>` subscription isn't unsubscribed on timeout/kill → goroutine + connection leak; over time exhausts Redis client connections.
- **Slots never freed:** a slot is reserved on start but only released on normal completion; a kill/timeout/panic path forgets to release → effective capacity bleeds to zero (workers report "full" while idle).
- **Zombie processes / dangling tmpfs:** if you ever run processes directly, unreaped children become zombies; tmpfs mounts not torn down with the container leak RAM.
- **Double-free / use-after-free of a slot:** two terminal events (e.g. CPU clock fires *and* process exits) both run cleanup → slot freed twice, or stdin published to a destroyed session.

**Why it happens:**
There are many terminal paths (normal result, wall-clock, idle, CPU clock, explicit `/kill`, process crash, worker panic, OOM kill) and cleanup is written per-path instead of funneled. Crash-safety (worker dies *outside* its own control flow) is the hardest and most-skipped case.

**How to avoid:**
- **Single idempotent teardown function** that EVERY terminal path calls exactly once (guard with `sync.Once` per session): destroy container, unsubscribe stdin, close pipes, free slot, publish terminal event. Idempotency makes the double-fire case (Pitfall 1's CPU-clock-and-exit race) safe.
- **Crash safety via labels + a reaper:** label every sandbox container with the worker ID and job ID; a startup/periodic reaper removes any container labeled with a *dead* worker (worker heartbeat key in Redis with TTL; reap containers whose worker key expired). This recovers orphans from `kill -9`/OOM of the worker itself, which no in-process `defer` can handle.
- **Slot accounting in Redis with TTL**, not just in-memory, so a dead worker's slots expire and are reclaimed.
- Use the container as the unit of cleanup so there are no separate zombie processes or dangling tmpfs to track (they die with `docker rm -f`).

**Warning signs:**
`docker ps -a` grows over time. Redis `CLIENT LIST` / subscription count climbs. Workers report full capacity but low CPU. Capacity that never recovers after an abuse/chaos test. Cleanup logic duplicated across handlers.

**Phase to address:** P4 — dedicated lifecycle/cleanup hardening phase (single teardown, idempotency). Crash-recovery reaper + TTL slot accounting in P5 (statelessness/scale). Verified in P7 (kill the worker mid-session, assert no orphans/leaked slots).

---

### Pitfall 10: Stateless-scaling failures — worker death, stdin to dead worker, thundering herd, queue poisoning

**What goes wrong:**
"Stateless workers" is the design, but an interactive session is inherently *stateful* (one worker owns the live sandbox for the session's life). Pitfalls:
- **stdin routed to a dead worker:** the API publishes `stdin:<jobId>` but the worker that owned the sandbox died; no other worker can pick up a *live* sandbox (it's gone with the worker). Without detection the client hangs.
- **Job claimed but never started** (also Pitfall 7): crash between claim and launch loses the job.
- **Queue poisoning:** a job that always crashes the worker (e.g. a payload that triggers a runtime bug) gets reclaimed by the reliable-queue pattern and crashes the *next* worker, and the next — cascading outage. Needs a max-retry / dead-letter.
- **Thundering herd:** all N workers `BRPOP` the same queue; a burst, or a Redis reconnect storm after failover, makes them wake/contend simultaneously; or many sessions expire at once and all hit cleanup/soketi together.
- **Capacity = concurrent live sandboxes, not requests:** scaling decisions based on request rate are wrong; a few long-lived interactive sessions saturate slots while request rate looks low.

**Why it happens:**
Statelessness is assumed to mean "any worker handles anything," which is false for a live session. Crash/poison/herd cases only appear under load + failure injection, which dev rarely does.

**How to avoid:**
- Accept that a session is pinned to its worker for its lifetime; make worker death a *terminal event* for its sessions (reaper tears down, client gets an `error`/`result`). Don't try to migrate a live sandbox.
- Reliable claim with **max-retry / dead-letter** so a poison job is quarantined after K crashes instead of cycling.
- Bound concurrency with the **slot** model and reject/queue beyond capacity (the 429/backpressure path); scale on *slot utilization*, not request rate.
- Smooth herds: jitter on reconnect/poll, and cap simultaneous soketi publishes.

**Warning signs:**
Cascading worker restarts after one bad payload. Sessions that hang exactly when a worker is recycled (deploys!). Autoscaler reacting to request rate while slots are saturated. Redis CPU spikes after failover.

**Phase to address:** P5 — statelessness/scale phase (dead-worker terminal handling, reliable claim + dead-letter, slot-based capacity). Poison-job test in P7.

---

### Pitfall 11: gVisor caveats — the runtime swap isn't free

**What goes wrong:**
The runner is behind an interface for a future Docker-hardened → gVisor swap. Teams assume gVisor is a drop-in security upgrade and hit:
- **Unimplemented syscalls return ENOSYS:** gVisor implements a *subset* (~237 of ~350 syscalls). A runtime/library that uses something gVisor doesn't support fails at runtime, not at launch. R, scientific Python (numpy/pandas), or Rust toolchains may hit an unsupported path. (Verified: gVisor docs / syscall list.)
- **Syscall-heavy workloads slow down; CPU-bound do not:** gVisor adds overhead on syscall-intensive code (lots of I/O, process spawning) but ~0 on pure CPU. Compile steps (fork/exec heavy) and file-churn workloads are exactly the slow case. (Verified: gVisor performance guide — CPU-bound has no penalty.)
- **GPU / some features unsupported.**
- **Different OOM/limit interaction:** gVisor's Sentry mediates memory; your cgroup assumptions may shift.

**Why it happens:**
gVisor is marketed as the secure runtime; the compatibility/perf caveats are in the fine print and only surface when you actually run your real language images under it.

**How to avoid:**
- Keep the runner interface clean so swapping runtime touches no core logic — but **per-language compatibility test** each image under gVisor before trusting it; don't assume Python/Rust/R all "just work."
- Budget extra time on the *compile* and syscall-heavy languages.
- Keep Docker-hardened as the supported default; treat gVisor as an *additional* layer/option, validated per language, not a blanket replacement.

**Warning signs:**
A language that works under runc fails with ENOSYS under runsc. Compile times balloon under gVisor. "We'll just switch to gVisor for security" with no per-language test plan.

**Phase to address:** Not MVP. Flag for a later "runtime hardening upgrade" phase; the *interface* that enables it is built in P2.

---

### Pitfall 12: macOS-dev vs Linux-prod — the Docker Desktop VM hides cgroup & socket realities

**What goes wrong:**
`docker compose up` on the dev's Mac runs Docker inside a **Linux VM** (Docker Desktop). This masks prod realities:
- **cgroup version:** the VM is cgroup **v2**; a prod Linux host might be **v1** (or vice versa) → OOM scope (Pitfall 5) and CPU-accounting paths (`cpu.stat` vs `cpuacct.usage`) differ. CPU-clock code that reads v2 files breaks on v1.
- **Socket semantics / paths:** `/var/run/docker.sock` behaves slightly differently across the VM boundary; `--pids-limit`, seccomp, and `--cpus` may be enforced by the VM kernel that differs from prod's kernel/version.
- **Performance & filesystem:** tmpfs sizing, bind-mount perf, and timing are all VM-mediated, so timing-sensitive tests (clocks, idle) behave differently than prod.
- **Resource ceilings:** the VM has its own memory/CPU cap; "host headroom" math for slots is meaningless on the laptop.

**Why it happens:**
Everything works in `docker compose` on the Mac, so the team assumes prod parity. The VM is invisible.

**How to avoid:**
- Make the CPU-clock and OOM code **explicitly cgroup-version-aware** (detect v1 vs v2 at runtime, read the right files) and test on a real Linux v2 host in CI.
- Pin/assert cgroup v2 in prod and document the requirement.
- Run the abuse suite and clock tests in CI on Linux, not only on the dev Mac.
- Don't compute slot capacity from laptop resources.

**Warning signs:**
CPU clock works on the Mac, throws "file not found" on a Linux host. OOM behaves differently in CI. Timing/idle tests flaky between laptop and CI.

**Phase to address:** P2 (cgroup-version-aware clock/OOM code), P7 (CI runs abuse suite on real Linux). The compose dev stack itself is P1/P3.

---

### Pitfall 13: Compile-language pitfalls — compile vs run limits conflated, compiler is untrusted-adjacent

**What goes wrong:**
Compiled languages (Rust) add a `compile` step before `run`. Mistakes:
- **One timeout for both phases:** a `rustc -O` compile legitimately takes many seconds; if the wall/CPU budget is shared with run, either compiles fail spuriously or you grant so much budget that *runtime* compute is effectively unbounded. The PROJECT manifest has `compile` (nullable) precisely so it can have its *own* limits.
- **Compile spawns child processes:** `rustc` invokes the linker (`cc`/`ld`) — a process tree. Timeout-kills the entrypoint and orphans the linker (Pitfall 2). Compile is *also* untrusted: a malicious program can be a compile-bomb (huge generated types, proc-macros, deep recursion in const eval) that hangs or OOMs the compiler. The compile step needs the same hardening + limits as run.
- **Compiler needs more memory than the program:** memory cap tuned for `run` starves `compile`.
- **Network during compile:** if not `--network=none`, the build could fetch dependencies (defeats the pre-baked-image model and opens exfil). Pre-bake everything; compile offline.
- **Interactive applied to compile:** the compile phase is batch; only `run` is interactive. Don't keep stdin pipes open during compile.

**Why it happens:**
The interactive/run model is built first (Python, no compile). Compile is bolted on for Rust later and reuses run's single-phase assumptions.

**How to avoid:**
- Distinct limits per phase: `compile` gets its own `wallTimeMs`/`cpuMs`/`memoryMb`; `run` gets its own. Enforce both as separate hardened, network-none, container-killed-as-tree executions.
- Treat compile as untrusted: full hardening + its own three-clock-style budget (at least wall + CPU + memory) and tree-kill.
- Keep `--network=none` for compile; rely on pre-baked toolchain/deps.
- Compile is non-interactive: no open stdin/handshake during compile; switch to interactive only for the run phase.

**Warning signs:**
Rust submissions failing with timeout on legit programs. Compile-bombs hanging a worker. Linker processes orphaned after a compile timeout. Compile budget reused as run budget.

**Phase to address:** P6 (language fan-out introduces Rust + the compile step). The *manifest shape* (`compile` nullable, per-phase limits) is locked in P1 so P6 doesn't require a core change.

---

## Technical Debt Patterns

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Redis **pub/sub** for stdin instead of Streams | Trivial, no consumer-group bookkeeping | Silent stdin loss on reconnect/dead worker; no replay | MVP only, behind an interface, with dead-worker detection; upgrade to Streams when loss matters |
| In-memory slot accounting only | Simple | Slots leak on worker crash; capacity bleeds to zero | Never for prod — back it with Redis + TTL by P5 |
| Per-terminal-path cleanup (no single teardown) | Fast to write the first handler | Guaranteed leaks as terminal paths multiply | Never — funnel to one idempotent teardown from the start |
| Single timeout (wall only) | One timer to build | Interactive compute escapes; tree orphans survive | Never for untrusted interactive code — three clocks from P2 |
| Tuning OOM/CPU on Docker Desktop only | No Linux box needed | v1/v2 divergence explodes in prod | Never — make clocks cgroup-version-aware and CI on Linux |
| Default Docker seccomp ("works") | No profile to author | Larger syscall surface than untrusted code needs | Briefly during P2 bring-up; ship a restrictive allowlist before P7 |
| No reaper (rely on `defer` cleanup) | Less code | `kill -9`/OOM of the worker orphans containers forever | Never for prod — label + reaper by P5 |

## Integration Gotchas

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| Docker socket | Mounting `docker.sock` into the sandbox or via DinD/`--privileged` | Socket only in the trusted worker; sandboxes get `--network=none`, no socket |
| Redis pub/sub | Assuming delivery; publishing before subscriber exists | Subscribe-before-start; detect dead worker; plan Streams upgrade for at-least-once |
| Redis queue | `BRPOP` (job lost if worker dies after pop) | `LMOVE`/`BRPOPLPUSH` to a processing list, or Stream consumer group with ack + dead-letter |
| soketi/Pusher | Single event > 10KB; one event per output line; publish before subscribe | Batch + chunk under 10KB, sequence numbers, start-handshake; rate-limit publishes |
| soketi auth | Worker assumes it can publish to any `private-` channel the client sees | Auth owned by TS API; worker publishes to the agreed channel only |
| cgroup | Reading v2 files (`cpu.stat`, `memory.max`) and assuming all hosts are v2 | Detect v1/v2 at runtime; assert/pin v2 in prod; CPU clock reads the right files |
| Container kill | `Process.Kill()` on the entrypoint PID | `docker rm -f`/`kill` the container → PID namespace kills the whole tree |

## Performance Traps

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| Unbounded output buffering | Worker memory climbs with chatty programs | Output byte cap + truncate while draining the pipe | First program that prints MBs |
| One soketi event per line | soketi CPU spikes, events dropped | Coalesce/batch on 50–100ms or ~8KB flush, <10KB each | A loop printing thousands of lines |
| tmpfs without `size=` | Host RAM exhausted by file writes | Always `size=`-cap tmpfs (counts against memory budget) | First program that writes a big temp file |
| PID exhaustion across sandboxes | Host PID space full though each sandbox is within `--pids-limit` | Global slot cap sized vs host headroom | High concurrency of fork-heavy sessions |
| All workers `BRPOP` same key | Contention/wake storm after burst or Redis failover | Jitter, reliable-queue, bounded concurrency | Failover or traffic burst at N workers |
| Long-lived interactive sessions saturate slots | "Full" at low request rate | Capacity = concurrent live sandboxes; scale on slot utilization | A handful of idle-but-open sessions |

## Security Mistakes

| Mistake | Risk | Prevention |
|---------|------|------------|
| Missing `--security-opt=no-new-privileges` | setuid/file-cap privilege re-gain inside sandbox | Always set it; assert in test |
| Partial `cap-drop` (leaving SYS_ADMIN/NET_RAW/PTRACE) | Escape / host interaction | `--cap-drop=ALL`, add back nothing untrusted code doesn't strictly need |
| Permissive/unconfined seccomp | Dangerous syscalls (ptrace, mount, keyctl, bpf, userfaultfd) reachable | Restrictive allowlist per runtime; remove namespace/clone/mount syscalls |
| `--network=none` omitted | SSRF into private net, reach Redis/soketi/metadata, exfil | Network none on every execution incl. compile |
| Sandbox runs as root | Larger blast radius on kernel bug | Non-root `--user`; non-root image user |
| Docker socket reachable from sandbox | Host root | Socket only in worker; never bind into sandbox |
| Writable rootfs / no `--read-only` | Tamper/persistence/disk fill | `--read-only` + size-capped tmpfs for scratch |
| Swap enabled (`--memory != --memory-swap`) | Memory cap becomes soft; thrash | Set equal to disable swap |
| Trusting anything inbound from soketi | Untrusted input crosses the boundary | soketi output-only; all trust via TS API |

## "Looks Done But Isn't" Checklist

- [ ] **Timeouts:** wall-clock works — verify the **CPU clock** also kills a "read-one-byte-then-spin" interactive program, and that **idle** actually fires (timer wired to last-activity, not started-and-forgotten).
- [ ] **Kill:** "killed" logged — verify `docker ps` shows **no surviving container** and host CPU drops (tree, not PID).
- [ ] **Cleanup:** happy path frees the slot — verify **every** terminal path (wall/idle/CPU/`/kill`/crash/OOM) frees slot + unsubscribes + destroys container, **idempotently**.
- [ ] **Crash recovery:** `kill -9` a worker mid-session — verify a **reaper** removes its orphaned containers and reclaims its slots (not just `defer`).
- [ ] **stdin EOF:** `/stdin/close` — verify a REPL (`input()`, sqlite3 shell) **exits cleanly**, not via idle-timeout.
- [ ] **Early prompt:** a program that prompts immediately — verify the client **receives the first prompt** (start-handshake working).
- [ ] **Output cap:** giant output — verify truncation with `truncated=true`, worker memory stays flat, and **no event exceeds 10KB**.
- [ ] **Backpressure:** flood stdin — verify pending-stdin cap returns **429** and worker memory stays bounded.
- [ ] **Hardening:** assert in a test that every run has network=none, read-only, no-swap, pids-limit, cap-drop=ALL, no-new-privileges, restrictive seccomp, non-root.
- [ ] **cgroup:** CPU clock + OOM verified on a real **Linux cgroup-v2** host in CI, not just Docker Desktop.
- [ ] **Compile:** Rust compile has its **own** limits, is hardened + network-none, and a compile-bomb is killed (tree).

## Recovery Strategies

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Orphaned containers | LOW (with reaper) / MEDIUM (manual) | Label-based reaper sweeps dead-worker containers; manual `docker rm -f` by label as stopgap |
| Leaked slots | LOW (with TTL) / HIGH (in-memory) | Redis slot keys with TTL self-heal; in-memory requires worker restart |
| stdin loss on pub/sub | MEDIUM | Detect dead session → terminal event to client; migrate transport to Streams |
| CPU escaping wall-clock | HIGH if shipped | Add cgroup CPU clock; retrofit requires runner-interface change — do it in P2, not after |
| Single-timeout/tree-orphan | MEDIUM | Switch kill primitive to container-destroy; add process-tree test |
| Queue poisoning | MEDIUM | Add max-retry + dead-letter; quarantine the payload |
| soketi >10KB drops | LOW | Add chunking/batching layer in the publisher |
| cgroup v1/v2 mismatch in prod | MEDIUM | Make clock/OOM version-aware; pin v2; re-test on Linux |

## Pitfall-to-Phase Mapping

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| 1. CPU hidden behind interactivity | P2 (three clocks) | Abuse test: read-one-byte-then-spin is killed by CPU clock (P7) |
| 2. Tree orphans on timeout | P2 (kill = destroy container) | `docker ps` empty + CPU drops after kill (P7) |
| 3. Hardening gaps | P2 (full flag set) | Assertion test of all flags; re-audit per new image (P6) |
| 4. Docker socket exposure | P2 (socket scoping) | Sandbox cannot reach socket/network (P7) |
| 5. Resource evasion (fork/OOM/tmpfs/PID) | P2 (per-sandbox) + P5 (global slot cap) | Fork bomb, OOM, tmpfs-fill, output-flood tests (P7) |
| 6. stdin deadlocks/EOF/early prompt | P3 (handshake, goroutines, real close) | Python REPL E2E + SQLite shell EOF (P3/P6) |
| 7. pub/sub loss / claimed-not-started | P3 (subscribe-before-start) + P5 (reliable claim, dead-worker) | Chaos test: restart worker mid-session (P7) |
| 8. soketi 10KB/ordering/auth | P3 (publisher: batch, <10KB, seq) | Large-output + ordering test (P7) |
| 9. Leaks/cleanup | P4 (single idempotent teardown) + P5 (reaper, TTL slots) | Kill worker mid-session → no orphans/leaked slots (P7) |
| 10. Stateless-scaling failures | P5 (dead-worker terminal, dead-letter, slot capacity) | Poison-job + worker-recycle tests (P7) |
| 11. gVisor caveats | Future runtime phase (interface in P2) | Per-language compatibility test under runsc |
| 12. macOS-dev vs Linux-prod | P2 (cgroup-version-aware) | Clocks/OOM/abuse suite in Linux CI (P7) |
| 13. Compile vs run limits | P6 (Rust + compile step; manifest shape in P1) | Compile-bomb killed; legit Rust compiles within compile budget (P6/P7) |

## Sources

- Docker resource constraints (memory/swap, OOM) — https://docs.docker.com/engine/containers/resource_constraints/ (HIGH)
- OOM-killer cgroup v1 vs v2 scope, swap masking — https://securebin.ai/blog/fix-docker-container-oom-killed/ , https://oneuptime.com/blog/post/2026-01-24-fix-oom-killer-memory-issues/view (MEDIUM, corroborated)
- gVisor performance (CPU-bound no penalty; syscall-heavy overhead) — https://gvisor.dev/docs/architecture_guide/performance/ (HIGH)
- gVisor syscall subset / ENOSYS on unimplemented — https://github.com/google/gvisor/blob/master/test/syscalls/README.md , https://groups.google.com/g/gvisor-users/c/k8Zwc8df-K4 (HIGH/MEDIUM)
- Pusher/soketi 10KB event size limit — Pusher Channels limits docs; https://docs.soketi.app/rate-limiting-and-limits/events-and-channels-limits (HIGH)
- Piston / isolate sandbox model (namespaces, cgroups pids.max, cleanup) — https://github.com/engineer-man/piston , https://deepwiki.com/engineer-man/piston/2.1-code-execution-engine (MEDIUM)
- RCE sandbox hardening overview (cap-drop, seccomp, network none, pids) — https://northflank.com/blog/remote-code-execution-sandbox (MEDIUM)
- Redis pub/sub no-delivery-guarantee vs Streams at-least-once — Redis docs (training + corroborated) (MEDIUM)
- Interactive pipe deadlock / backpressure, process-group kill, cgroup CPU accounting — domain experience, corroborated by above (MEDIUM)

---
*Pitfalls research for: sandboxed remote code-execution engine (Go, untrusted code, interactive stdin)*
*Researched: 2026-06-02*
