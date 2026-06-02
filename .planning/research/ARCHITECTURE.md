# Architecture Research

**Domain:** Sandboxed remote code execution (Piston-style) with live interactive stdin, Go
**Researched:** 2026-06-02
**Confidence:** HIGH (core systems design + verified Docker/Pusher/Redis semantics); MEDIUM on soketi-specific default limits (configurable, see Sources)

## Standard Architecture

### System Overview

```
┌──────────────────────────────────────────────────────────────────────────┐
│  PUBLIC EDGE (exists, out of scope)                                        │
│  ┌──────────┐   triggers auth, owns user trust, owns Pusher channel auth   │
│  │ TS API   │   (private-run-<jobId> authorization happens HERE)           │
│  └────┬─────┘                                                              │
└───────┼────────────────────────────────────────────────────────────────────┘
        │  HTTP over PRIVATE network (shared-secret bearer / mTLS)
        │  POST /execute, /run/:id/start, /run/:id/stdin, /stdin/close, /kill
┌───────▼────────────────────────────────────────────────────────────────────┐
│  EXECUTOR API  (Go, stateless, N replicas)                                  │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────────┐  ┌──────────────┐    │
│  │ HTTP router │  │ enqueue      │  │ stdin gateway │  │ control      │    │
│  │ + authn     │  │ (LPUSH jobs) │  │ (PUBLISH      │  │ (PUBLISH     │    │
│  │             │  │              │  │  stdin:<id>)  │  │  ctrl:<id>)  │    │
│  └─────────────┘  └──────┬───────┘  └───────┬───────┘  └──────┬───────┘    │
│   Holds NO live state. Knows nothing about which worker owns a job.         │
└──────────────────────────┼──────────────────┼──────────────────┼───────────┘
                           │ Redis            │ Redis pub/sub    │ Redis pub/sub
┌──────────────────────────▼──────────────────▼──────────────────▼───────────┐
│  REDIS                                                                       │
│  ┌──────────────┐  ┌────────────────────┐  ┌────────────┐  ┌────────────┐  │
│  │ jobs (LIST   │  │ job:<id>:meta      │  │ stdin:<id> │  │ ctrl:<id>  │  │
│  │ or STREAM    │  │ (HASH, code/lang/  │  │ (channel,  │  │ (channel,  │  │
│  │ + group)     │  │  limits, status)   │  │  per-job)  │  │  per-job)  │  │
│  └──────┬───────┘  └────────────────────┘  └─────▲──────┘  └─────▲──────┘  │
└─────────┼──────────────────────────────────────────┼──────────────┼────────┘
          │ BRPOP / XREADGROUP (claim)                │ SUBSCRIBE only │
          │                                           │ for OWNED jobs │
┌─────────▼──────────────────────────────────────────┴────────────────┴───────┐
│  WORKER POOL  (Go, stateless across restarts, N replicas)                   │
│  Each worker owns the live sandboxes it launched. THAT is the only state.   │
│  ┌────────────────────────────────────────────────────────────────────┐    │
│  │ claim loop → Session{} per live job (goroutine tree)               │    │
│  │   ├─ clocks: wall timer | idle timer | cpu cgroup poller          │    │
│  │   ├─ stdin sub  → process stdin pipe                              │    │
│  │   ├─ stdout/stderr pump → byte-capped → soketi publisher (batched)│    │
│  │   └─ slot accounting (semaphore: max live sandboxes/worker)       │    │
│  └────────────────────────┬───────────────────────────────────────────┘    │
└───────────────────────────┼─────────────────────────────────────────────────┘
        Runner interface     │ (mounted /var/run/docker.sock, NO DinD)
┌───────────────────────────▼─────────────────────────────────────────────────┐
│  HOST CONTAINER RUNTIME → ephemeral hardened sandbox (1 per live session)    │
│  net=none, ro rootfs + tmpfs, mem cap no-swap, pids-limit, cpus,             │
│  cap-drop=ALL, no-new-privileges, seccomp profile. Runner iface → gVisor.    │
└──────────────────────────────────────────────────────────────────────────────┘
                           │ Pusher HTTP API (trigger / batch_events)
                           ▼ channel private-run-<jobId>  (OUTPUT ONLY)
                        soketi ──► client (via TS-API-authorized subscription)
```

