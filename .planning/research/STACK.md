# Stack Research

**Domain:** Internal, horizontally-scalable remote code execution / sandbox service (Piston-style) in Go
**Researched:** 2026-06-02
**Confidence:** HIGH (versions verified against Go module proxy + official repos on research date)

> Scope note: this is the **stack dimension only** — specific libraries, versions, rationale, and a "do not use" list. It deliberately prescribes one choice per decision so the roadmap can move directly to implementation.

---

## Recommended Stack

### Core Technologies

| Technology | Version | Purpose | Why Recommended |
|------------|---------|---------|-----------------|
| **Go** | **1.26.x** (toolchain pinned in `go.mod`) | Executor API + worker pool | Required by spec. Use the latest stable (1.26.3 at research date; 1.25.x acceptable LTS-ish fallback). Pin via `go 1.26` + `toolchain go1.26.3` so CI/dev are reproducible. Strong goroutine model fits "one goroutine per live sandbox holding pipes open." |
| **net/http (stdlib) + `chi` v5** | **chi `v5.3.0`** | HTTP router for the internal Executor API | For a small, internal, ~6-route API, stdlib `net/http` with the Go 1.22+ enhanced `ServeMux` (method+path patterns like `POST /run/{jobId}/stdin`) is enough. Add **chi** only for ergonomic middleware (request ID, recoverer, timeout, structured logging) and `chi.URLParam`. chi is `net/http`-native (handlers are plain `http.Handler`), zero lock-in, no custom context type. **Recommend chi** for the middleware stack; you could ship on pure stdlib and not be wrong. |
| **go-redis** | **`github.com/redis/go-redis/v9` v9.20.0** | Redis client: job queue, stdin pub/sub, slot accounting | The de-facto standard, actively maintained, RESP3-native, first-class `PubSub`, `XADD`/`XREAD`, Lua `EVAL`, and connection pooling. v9 is the current major; import path **must** include `/v9`. |
| **Docker Engine SDK (moby)** | **`github.com/docker/docker` v28.5.2+incompatible** (a.k.a. moby client) | Talk to the host container runtime to create/start/attach/kill ephemeral sandboxes | The runtime client lives **behind your `Runner` interface**. The moby client (`client.NewClientWithOpts(client.FromEnv)`) speaks the Docker Engine API over the mounted `/var/run/docker.sock`. It gives you `ContainerCreate` (with `HostConfig` for cgroup limits, seccomp, cap-drop, read-only, tmpfs, `NetworkMode=none`), `ContainerAttach` (hijacked conn for stdin/stdout/stderr pipes), `ContainerKill`, `ContainerRemove`. Selecting `runsc` later is a one-line `HostConfig.Runtime = "runsc"` change. |
| **pusher-http-go** | **`github.com/pusher/pusher-http-go/v5` v5.1.1** | Publish stdout/stderr/result events to soketi over the Pusher HTTP API | Official Pusher server SDK. Point it at soketi by setting `Host` + `Secure:false` on `pusher.Client`. Output-only, exactly matches the trust boundary. **Caveat: last release 2022** (stable, protocol is frozen) — see Version Compatibility. |
| **soketi** | **soketi 1.x** (run as a container image in the dev compose stack) | Pusher-compatible WebSocket server, output channel transport | Implements the Pusher protocol; the Go side only ever calls its HTTP trigger endpoint. Not a Go dependency — it's an infra component pinned by image tag in `docker-compose.yml`. |

### Supporting Libraries

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| **slog (stdlib `log/slog`)** | Go 1.26 stdlib | Structured logging | Default logger across both binaries. No third-party logger needed. |
| **`github.com/google/uuid`** | v1.6.0 | Job IDs / channel suffixes | If TS API doesn't supply IDs; otherwise skip. |
| **`github.com/caarlos0/env` or stdlib `os` + a small loader** | env v11 | 12-factor config (Redis URL, soketi creds, limits) | Stateless replicas configured purely via env. Keep it tiny; don't pull Viper. |
| **`github.com/opencontainers/runtime-spec`** | v1.3.0 | Types for seccomp/OCI when hand-authoring a profile | Only if you build the seccomp profile programmatically; usually you ship a JSON file and pass its path. |
| **`github.com/stretchr/testify`** | v1.10.x | Assertions in the abuse-test suite | Test ergonomics only. |
| **`github.com/redis/go-redis/v9` `redismock`** (`/go-redis/redismock/v9`) | latest | Unit-test Redis interactions without a live server | Pure unit tests; integration tests should hit a real Redis (testcontainers or the compose stack). |
| **`github.com/testcontainers/testcontainers-go`** | v0.x (latest) | Spin up real Redis + sandbox runtime in integration/abuse tests | Optional; the `docker compose` dev stack can double as the integration target. |