### Component Responsibilities

| Component | Responsibility | Implementation |
|-----------|----------------|----------------|
| Executor API | Validate + authenticate requests from TS API; enqueue jobs; relay stdin/control onto Redis pub/sub; never touches sandboxes | Go HTTP server (net/http or chi/echo), Redis client (go-redis) |
| Redis | Job queue (decouple receive/execute, backpressure), job metadata, per-job stdin + control channels | Single Redis; LIST or STREAM for queue, pub/sub for stdin/ctrl |
| Worker | Claim jobs; own the lifecycle of each live sandbox; run 3 clocks; pump output; route stdin to the right process; enforce per-worker slot capacity | Go daemon, go-redis, Runner interface impl |
| Runner | Abstract "hardened sandbox": create/attach/limit/kill/cleanup. The only thing that knows about Docker (or later gVisor) | Go interface; Docker SDK impl over mounted socket |
| soketi | Output-only realtime fan-out to clients | Pusher-compatible server; published to via Pusher HTTP API |
| Language manifests | Declare image/entrypoint/compile/run/limits; loaded at boot; zero hardcoded langs | `languages/<lang>/{manifest.json,Dockerfile}` read into a registry |

## Recommended Project Structure

**One Go module, two binaries.** The Executor API and Worker share the same domain types (job, manifest, limits, event shapes), the same Redis key/channel conventions, and the same soketi client. Splitting into separate modules would force a third "shared" module and version drift. Keep them in one module under `cmd/`.

```
code-runner/
├── go.mod
├── cmd/
│   ├── executor/main.go        # Executor API binary entrypoint
│   └── worker/main.go          # Worker binary entrypoint
├── internal/
│   ├── api/                    # Executor: HTTP handlers, authn middleware, request DTOs
│   │   ├── handlers.go         #   /execute, /run/:id/start, /stdin, /stdin/close, /kill
│   │   └── auth.go             #   shared-secret bearer check (TS API ↔ Executor)
│   ├── queue/                  # Redis queue abstraction (enqueue / claim)
│   │   ├── queue.go            #   interface: Enqueue, Claim, Ack, MarkStarted
│   │   └── redis_list.go       #   LIST impl (MVP); redis_stream.go later
│   ├── channels/               # Redis pub/sub key + channel naming, publish/subscribe helpers
│   │   └── channels.go         #   jobsKey, stdinChan(id), ctrlChan(id), metaKey(id)
│   ├── job/                    # Job domain model + state machine (queued→started→running→terminal)
│   │   └── job.go
│   ├── manifest/               # Manifest schema, loader, registry (language-agnostic core)
│   │   ├── manifest.go
│   │   └── registry.go
│   ├── runner/                 # THE Runner interface + implementations
│   │   ├── runner.go           #   interface (Create/Start/Stdin/Wait/Stats/Kill/Cleanup)
│   │   ├── docker/             #   Docker hardened impl (mounted socket)
│   │   └── gvisor/             #   (future) runsc impl — same interface
│   ├── session/                # Worker: per-job orchestration, the 3 clocks, output pumps
│   │   ├── session.go          #   goroutine tree, clock supervision, lifecycle/cleanup
│   │   ├── clocks.go           #   wall / idle / cpu(cgroup) implementations
│   │   └── pump.go             #   stdout/stderr → byte cap → publisher
│   ├── slots/                  # Per-worker capacity accounting (concurrent live sandboxes)
│   │   └── slots.go
│   └── publisher/              # soketi/Pusher HTTP client (sign, batch, channel naming)
│       └── soketi.go
├── languages/                  # Drop-a-folder extensibility (no core change)
│   ├── python-3.12/{manifest.json,Dockerfile}
│   ├── rust/{manifest.json,Dockerfile}
│   ├── r-4.4/{manifest.json,Dockerfile}
│   └── sqlite-3/{manifest.json,Dockerfile}
├── deploy/
│   ├── docker-compose.yml
│   └── seccomp/default.json
└── test/abuse/                 # fork bomb, OOM, infinite loop, idle, EOF, giant output
```

### Structure Rationale

- **`cmd/{executor,worker}` + shared `internal/`:** two binaries, one module — shared types and Redis conventions stay in lockstep, no submodule versioning.
- **`runner/` is the only Docker-aware package:** everything above it speaks the interface, so the gVisor swap is one new subpackage with zero changes to `session/`. This is the single most important boundary in the system.
- **`session/` lives only in the worker path:** it is where the only mutable live state in the whole system resides. Keeping it isolated makes "what is stateful" obvious.
- **`manifest/registry` is read-only after boot:** core never imports a language. Adding Python vs Rust is data, not code.
- **`channels/` centralizes naming:** `stdin:<id>`, `ctrl:<id>`, `private-run-<id>`, `jobs`, `job:<id>:meta` defined once so executor and worker can never disagree.

## Architectural Patterns

### Pattern 1: Two-phase start handshake (queue-and-hold)

**What:** `/execute` enqueues the job and returns a `jobId` immediately — but the worker that claims it **does not start the process**. The worker creates the sandbox, attaches pipes, and parks at a "ready, awaiting start" gate. Only when the client has subscribed to `private-run-<jobId>` does the TS API call `/run/:jobId/start`, which publishes a `start` message on `ctrl:<jobId>`; the owning worker then runs the entrypoint. This guarantees no early prompt/output is emitted before the client is listening.

**When to use:** Always for interactive sessions. The whole point is that the first prompt (`>>>`, `sqlite>`) must not be lost.

**Trade-offs:** A slot is reserved (sandbox created) during the window between claim and start — capacity must account for "warming" sessions, and a `start` that never arrives needs a short warm-up timeout to reclaim the slot. The alternative (start on enqueue, buffer output) is fragile and loses the "live" property; reject it.

**Mechanics mapping:**
- `POST /execute` → `LPUSH jobs <jobId>` + write `job:<id>:meta`, status=`queued`. Returns `jobId`.
- Worker `BRPOP jobs` → claims → `runner.Create()` + attach → status=`warming` → `SUBSCRIBE ctrl:<id>` + `SUBSCRIBE stdin:<id>`.
- `POST /run/:id/start` → executor `PUBLISH ctrl:<id> {"type":"start"}`.
- Worker receives `start` → `runner.Start()` (runs entrypoint) → status=`running`, clocks begin.

```go
// Worker, after claim:
sess.runner.Create(ctx, manifest)   // sandbox up, pipes attached, process NOT started
sub := redis.Subscribe(ctx, ctrlChan(id), stdinChan(id))
select {
case m := <-startSignal(sub):       // ctrl start arrived
    sess.start()                    // runner.Start() → clocks on
case <-time.After(warmupTimeout):   // client never showed → reclaim slot
    sess.cleanup("warmup-expired")
}
```

### Pattern 2: Ownership-by-subscription stdin routing (no service discovery)

**What:** The worker that owns a job is the *only* process subscribed to `stdin:<jobId>` and `ctrl:<jobId>`. The Executor API blindly `PUBLISH stdin:<jobId> <bytes>` without knowing or caring which worker holds it — Redis pub/sub delivers to whichever subscriber exists. Ownership is implicit: it is whoever subscribed. No registry, no "which worker has job X" lookup, no service discovery.

**When to use:** This is the core trick that makes workers stateless and discovery-free. Use it for stdin and control.

**Trade-offs:** pub/sub is fire-and-forget. If the owning worker is mid-restart there is a window with *no* subscriber and that stdin frame is silently dropped. For an interactive REPL this is usually acceptable for MVP (the user just retypes), but it is the failure mode to document and the reason Streams is the upgrade path (Pattern 6).

```go
// Executor (stdin gateway) — zero knowledge of topology:
redis.Publish(ctx, stdinChan(jobId), frame)   // someone owns it, or nobody does
```