### Development Tools

| Tool | Purpose | Notes |
|------|---------|-------|
| **Go workspaces (`go.work`) or single module, multi-`main`** | Multi-binary repo (executor + worker) | **Recommend a single Go module** with `cmd/executor/main.go` and `cmd/worker/main.go` sharing `internal/` packages. Simpler than `go.work` for a tightly coupled repo; `go.work` only earns its keep if executor/worker become separately versioned modules. |
| **`golangci-lint`** | Aggregated linting | Pin version in CI; run `govet`, `staticcheck`, `errcheck`, `gosec` (security linter — relevant given untrusted-code domain). |
| **`docker compose`** | Dev stack: executor + workers + redis + soketi + TS API stub | Required by spec. Workers mount the host Docker socket; do **not** run Docker-in-Docker. |
| **`air` or `wgo`** | Live reload during dev | Optional convenience for the API binary. |
| **`mockgen` / hand-written fakes** | Mock the `Runner` interface in tests | Hand-written fakes are usually cleaner for a 5-method interface; skip codegen unless it grows. |

---

## Installation

```bash
# Initialize module (single module, multi-binary)
go mod init github.com/<org>/code-runner
go mod edit -go=1.26

# Core
go get github.com/redis/go-redis/v9@v9.20.0
go get github.com/docker/docker@v28.5.2+incompatible
go get github.com/pusher/pusher-http-go/v5@v5.1.1
go get github.com/go-chi/chi/v5@v5.3.0

# Supporting
go get github.com/google/uuid@latest
go get github.com/opencontainers/runtime-spec@v1.3.0

# Dev / test
go get github.com/stretchr/testify@latest
go get github.com/go-redis/redismock/v9@latest
go get github.com/testcontainers/testcontainers-go@latest   # optional
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

> The Docker SDK pulls a large transitive tree (moby). That is expected and fine; it is the canonical client.

---

## Decision deep-dives (the questions asked)

### 1. HTTP router — **stdlib `net/http` + chi v5**
- Internal API, ~6 routes, no public exposure, no need for a heavyweight framework.
- **Gin / Echo: do not use here.** They bring their own `Context`, binding, and rendering machinery you don't need for an internal RPC-ish API, and they make the codebase non-`http.Handler`-native (harder to test, more lock-in). Gin v1.12.0 / Echo v4.15.2 are fine libraries — just over-scoped for this service.
- chi keeps handlers as plain `http.Handler`, gives you battle-tested middleware (RequestID, Recoverer, Timeout), and `chi.URLParam(r, "jobId")`. Confidence: **HIGH**.

### 2. Redis client + stdin delivery — **go-redis v9.20.0; pub/sub for MVP, Streams as the upgrade**
- **Pub/sub (`SUBSCRIBE stdin:<jobId>`)**: fire-and-forget, lowest latency, *no persistence*. If the owning worker isn't subscribed at publish time, the message is lost. This is acceptable for MVP because the start-handshake (queued → subscribe → start) guarantees the worker is subscribed before the client can send stdin. Matches the spec's stated MVP choice.
- **Streams (`XADD` + `XREAD BLOCK`/consumer groups)**: persisted, replayable, at-least-once, backpressure-aware (you can cap stream length with `MAXLEN`). The right upgrade when you need *guaranteed* stdin delivery across a worker crash/reconnect, ordered redelivery, or to detect a dropped consumer. Cost: more bookkeeping (offsets, ack, trimming) and slightly higher latency.
- **Recommendation:** ship pub/sub behind a `StdinTransport` interface so swapping to Streams later doesn't touch the worker's pipe-writing code. Confidence: **HIGH**.

### 3. Container runtime client — **Docker Engine SDK (moby) behind a `Runner` interface; NOT the CLI, NOT containerd directly (yet)**
- **moby SDK (recommended):** native Go API over the mounted socket. Clean primitives for the exact lifecycle you need — `ContainerCreate` with full `HostConfig` (Memory + MemorySwap to disable swap, `NanoCPUs`/`CPUQuota`, `PidsLimit`, `ReadonlyRootfs`, `Tmpfs`, `NetworkMode:"none"`, `CapDrop:["ALL"]`, `SecurityOpt:["no-new-privileges","seccomp=<path>"]`, and `Runtime`), `ContainerAttach` (returns a hijacked `net.Conn` you split into stdin/stdout/stderr — stdout/stderr are multiplexed via Docker's stdcopy framing unless TTY), `ContainerWait`, `ContainerKill`, `ContainerRemove(force:true)`.
- **Shelling out to the `docker` CLI: do not use.** Parsing CLI output, managing child processes, and handling attach via subprocess pipes is fragile, slow to fork per call, and hard to get right for the interactive-stdin + multiplexed-stream case. Only acceptable as a last-resort fallback inside one `Runner` implementation.
- **containerd client directly: defer.** `containerd/v2` (v2.3.1) or v1 (v1.7.32) is the more "correct" low-level path and what you'd reach for to drop the Docker daemon entirely — but it's materially more code (snapshotters, content store, task/IO management) and not needed while you're talking to the host Docker socket in dev. Keep it as a *future* `Runner` implementation, not the first one.
- **nerdctl: do not use as a library.** It's a CLI; same objections as shelling out to `docker`.
- **The interface that makes the swap clean:**
  ```go
  type Sandbox interface {
      Stdin() io.WriteCloser
      Stdout() io.Reader
      Stderr() io.Reader
      Wait(ctx context.Context) (exitCode int, err error)
      Kill(ctx context.Context) error
      Remove(ctx context.Context) error
  }
  type Runner interface {
      // Spec carries image, cmd, limits, seccompPath, runtime ("runc"|"runsc"), etc.
      Launch(ctx context.Context, spec SandboxSpec) (Sandbox, error)
  }
  ```
  A `DockerRunner` implements this today; gVisor is just `spec.Runtime = "runsc"`. Confidence: **HIGH**.

### 4. gVisor (runsc) integration — **runtime swap, mostly transparent**
- Install with `runsc install` (writes a `runsc` runtime into `/etc/docker/daemon.json`), restart dockerd, then launch with `--runtime=runsc` — via the SDK that's **`HostConfig.Runtime = "runsc"`**. Nothing else in your create call changes.
- **What stays the same:** image, command/entrypoint, volume/tmpfs mounts, `NetworkMode=none`, `Memory`/`NanoCPUs`/`PidsLimit` cgroup limits, port mapping, cap-drop, attach/stdin semantics. Your `Runner` code is identical apart from the one field.
- **What changes / caveats (flag for a phase):**
  - gVisor's Sentry **re-implements every syscall in userspace** and never passes calls straight to the host. This *is* a syscall-filtering sandbox by design, so a host **seccomp profile is largely redundant** under runsc — runsc applies its own internal seccomp to protect the host from the Sentry. Passing `--security-opt seccomp=...` is generally accepted but the application-visible behavior is governed by the Sentry, not your profile. Keep your custom seccomp profile for the **runc** path; treat it as belt-and-suspenders under runsc.
  - Some syscalls/`/proc` and `/sys` details, certain `ptrace`/perf features, and a few niche kernel interfaces behave differently or are unimplemented under runsc. For Python/R/Rust/SQLite workloads this is almost never an issue, but **validate each language image under runsc before declaring the swap done** — don't assume bit-identical behavior.
  - Performance: extra syscall-interception overhead, especially for I/O-heavy workloads. Acceptable for the security gain; benchmark per language.
  - Confidence on the *integration mechanism*: **HIGH**. Confidence on *per-language behavioral parity*: **MEDIUM** (must be validated empirically).

### 5. soketi publishing — **pusher-http-go/v5 v5.1.1, custom Host**
- `pusher.Client{AppID, Key, Secret, Host:"soketi:6001", Secure:false}` then `client.Trigger("private-run-<jobId>", "stdout", payload)`.
- soketi config basics: set `SOKETI_DEFAULT_APP_ID` / `_APP_KEY` / `_APP_SECRET` (or an apps array) to match the Go client's creds; expose port `6001`. For `private-` channels, **soketi calls back to an auth endpoint** — that's the **TS API's** job (per the trust boundary), not this service. Your Go service only ever *triggers* events; it never authenticates subscribers.
- **Caveat:** v5.1.1 is from Oct 2022. It's stable because the Pusher HTTP trigger protocol is frozen, and it interoperates with soketi fine. But it's effectively in maintenance mode — pin the version, and if it ever blocks you, the trigger endpoint is a single signed `POST /apps/{id}/events` you can re-implement in ~50 lines. Confidence: **HIGH** on it working; **MEDIUM** on long-term maintenance.

### 6. Job queue — **roll your own on Redis Lists/Streams; do NOT adopt asynq/river for this**
- This is the most opinionated call. The queue here is an **execution-slot/backpressure queue for live interactive sessions**, not a generic background-job system. You need: enqueue on `/execute`, a worker `BRPOP`/`XREADGROUP` to claim a job, a **bounded concurrency counter of live sandboxes per worker**, and reject-with-429 when full. That's a thin layer.
- **asynq (`v0.26.0`): not recommended here.** Excellent general distributed task queue (retries, scheduling, dead-letter, web UI), but its model is "process a task to completion" — it doesn't map to *holding a slot open for a long-lived interactive session governed by three independent clocks* with the API needing to push stdin into the still-running task. You'd fight its lifecycle. Use it if you later add genuine fire-and-forget background work (cleanup, metrics rollups).
- **river (`v0.38.0`): wrong backing store.** River is a Postgres-backed queue. The spec mandates Redis; introducing Postgres just for the queue is unjustified. Excellent if you were already on Postgres — you're not.
- **watermill / sarama: over-engineered** for an internal Redis queue; pub/sub abstraction layers you don't need.
- **Recommendation:** `BRPOPLPUSH`/`LMOVE` (reliable list with in-flight list) or a Redis Stream consumer group for the job queue, plus a Lua-script-guarded counter for slot accounting. Keep it behind a `Queue` interface so asynq/Streams can be swapped in if requirements change. Confidence: **HIGH** for the recommendation given these specific constraints.

### 7. seccomp — **start from Docker's default, harden to a custom restrictive profile; apply via `SecurityOpt`**
- **Source:** begin with the **Docker/moby default seccomp profile** (the well-known `default.json` allow-list that already blocks ~44 dangerous syscalls). Fork it into `profiles/seccomp/runner.json` and tighten: drop syscalls no sandbox language needs (e.g. `mount`, `ptrace`, `kexec_load`, `bpf`, `clone` of new namespaces, `keyctl`, `add_key`, `perf_event_open`, `userfaultfd`). Pair with `--cap-drop=ALL` and `no-new-privileges`.
- **Apply via the SDK:** `HostConfig.SecurityOpt = []string{"no-new-privileges", "seccomp=/etc/runner/seccomp.json"}`. (Mount the profile into the worker container or reference a host path the daemon can read — note the **daemon** reads the profile, so the path must be resolvable by dockerd, not just the worker.)
- Validate the profile against each language image (a too-tight profile shows up as `Operation not permitted` / `SIGSYS`). Confidence: **HIGH** on mechanism; **MEDIUM** on the exact deny-list (must be tuned per language, expect iteration).

### 8. Abuse-test tooling — **plain Go test suite, no special framework**
- These are **integration tests** that launch real sandboxes through the `Runner` and assert on the kill/clean-up behavior. Use `go test` + `testify`, optionally `testcontainers-go` or the compose stack as the target.
- Coverage maps directly to the spec's three clocks + hardening:
  - **Fork bomb** `:(){ :|:& };:` → asserts `PidsLimit` + cgroup kill.
  - **OOM** (allocate > memory cap) → asserts no-swap memory cap triggers OOM-kill, sandbox removed.
  - **Infinite loop** (`while True: pass`) → asserts **wall-clock** and **CPU (cgroup)** clocks fire.
  - **Idle / stdin-blocked** (`input()` with no input) → asserts **idle** clock fires.
  - **EOF** (stdin/close) → asserts process gets EOF and exits cleanly, slot freed.
  - **Giant output** (`while True: print('x'*4096)`) → asserts output byte-cap truncation (`truncated=true`) and that the publisher/back-pressure doesn't OOM the worker.
- No third-party "chaos" tool needed; the abuse cases *are* the test fixtures. Confidence: **HIGH**.

### 9. Go version + repo layout
- **Go 1.26.x**, pinned via `go.mod` `go 1.26` + `toolchain go1.26.3`.
- **Single module, multi-binary, standard layout:**
  ```
  code-runner/
    go.mod
    cmd/
      executor/main.go      # the private HTTP API
      worker/main.go        # the stateless sandbox runner
    internal/
      api/                  # http handlers, chi router, middleware
      queue/                # Redis job queue + slot accounting (interface + impl)
      stdin/                # Redis stdin transport (pub/sub now, streams later)
      runner/               # Runner/Sandbox interfaces + docker impl (+ future runsc/containerd)
      publisher/            # pusher-http-go wrapper -> soketi
      manifest/             # manifest.json loader (Piston model)
      session/              # lifecycle, three clocks, cleanup
      config/
    profiles/seccomp/runner.json
    languages/<lang-version>/{manifest.json, Dockerfile}
    deploy/docker-compose.yml
  ```
- **Build/dev:** `Makefile` or `Taskfile` with `build` (both binaries), `lint` (golangci-lint), `test`, `compose-up`. Multi-stage `Dockerfile` per binary (distroless/static final image). Confidence: **HIGH**.

---

## Alternatives Considered

| Recommended | Alternative | When to Use Alternative |
|-------------|-------------|-------------------------|
| stdlib + chi | Gin / Echo | If this grew into a large public API with heavy binding/validation/rendering needs — not this internal service. |
| Roll-your-own Redis queue | asynq v0.26.0 | If you add real fire-and-forget background jobs (cleanup, scheduled rollups) needing retries/DLQ/web UI. |
| Roll-your-own Redis queue | river v0.38.0 | Only if the system were already Postgres-backed. |
| Redis pub/sub for stdin | Redis Streams (XREAD BLOCK) | When you need guaranteed/at-least-once stdin delivery surviving worker reconnects — the planned upgrade. |
| moby Docker SDK | containerd/v2 client (v2.3.1) | When you drop the Docker daemon entirely and manage tasks/snapshots directly — a later `Runner` impl. |
| moby Docker SDK | runc/runsc OCI spec directly | Maximum control, but far more plumbing; unjustified while using the host Docker socket. |
| Docker default→custom seccomp | gVisor-only isolation | runsc reduces seccomp's relevance, but seccomp stays valuable on the runc path; keep both. |

## What NOT to Use

| Avoid | Why | Use Instead |
|-------|-----|-------------|
| Shelling out to `docker` CLI / `nerdctl` | Per-call fork cost, fragile output parsing, painful interactive attach + multiplexed stream handling | moby Docker SDK (`ContainerAttach`) |
| Docker-in-Docker | Explicitly out of scope; privileged, slow, security-hostile for untrusted code | Mount the host Docker socket into the worker (dev) |
| Gin / Echo for this API | Over-scoped; non-`http.Handler` context lock-in for a 6-route internal API | stdlib `net/http` + chi v5 |
| asynq / river as the execution queue | Built for run-to-completion background jobs, not long-lived interactive slot-holding sessions; river needs Postgres | Thin Redis List/Streams queue behind a `Queue` interface |
| go-redis **v8** or `gomodule/redigo` | v8 is the old major (import without `/v9`); redigo is lower-level and less maintained | `github.com/redis/go-redis/v9` |
| Viper for config | Heavy for a 12-factor stateless service | env vars + a tiny loader (e.g. `caarlos0/env`) |
| Disabling seccomp (`seccomp=unconfined`) "to make it work" | Removes a core defense layer for untrusted code | Fork Docker's default profile and tighten per language |
| Re-implementing the Pusher protocol from scratch up front | Wasted effort; SDK works against soketi | pusher-http-go/v5 (re-implement only the trigger POST if it ever blocks you) |

## Stack Patterns by Variant

**If you need guaranteed stdin delivery (post-MVP):**
- Swap the `StdinTransport` impl from pub/sub to Redis Streams (`XADD` + consumer group `XREADGROUP`, `XACK`, `MAXLEN` trim).
- Because pub/sub silently drops messages with no live subscriber; Streams persist + redeliver.

**If you later drop the Docker daemon:**
- Add a `containerd/v2` (v2.3.1) `Runner` implementation behind the existing interface.
- Because talking to containerd directly removes the dockerd dependency for production density, at the cost of managing snapshots/tasks yourself.

**If gVisor becomes the default runtime:**
- Set `HostConfig.Runtime="runsc"` per launch; keep the seccomp profile for defense-in-depth but expect the Sentry to govern actual behavior.
- Because runsc's userspace syscall layer supersedes host seccomp for application-visible semantics — validate each language image under runsc first.

**If real background jobs appear (cleanup, metrics):**
- Introduce asynq v0.26.0 alongside (not replacing) the execution queue.
- Because that's the workload asynq is actually good at.

## Version Compatibility

| Package | Compatible With | Notes |
|---------|-----------------|-------|
| `redis/go-redis/v9` v9.20.0 | Redis 6/7/8, RESP2 & RESP3 | Import path **must** end in `/v9`. Pub/Sub + Streams both first-class. |
| `docker/docker` v28.5.2+incompatible | Docker Engine API; dockerd ≥ matching API version | The `+incompatible` suffix is normal (moby pre-dates Go modules versioning) — not a problem. `client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())` to auto-match the daemon. |
| `pusher/pusher-http-go/v5` v5.1.1 (2022) | soketi 1.x, Pusher HTTP protocol | Stable but maintenance-mode; the trigger protocol is frozen so interop is safe. Pin the exact version. |
| `go-chi/chi/v5` v5.3.0 | Go ≥ 1.21, `net/http` | Handlers are plain `http.Handler`; no version coupling to other deps. |
| gVisor `runsc` | Docker Engine (registered runtime) | Requires `runsc install` + dockerd restart on the host; not a Go dependency. Validate per-language behavior. |
| Go 1.26.x | All of the above | Pin `toolchain` in `go.mod` for reproducible builds. 1.25.x is an acceptable fallback. |

## Sources

- Go module proxy (`proxy.golang.org/.../@latest`) — verified exact current versions on 2026-06-02: go-redis v9.20.0, asynq v0.26.0, pusher-http-go/v5 v5.1.1, docker/docker v28.5.2+incompatible, chi v5.3.0, river v0.38.0, containerd/v2 v2.3.1, containerd v1.7.32, gin v1.12.0, echo v4.15.2, runtime-spec v1.3.0 — **HIGH**
- go.dev/dl JSON — latest Go stable go1.26.3 (and go1.25.10) — **HIGH**
- gVisor official docs (Docker quick start + security/architecture guide) — runsc install/runtime model, Sentry intercepts all syscalls, `--runtime=runsc` is the only required change — **HIGH** on mechanism, **MEDIUM** on per-language parity (needs empirical validation)
- pusher-http-go GitHub README — custom `Host`/`Secure:false` for self-hosted (soketi), `Trigger(channel,event,data)` signature, `/v5` import path — **HIGH**
- Context7 library resolution (go-redis `/redis/go-redis`, asynq `/hibiken/asynq`, pusher `/websites/pusher`) — confirmed canonical, high-reputation libraries — **HIGH**
- Docker/moby Engine SDK + OCI runtime-spec knowledge (HostConfig fields: Memory, NanoCPUs, PidsLimit, ReadonlyRootfs, Tmpfs, NetworkMode, CapDrop, SecurityOpt, Runtime; ContainerAttach hijack + stdcopy) — cross-checked against training + SDK shape — **MEDIUM/HIGH** (verify exact field names against the v28 SDK godoc at implementation time)

---
*Stack research for: internal Go remote code execution / sandbox service (Piston-style)*
*Researched: 2026-06-02*