### Pattern 3: The Runner interface (sandbox abstraction)

**What:** A narrow interface that fully covers the sandbox lifecycle while hiding Docker. Designed so a Docker-hardened impl and a future gVisor (`runsc`) impl satisfy it identically. The interface returns a `Sandbox` handle holding the attached streams.

```go
type Limits struct {
    WallMs, IdleMs, CpuMs int
    MemoryMB, Pids        int
    NanoCPUs              int64   // cpus
    OutputKB             int
}

type Spec struct {
    Image      string
    Entrypoint []string
    Cmd        []string         // run (or compile-then-run resolved by caller)
    Limits     Limits
    Interactive bool
}

type Runner interface {
    // Create: build the hardened container, attach pipes, do NOT start the entrypoint.
    Create(ctx context.Context, spec Spec) (Sandbox, error)
}

type Sandbox interface {
    Start(ctx context.Context) error          // run entrypoint
    Stdin() io.WriteCloser                     // pipe to process stdin
    Stdout() io.Reader                         // demuxed stdout
    Stderr() io.Reader                         // demuxed stderr
    Stats(ctx context.Context) (CPUStat, error)// cgroup cpu usage for CPU clock
    Wait(ctx context.Context) (ExitInfo, error)// blocks until process exits
    Kill(ctx context.Context) error            // SIGKILL the process
    Cleanup(ctx context.Context) error         // force-remove container, free everything
    ID() string
}
```

**Why these methods:** They map 1:1 to the required operations — create hardened sandbox (`Create` with all `Limits`/hardening flags), attach stdin/out/err (`Stdin/Stdout/Stderr`), enforce 3 clocks (`Stats` feeds the CPU clock; wall/idle are timers the session owns; `Kill` is what every clock calls on expiry), kill (`Kill`), cleanup (`Cleanup`). Hardening (net=none, ro rootfs+tmpfs, no-swap mem, pids-limit, cap-drop=ALL, no-new-privileges, seccomp) lives entirely inside the Docker impl's `Create` — it maps onto `HostConfig{Memory, MemorySwap:Memory, NanoCPUs, PidsLimit, CapDrop:["ALL"], ReadonlyRootfs:true, NetworkMode:"none", SecurityOpt:["no-new-privileges","seccomp=..."], Tmpfs:{...}}`. gVisor reuses the same Docker SDK path with `runtime: "runsc"`, so the gVisor impl can even be a thin variant of the Docker impl.

**Trade-offs:** `Stats`-based CPU clock means polling (e.g. every 100–250ms) rather than a hard cgroup kill; acceptable and runtime-portable. Demuxing stdout/stderr (Docker multiplexes them over one attach stream unless TTY) must be handled in the Docker impl with `stdcopy.StdCopy`, kept out of `session/`.

### Pattern 4: Per-session goroutine tree with a single supervisor

**What:** Each live job is a `Session` owning a small fixed set of goroutines, supervised by one loop that any of them can signal to terminate. On the first terminal signal (clock expiry, process exit, kill, EOF after close, output-cap-on-kill), the supervisor runs `cleanup` exactly once.

```
Session(jobId)
 ├─ stdinLoop   : <-stdinSub → sandbox.Stdin().Write(); resets idle clock
 ├─ stdoutPump  : sandbox.Stdout() → cap+batch → publisher (stdout events)
 ├─ stderrPump  : sandbox.Stderr() → cap+batch → publisher (stderr events)
 ├─ wallTimer   : time.AfterFunc(wallMs)        → terminate("wall")
 ├─ idleTimer   : reset on stdin/stdout activity → terminate("idle")
 ├─ cpuPoller   : ticker → sandbox.Stats() → if cpuMs exceeded terminate("cpu")
 └─ waiter      : sandbox.Wait()                → terminate("exit", code)

terminate(reason) → close(done) once (sync.Once) →
    unsubscribe stdin/ctrl → close pipes → publish result → sandbox.Cleanup() → free slot
```

**The three clocks:**
- **Wall clock:** plain `time.AfterFunc(wallMs)`. Hard ceiling on total session lifetime.
- **Idle clock:** a resettable timer; reset on any stdin received *and* on any stdout/stderr produced (configurable — at minimum reset on stdin so a blocked-on-input REPL eventually dies). Catches "waiting for input nobody will send."
- **CPU clock:** poll `sandbox.Stats()` (cgroup `cpu.stat`/`cpuacct.usage`); when cumulative CPU-time ≥ `cpuMs`, terminate. Catches infinite loops that produce no output and never block. Poll interval (100–250ms) bounds overshoot.

**Trade-offs:** `sync.Once` on terminate prevents double-cleanup races (the hardest bug class here — process exit and a clock firing simultaneously). Context cancellation propagates to all child goroutines so none leak.

### Pattern 5: Slot-based backpressure (capacity = concurrent live sandboxes)

**What:** Capacity is counted in **live sandboxes**, not requests, because an interactive session holds resources until a clock expires. Each worker holds a counting semaphore sized by `min(CPU-derived, RAM-derived)` slots: `slots = min(allocatableCPUs / cpusPerSandbox, allocatableRAM / memPerSandbox)`. A worker only `BRPOP`s a new job when it has a free slot (acquire-before-claim). When all workers are full, the `jobs` list simply grows → that *is* the backpressure. The Executor API enforces a queue-depth ceiling (`LLEN jobs`); above it, `/execute` returns **429**. Pending-stdin byte cap and stdin rate-limit are enforced at the API layer (also 429).

```go
// Worker claim loop:
for {
    slots.Acquire()                 // blocks until a sandbox slot frees
    jobID, err := queue.Claim(ctx)  // BRPOP only AFTER we have a slot
    if err != nil { slots.Release(); continue }
    go session.Run(jobID, func(){ slots.Release() })  // release on terminate
}
```

**Trade-offs:** Acquire-before-claim means an idle-but-full worker won't drain the queue, which is correct — it can't run more. Warming sessions (Pattern 1) must consume a slot too, or a burst of `/execute` could over-commit RAM.

### Pattern 6: Pub/Sub now, Streams as the upgrade (stdin reliability)

**What & trade-off:**

| Aspect | Redis Pub/Sub (MVP) | Redis Streams + consumer group (upgrade) |
|--------|---------------------|------------------------------------------|
| Delivery | Fire-and-forget; dropped if no subscriber | Persisted; `XREADGROUP` + `XACK`, redeliverable |
| Discovery | None needed — subscriber = owner | Group `job:<id>`, consumer = workerID; still no central registry |
| Worker death mid-session | stdin frames in flight are lost; session orphaned until clocks kill the sandbox on its host (or sandbox dies with worker if co-located) | Unacked frames can be claimed via `XAUTOCLAIM` if another worker re-adopts the sandbox (only feasible if sandboxes survive worker death) |
| Ordering | Per-channel ordered | Per-stream ordered + IDs |
| Complexity | Trivial | Trim/ack/idle-claim bookkeeping |

**Recommendation:** Ship pub/sub. It is sufficient because (a) there is no service discovery to build, (b) interactive stdin loss on a rare worker restart is tolerable (user retypes), and (c) the sandbox is bound to its worker's host socket, so a dead worker's sessions are doomed regardless of stdin durability — Streams would not save the session without sandbox-survives-worker re-adoption, which is out of scope. Document the failure mode and leave the Streams seam (the `queue/` and `channels/` interfaces) so the swap is localized.

**Failure mode if a worker dies mid-session (MVP):** the sandbox either dies with the worker (if lifecycle-tied) or is orphaned on the host and reaped by the worker's own clocks dying with it — add a **janitor sweep** (label every sandbox `runner.jobId=<id>` + `runner.worker=<id>`; a periodic sweep removes containers whose owning worker is gone). The client sees the `private-run-<id>` channel go silent; the TS API should treat prolonged silence past wall-clock as session-dead.

## Data Flow

### Request / lifecycle flow (Python interactive, the E2E target)

```
TS API ──POST /execute {lang:python, code}──► Executor
Executor: LPUSH jobs <id>; HSET job:<id>:meta ...; status=queued ──► returns {jobId}
Worker:  slots.Acquire(); BRPOP jobs → <id>; runner.Create(python spec) [process NOT started]
Worker:  SUBSCRIBE stdin:<id>, ctrl:<id>; status=warming
TS API ──(client now subscribed to private-run-<id>)── POST /run/<id>/start ──► Executor
Executor: PUBLISH ctrl:<id> {start} ──► Worker: sandbox.Start(); clocks on; status=running
Sandbox stdout ">>> " ──► stdoutPump ──► publisher.batch ──► soketi trigger ──► client
TS API ──POST /run/<id>/stdin {data}──► Executor: PUBLISH stdin:<id> ──► Worker: stdin.Write(); reset idle
... loop ...
TS API ──POST /run/<id>/stdin/close──► Executor: PUBLISH ctrl:<id> {eof} ──► Worker: stdin.Close()
Process exits ──► waiter ──► terminate("exit",0) ──► publish result ──► cleanup ──► slots.Release()
```

### Event shapes (soketi, channel `private-run-<jobId>`)

```
event "stage"  data {"stage":"warming"|"running"|"compiling"|"done"}
event "stdout" data {"seq":N,"chunk":"...","truncated":false}
event "stderr" data {"seq":N,"chunk":"...","truncated":false}
event "result" data {"exitCode":0,"reason":"exit"|"wall"|"idle"|"cpu"|"oom"|"killed","truncated":bool}
```

### soketi publish path

- **Channel naming:** `private-run-<jobId>` (the `private-` prefix triggers Pusher channel auth — performed by the **TS API**, not the executor; executor only *publishes*, which needs no channel auth, only the app key/secret signature).
- **Auth (TS API ↔ Executor):** the private-network HTTP contract is protected by a shared-secret bearer header (constant-time compare) or mTLS — *separate* from Pusher app-secret signing. Two different secrets, two different boundaries.
- **Auth (Executor ↔ soketi):** Pusher REST signature — HMAC-SHA256 over `POST\n/apps/<app_id>/batch_events\n<sorted query incl. auth_key, auth_timestamp, auth_version=1.0, body_md5>`. Use the official `pusher-http-go` client or a thin signer.
- **Batching & byte caps:** coalesce rapid stdout chunks within a short flush window (e.g. 25–50ms) into one `batch_events` call. Respect payload limits: hosted Pusher caps event data at **10KB** and **10 events per batch**; **soketi makes both configurable** (default event payload ~100KB, batch size configurable) — size your `outputKb` cap and flush window against the soketi config you ship, and always set `truncated=true` when you cut. Per-event `seq` lets the client detect drops.

## Scaling Considerations

| Scale | Architecture Adjustments |
|-------|--------------------------|
| 1 worker / dev | docker compose, single Redis, mounted socket — works as-is |
| N workers, 1 host each | stateless workers scale horizontally; capacity = Σ per-worker slots; queue absorbs bursts; this is the design target |
| Many hosts | one worker per host (each owns its host's runtime socket); add a janitor for orphan reaping; Redis remains single point — add Redis HA before Redis is the bottleneck |
| Heavy interactive load | soketi becomes the fan-out bottleneck before the executor does — scale soketi horizontally (it supports adapters) and batch aggressively |

### Scaling Priorities

1. **First bottleneck — per-worker slots (RAM):** interactive sessions hold RAM for their whole life. Tune `memPerSandbox` and add workers/hosts. Queue depth + 429s are the relief valve.
2. **Second bottleneck — soketi fan-out:** high-frequency stdout (verbose programs) floods publishes. Mitigate with batching window + output byte caps before considering soketi scale-out.
3. **Third — Redis pub/sub throughput:** only at large N; the upgrade to Streams (Pattern 6) also helps here.

## Anti-Patterns

### Anti-Pattern 1: Tracking "which worker owns which job" in Redis

**What people do:** Build a `job:<id>:worker` registry so the API can route stdin to the right worker.
**Why it's wrong:** Reintroduces service discovery, creates a consistency problem on worker death, and makes the API stateful about topology. The whole stdin-via-pub/sub design exists to avoid this.
**Do this instead:** Let subscription *be* ownership (Pattern 2). The API publishes blindly; Redis routes.

### Anti-Pattern 2: Starting the process on enqueue / on claim

**What people do:** Run the entrypoint as soon as the worker picks up the job, then buffer early output until the client subscribes.
**Why it's wrong:** Buffering is lossy and racy for interactive prompts; "live" is no longer live; idle/wall clocks start before the user can interact.
**Do this instead:** Two-phase handshake (Pattern 1) — create-and-hold, start only on `ctrl:<id>` start.

### Anti-Pattern 3: Hardcoding language behavior in Go

**What people do:** `if lang == "rust" { compile... }` branches in core.
**Why it's wrong:** Defeats the central extensibility requirement; every new language touches core.
**Do this instead:** Manifest declares `compile` (nullable) and `run`; the session executes a generic compile-then-run pipeline driven by data. Adding a language is a folder + image.

### Anti-Pattern 4: Docker-in-Docker for sandboxes

**What people do:** Run a Docker daemon inside the worker container.
**Why it's wrong:** Heavy, privileged, slow, weaker isolation, explicitly out of scope.
**Do this instead:** Mount the host runtime socket; the worker is a *client* of the host runtime (Pattern: Runner over mounted socket). Label every sandbox for janitor reaping.

### Anti-Pattern 5: One Redis pub/sub channel for all stdin

**What people do:** A single `stdin` channel with `jobId` in the payload; every worker filters.
**Why it's wrong:** Every worker receives every keystroke and filters in app code — O(workers×traffic) waste and a privacy smell.
**Do this instead:** Per-job channel `stdin:<id>`; only the owner subscribes.

## Integration Points

### External Services

| Service | Integration Pattern | Notes |
|---------|---------------------|-------|
| TS API (inbound) | HTTP on private net; shared-secret bearer or mTLS | All trusted input enters here; it owns Pusher channel auth and user trust |
| Redis | go-redis; LIST/STREAM queue + per-job pub/sub channels + meta hashes | Single instance MVP; HA later; the one shared dependency |
| Host container runtime | Mounted `/var/run/docker.sock` via Docker Go SDK; `stdcopy` for stream demux | No DinD; label sandboxes for janitor; gVisor = same SDK + `runtime:runsc` |
| soketi (outbound) | Pusher HTTP API (`/apps/<id>/batch_events`), HMAC-SHA256 signed | Output only; nothing trusted enters via soketi; size caps vs soketi config |

### Internal Boundaries

| Boundary | Communication | Notes |
|----------|---------------|-------|
| Executor ↔ Worker | Indirect via Redis only (queue + pub/sub) — never direct HTTP | This is what makes both stateless and discovery-free |
| Worker ↔ Sandbox | Runner interface → Docker SDK → mounted socket | The single Docker-aware seam; gVisor swaps here |
| Session ↔ Runner | `Sandbox` handle (pipes + Stats + Kill + Cleanup) | Session owns clocks; Runner owns isolation |
| Core ↔ Languages | Manifest registry loaded at boot | Data-driven; zero language code in core |

## Suggested Build Order (dependency graph)

Goal: **Python interactive E2E** (stdin + 3 clocks + streamed output) before fanning out to other languages. Build bottom-up so each layer is testable against the one below.

```
1. manifest/  ──────────────► schema + loader + registry; python-3.12 manifest+Dockerfile
        │                     (define the data contract first; everything reads it)
        ▼
2. runner/docker  ──────────► Create(hardened) / Start / Stdin / Stdout / Stderr / Stats /
        │                     Wait / Kill / Cleanup over mounted socket. Unit-test against
        │                     a real container: run python, write stdin, read stdout, kill.
        ▼
3. channels/ + queue/redis_list ─► key/channel naming; Enqueue/Claim/Ack; pub/sub helpers.
        │                          Testable with a real Redis, no HTTP yet.
        ▼
4. session/ (clocks + pumps) ──► wire Runner + channels into the goroutine tree; the three
        │                        clocks; sync.Once cleanup; byte-capped pumps (publisher
        │                        stubbed to stdout first). The interactive heart.
        ▼
5. publisher/soketi  ──────────► signed Pusher HTTP client + batching; replace the pump stub.
        │
        ▼
6. cmd/worker  ────────────────► claim loop + slots; assemble session per job.
        │
        ▼
7. cmd/executor + api/  ───────► /execute, /run/:id/start, /stdin, /stdin/close, /kill;
        │                        authn middleware; queue-depth 429s; stdin rate-limit/byte-cap.
        ▼
8. deploy/docker-compose  ─────► executor + worker + redis + soketi + TS-API stub + socket.
        │                        First full Python interactive E2E runs here.
        ▼
9. abuse/test  ────────────────► fork bomb, OOM, infinite loop (cpu), idle, EOF, giant output.
        ▼
10. languages/{rust,r,sqlite}  ─► reuse everything; only data (manifest+image) changes.
                                  Rust exercises the compile-then-run path; sqlite the shell.
```

**Critical path note:** steps 2 and 4 (Runner + session/clocks) are where all the real risk lives — schedule deeper validation there. Steps 1, 3, 7, 8 are mechanical. Step 10 should require *zero* core changes; if it doesn't, the manifest abstraction (Pattern: manifest-driven) leaked and must be fixed before declaring extensibility done.

## docker compose topology (local dev)

```
services:
  redis:        # queue + stdin/ctrl pub/sub + meta
  soketi:       # Pusher-compatible; env app_id/key/secret; output fan-out
  executor:     # build cmd/executor; depends_on redis; private port; SHARED_SECRET, REDIS_URL
  worker:       # build cmd/worker; depends_on redis,soketi; scale: N
                # volumes: /var/run/docker.sock:/var/run/docker.sock   (mounted runtime)
                # mounts ./languages (manifests) read-only; SLOTS env; PUSHER_* env
  ts-api-stub:  # mock public API: enqueues via executor, drives start/stdin, prints soketi msgs
```

Key wiring: worker mounts the **host docker socket** (no DinD) and the `languages/` tree; executor and worker share `REDIS_URL` and the soketi `PUSHER_*` credentials; `SHARED_SECRET` gates ts-api-stub → executor. `docker compose up --scale worker=2` validates the stateless/multi-replica property locally (publish stdin → only the owning replica reacts).

## Sources

- Pusher Channels HTTP API — trigger + `batch_events` endpoints, 10KB event-data limit, 100-channel/10-event batch caps, HMAC-SHA256 signature over method+path+sorted-query+body_md5 (HIGH): https://pusher.com/docs/channels/library_auth_reference/rest-api/
- soketi — configurable event payload max size and batch_events limits (more lenient than hosted Pusher; defaults are config-dependent) (MEDIUM, configurable): https://docs.soketi.app/rate-limiting-and-limits/events-and-channels-limits and https://github.com/soketi/soketi
- Docker Engine API / Go SDK — HostConfig fields (Memory, MemorySwap, NanoCPUs, PidsLimit, CapDrop, ReadonlyRootfs, NetworkMode, SecurityOpt, Tmpfs), ContainerAttach hijacked streams, stdcopy demux (HIGH, stable API surface): https://docs.docker.com/engine/api/sdk/ and Docker Engine OpenAPI spec
- Redis pub/sub vs Streams semantics (fire-and-forget vs persisted consumer groups, XREADGROUP/XACK/XAUTOCLAIM) (HIGH): https://redis.io/docs/latest/develop/interact/pubsub/ and https://redis.io/docs/latest/develop/data-types/streams/
- Piston (engineer-man) architecture — manifest/package model, pre-built language images, no runtime dep resolution (HIGH, prior art the project explicitly models on): https://github.com/engineer-man/piston

---
*Architecture research for: sandboxed interactive code execution (Piston-style), Go*
*Researched: 2026-06-02*
