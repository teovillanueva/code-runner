# Stack Supplement — Hono API + Shared Contract + Deployment Targets

> **This file SUPPLEMENTS `STACK.md`.** It covers ONLY the pieces introduced by the pivot: the TypeScript **Hono** HTTP gateway, the **shared wire contract** codegen seam (JSON Schema → TS + Go), and the **deployment targets / Runner backends** (Fly.io Machines, Upstash, k8s gVisor, scale-to-zero). **`STACK.md` remains authoritative for the Go worker internals** (Docker SDK, go-redis, pusher-http-go, manifest-driven languages, three clocks, seccomp). Nothing here re-litigates those.

**Researched:** 2026-06-02
**Versions verified against:** npm registry, Go module proxy (`proxy.golang.org`), and official docs on the research date — not training data.
**Overall confidence:** HIGH on versions and the two flagged risk verdicts (Upstash pub/sub, Fly runner model); MEDIUM where empirical validation is still required (noted inline).

---

## 0. Headline verdicts (read these first)

1. **Upstash Redis cannot serve the Go worker's persistent blocking SUBSCRIBE / BRPOP / XREAD BLOCK over TCP.** Confirmed against Upstash's own compatibility docs: blocking list commands and blocking `XREAD`/`XREADGROUP` are **explicitly unsupported**, and pub/sub is **REST/SSE only**. **Verdict:** Upstash is fine for the **Hono API** (which only `PUBLISH`es + reads hashes — all non-blocking) but is **not** a drop-in for the worker's stdin transport or its blocking queue claim. This forces a decision at the prod target — see §3.2 for the three options and the recommendation. **Confidence: HIGH.**

2. **Fly.io Machines as a sandbox backend = the Go worker calls the Fly Machines REST API to create an ephemeral Machine per execution (a `FlyMachinesRunner`).** This is the clean mapping to the existing `Runner` interface. The alternative ("worker runs *inside* a Fly Machine and uses a local Docker/gVisor runtime") is a *deployment topology*, not a Runner backend, and it does **not** give you Firecracker-per-execution isolation. Recommend the API-driven `FlyMachinesRunner`; flag the open question in §3.1. **Confidence: HIGH on the model, MEDIUM on the per-execution latency/cost being acceptable — must benchmark.**

3. **Contract codegen: JSON Schema as the single source is the right call.** Generate TS with `json-schema-to-typescript` and Go structs with **`omissis/go-jsonschema`** (the maintained successor to `atombender/go-jsonschema` — same project). Drift check = regenerate in CI and `git diff --exit-code`. protobuf would only have been better if you needed binary wire framing or gRPC streaming — you don't; the wire is JSON over Redis. **Confidence: HIGH.**

---

## 1. Hono API Gateway (TypeScript)

The Hono app is a **thin, trusted gateway**: bearer auth, validate, enqueue to Redis, `PUBLISH` stdin, read job status. It **never** calls the worker — coupling is Redis-only. Keep it small.

### 1.1 Hono version + runtime

| Decision | Recommendation | Version (verified) | Confidence |
|----------|----------------|--------------------|------------|
| Framework | **Hono** | **4.12.23** (npm `hono@latest`) | HIGH |
| Runtime | **Node.js via `@hono/node-server`** | **`@hono/node-server` 2.0.4** | HIGH |

**Runtime rationale (Node over Bun):** This is **self-hostable OSS**. The lowest-friction, most-portable target for someone running `docker compose up` or deploying to an arbitrary box is Node — `node:22-slim` / `node:24-slim` is universally available, ops teams know it, and `@hono/node-server` is a first-class adapter. Bun is faster and has a nicer single-binary story, but it adds a non-standard runtime dependency for self-hosters and a second toolchain to support, for a gateway whose hot path is "parse small JSON → one Redis round-trip." The gateway is **not** CPU-bound; runtime micro-perf is irrelevant here. **Ship Node; document Bun as an optional swap** (Hono runs unmodified on Bun via `Bun.serve`, so you lose nothing by starting on Node). Pin a Node LTS (22.x or 24.x) in `.nvmrc` and the Docker base image.

- **Do NOT** reach for Deno here — extra ecosystem friction for self-hosters with no payoff for this workload.
- **Do NOT** put the gateway on an edge runtime (Cloudflare Workers / Vercel Edge). It needs a normal TCP Redis client and lives on the private network next to Redis; edge isolates are the wrong shape and reintroduce the Upstash-REST constraint you don't want on this side.

### 1.2 Request validation — `@hono/zod-validator` + Zod, types DERIVED from the contract

| Decision | Recommendation | Version (verified) | Confidence |
|----------|----------------|--------------------|------------|
| Validator middleware | **`@hono/zod-validator`** | **0.8.0** | HIGH |
| Schema library | **Zod** | **4.4.3** | HIGH |

**The hard rule: validators must AGREE with `packages/contract`, not redefine it.** The contract's single source of truth is JSON Schema (see §2). So the Zod schemas the API validates against must be **derived from** that JSON Schema, not hand-written a second time. Two acceptable mechanisms — pick **one** and make it the only path:

- **(Recommended) Generate Zod from the contract JSON Schema** using `json-schema-to-zod` (**2.8.1**) into `packages/contract/generated/zod.ts`, and have `@hono/zod-validator` consume those generated schemas. The TS *types* come from `json-schema-to-typescript`; the *runtime validators* come from `json-schema-to-zod`. Both are generated from the same `.schema.json`, so they cannot drift from the wire contract, and the drift check (§2.3) covers them.
- **(Alternative) Validate directly against the JSON Schema** with `@hono/standard-validator` (**0.2.2**) backed by a JSON-Schema Standard-Schema adapter, skipping Zod entirely. This is the *purest* "single source" approach (you validate against the exact contract artifact, no Zod regeneration step) but the JSON-Schema-as-Standard-Schema tooling is younger and the error-message ergonomics are worse than Zod's. **Choose this only if you want zero TS-side schema generation;** otherwise the generated-Zod path has better DX.

**Verdict:** generated-Zod + `@hono/zod-validator`. It gives you Hono-native `c.req.valid('json')` typing, the best error messages, and — critically — the schemas are *generated from the contract*, so "the validator and the wire format agree" is enforced by the build, not by discipline.

- **Do NOT use TypeBox here.** TypeBox (`@sinclair/typebox` 0.34.49) is excellent when TypeBox *is* your source of truth and you emit JSON Schema *from* it. But your source of truth is the language-neutral JSON Schema file (because Go also consumes it). Making TypeBox authoritative would put the canonical contract inside the TS package, which is exactly the coupling the poliglota seam must avoid. Generate *from* the schema instead.
- **Do NOT hand-write Zod schemas** that mirror the contract. That is a second source of truth and the #1 way the API and worker silently diverge.

### 1.3 Bearer-token auth with constant-time comparison

Authenticate the TS-frontend-or-caller → Hono boundary with a shared secret `EXECUTOR_API_TOKEN`. Use a **length-safe, constant-time** comparison — a naive `===` short-circuits on the first differing byte and leaks token length/prefix via timing.

Correct Node pattern (works under `@hono/node-server`; uses stdlib `node:crypto`):

```ts
import { timingSafeEqual, createHash } from "node:crypto";
import { createMiddleware } from "hono/factory";

const EXPECTED = process.env.EXECUTOR_API_TOKEN!;

// Hash both sides to a FIXED 32-byte length first. timingSafeEqual throws if the
// two buffers differ in length, and that length check itself is non-constant-time,
// so comparing raw tokens of differing length both throws AND leaks length. Hashing
// normalizes length and removes the early-exit length leak.
function safeEqual(a: string, b: string): boolean {
  const ha = createHash("sha256").update(a).digest(); // 32 bytes
  const hb = createHash("sha256").update(b).digest(); // 32 bytes
  return timingSafeEqual(ha, hb);
}

export const bearerAuth = createMiddleware(async (c, next) => {
  const header = c.req.header("authorization") ?? "";
  const token = header.startsWith("Bearer ") ? header.slice(7) : "";
  if (!token || !safeEqual(token, EXPECTED)) {
    return c.json({ error: "unauthorized" }, 401);
  }
  await next();
});
```

- The sha256-then-`timingSafeEqual` idiom is the standard length-safe pattern: `timingSafeEqual` requires equal-length buffers, and hashing both inputs guarantees that without branching on the attacker-controlled length.
- Hono ships a built-in `bearerAuth` middleware (`hono/bearer-auth`) that **already uses `timingSafeEqual` internally** — prefer it unless you need custom behavior; it saves you owning the crypto. Either way, the requirement (constant-time, length-safe) is satisfied. **Confidence: HIGH.**
- **Do NOT** compare with `===`, `==`, `Buffer.compare`, or `a.localeCompare(b)` — all short-circuit. **Do NOT** `timingSafeEqual` two raw tokens of attacker-controlled length without hashing first.

### 1.4 Redis client from TS — `ioredis`

| Decision | Recommendation | Version (verified) | Confidence |
|----------|----------------|--------------------|------------|
| Redis client | **`ioredis`** | **5.11.0** | HIGH |

**What the API actually does with Redis:** `LPUSH`/`XADD` (enqueue), `PUBLISH stdin:<id>` (route stdin), `HGET`/`HGETALL job:<id>:meta` (read status). **All non-blocking. The API never `SUBSCRIBE`s** — the worker is the only subscriber (ownership-by-subscription, per `ARCHITECTURE.md` Pattern 2). This matters enormously for the Upstash story (below).

**ioredis vs node-redis:**
- **`ioredis` 5.11.0 — recommend.** Mature, ergonomic, robust auto-reconnect, built-in `Cluster`, trivial Upstash compatibility (just a TLS `rediss://` URL). Battle-tested as the default in most TS Redis codebases. For a gateway doing simple non-blocking commands, it's the safe, boring, correct choice.
- **`node-redis` — note the version churn.** npm `redis@latest` is now **6.0.0**, a major release that makes **RESP3 the default** and bumps the minimum Node version. The question framed this as "node-redis **v4**" — that's stale; v4→v5→v6 happened. node-redis v6 is fine, but the RESP3-default change is a migration footgun and offers you nothing over ioredis for this gateway. **Stick with ioredis** and avoid the moving target.

**Upstash compatibility for the API side — GREEN.** The API only issues non-blocking commands (`LPUSH`/`XADD`/`PUBLISH`/`HGET`). Upstash supports all of these over both its native TCP/RESP endpoint and REST. So the Hono gateway can point `ioredis` at an Upstash `rediss://` URL with **zero** caveats. The Upstash limitations (no blocking SUBSCRIBE/BRPOP/XREAD BLOCK) bite **only the worker**, not the API — see §3.2. **Confidence: HIGH.**

> Note the asymmetry the architecture buys you: because the API only PUBLISHes and the worker is the only SUBSCRIBEr, **the API is Upstash-safe even when the worker is not.** Don't let the worker's Upstash problem scare you off Upstash for the gateway.

### 1.5 Optional soketi private-channel auth helper (keep NON-core)

The trust model (`ARCHITECTURE.md`) says **channel authorization is the upstream app's job**, not this service's. But because this is now OSS and people will want a turnkey demo, you may ship an **optional** helper endpoint that authorizes a soketi `private-run-<jobId>` subscription.

- **Mechanism:** A Pusher private-channel auth is `HMAC-SHA256(secret, "<socket_id>:<channel_name>")`, returned as `"<app_key>:<hmac>"`. The official **`pusher`** npm server SDK (**5.3.3**, verified) does this for you: `pusher.authorizeChannel(socketId, channel)`.
- **Keep it isolated:** put it behind a clearly optional route/flag, document that production deployments should authorize channels in *their own* app, and make sure the soketi app secret lives only in this helper's env, never on the worker. The worker (per `STACK.md`) only *triggers* events via `pusher-http-go`; it never authorizes subscribers. **Do NOT** make the helper a hard dependency of the core flow. **Confidence: HIGH.**

### 1.6 stdin rate-limit + pending-byte cap → 429

Two independent guards at the Hono layer, both returning **429** (matches the `/execute` queue-depth 429 already in `ARCHITECTURE.md` Pattern 5):

1. **Rate limit** stdin frames per job (e.g. N frames/sec). Use **`hono-rate-limiter`** (**0.5.3**) with a **Redis store** keyed by `jobId` so the limit holds across API replicas (the gateway is stateless/horizontally scaled — an in-memory limiter would be per-replica and wrong). A per-`jobId` Redis counter with `INCR`+`EXPIRE` (or a token bucket via Lua) is the backing.
2. **Pending-byte cap** (backpressure): track in-flight, un-consumed stdin bytes per job and reject when over the cap. Maintain a Redis counter `job:<id>:stdin_pending` (`INCRBY` on publish; the worker `DECRBY`s as it drains, or you cap on a sliding window). Over cap → 429. This is the API-layer half of the pending-stdin backpressure that `PROJECT.md` requires.

**Confidence: HIGH** on the approach; **MEDIUM** on exact thresholds (tune empirically — they depend on language REPL behavior and soketi flush window).

### 1.7 Hono gateway — install summary

```bash
npm install hono @hono/node-server @hono/zod-validator zod ioredis hono-rate-limiter
npm install pusher                 # OPTIONAL: only if shipping the channel-auth helper
npm install -D json-schema-to-typescript json-schema-to-zod
# pin Node LTS (22.x or 24.x) in .nvmrc + Docker base image
```

### 1.8 API "do NOT use" list

| Avoid | Why | Use instead |
|-------|-----|-------------|
| Bun/Deno as the *required* runtime | Adds non-standard runtime dep for self-hosters; gateway isn't CPU-bound | Node + `@hono/node-server` (Bun as optional swap) |
| Edge runtime (CF Workers / Vercel Edge) for the gateway | Wrong shape; needs TCP Redis on the private net; reintroduces REST-only constraint | Long-running Node process |
| Hand-written Zod mirroring the contract | Second source of truth → silent drift | Zod generated from the contract schema |
| TypeBox as the contract source of truth | Puts the canonical wire format inside the TS package; Go can't consume it cleanly | JSON Schema file as source; generate TS+Zod from it |
| `node-redis` v6 churn / RESP3-default footguns | No benefit over ioredis for a non-blocking gateway | `ioredis` 5.11.0 |
| `===` / `Buffer.compare` for the token | Short-circuits → timing leak | sha256 + `crypto.timingSafeEqual` (or `hono/bearer-auth`) |
| In-memory rate limiter | Per-replica, breaks under horizontal scale | Redis-backed `hono-rate-limiter` keyed by jobId |

---

## 2. Shared wire contract codegen (the fragile poliglota seam)

**Single source of truth = JSON Schema files in `packages/contract/`.** TS types + Zod validators + Go structs are all **generated** from them. The contract is the wire JSON spoken between the TS API and the Go worker over Redis (jobs, stdin, control, output events). This seam is where the two languages can silently diverge, so codegen + a CI drift check is mandatory.

### 2.1 JSON Schema → TypeScript types

| Decision | Recommendation | Version (verified) | Confidence |
|----------|----------------|--------------------|------------|
| TS type generator | **`json-schema-to-typescript`** | **15.0.4** | HIGH |

The de-facto standard. Emits clean `interface`/`type` declarations, `$ref` resolution, `additionalProperties` handling, enums as unions, JSDoc from `description`. Pair with `json-schema-to-zod` (§1.2) for the runtime validators. **Do NOT** use `quicktype` for the TS side just to "match" the Go side — `json-schema-to-typescript` produces idiomatic TS; quicktype's TS output is noisier. Keep the TS and Go generators independent; the **schema** is what they share, not the generator.

### 2.2 JSON Schema → Go structs — `omissis/go-jsonschema`

| Tool | Verified state | Verdict |
|------|----------------|---------|
| **`omissis/go-jsonschema`** | **v0.23.1** (Go proxy, 2026-05-09) | **RECOMMEND** |
| `atombender/go-jsonschema` | v0.23.0 (2026-03-28) | **Same project — atombender is the original, `omissis` is the maintained fork/successor.** The import path/module is now `github.com/omissis/go-jsonschema`; the binary is `go-jsonschema`. Use omissis. |
| `quicktype` (npm 23.2.6) | Active | Reject for Go: cross-language convenience, but Go output is less idiomatic (extra helper scaffolding, `UnmarshalJSON` boilerplate, weaker json-tag fidelity) and it's a Node tool in your Go build. |
| `steel/json-schema-to-go` | Not found as a Go module (likely a one-off/abandoned repo) | Reject — not maintained/installable. |

**Why `omissis/go-jsonschema`:** it produces **clean, idiomatic Go structs with correct `json:"..."` tags**, honors `required` vs optional (pointer fields / `,omitempty`), supports `$ref`, generates typed enum constants, and is purpose-built for "JSON Schema → Go types" (not a general transpiler). It's the natural Go counterpart to `json-schema-to-typescript`. Run it via `go run github.com/omissis/go-jsonschema/cmd/gojsonschema` (or `go-jsonschema`) in a `go:generate` directive / Makefile target. **Confidence: HIGH.**

> Caveat to validate empirically (MEDIUM): JSON Schema features like `oneOf`/discriminated unions and `additionalProperties: true` map awkwardly into Go's type system. Keep the wire schemas **simple and closed** (see §2.5 modeling guidance) — favor a flat tagged-union shape with a `type` discriminator field rather than schema `oneOf` gymnastics, so both generators emit clean output.

### 2.3 Drift detection (CI / make check)

The check that makes the seam safe:

```makefile
# make contract        -> regenerate all artifacts from packages/contract/*.schema.json
# make contract-check  -> regenerate into a temp/working tree, fail if anything changed
contract:
	pnpm json2ts -i 'packages/contract/*.schema.json' -o packages/contract/generated/ts/
	pnpm json-schema-to-zod -i ... -o packages/contract/generated/zod.ts
	go run github.com/omissis/go-jsonschema/cmd/gojsonschema \
	    -p contract packages/contract/*.schema.json > apps/worker/internal/contract/types.go
	gofmt -w apps/worker/internal/contract/types.go

contract-check: contract
	git diff --exit-code -- packages/contract/generated apps/worker/internal/contract \
	  || (echo "Contract drift: regenerated output differs. Run 'make contract' and commit." && exit 1)
```

Wire `contract-check` into CI (and ideally a pre-commit hook). The generated files **are committed** (so reviewers see wire changes in diffs and the build doesn't depend on codegen succeeding), and CI proves they're up to date. A schema change that isn't regenerated, or a hand-edit of generated code, fails the build. **Confidence: HIGH.**

### 2.4 Why JSON Schema codegen (and when protobuf would've been better)

| Alternative | Why rejected here |
|-------------|-------------------|
| **TypeSpec** | A nicer authoring language that *emits* JSON Schema/OpenAPI — but it's another layer and toolchain to learn for a handful of small messages, and you'd still generate JSON Schema underneath. Adopt later only if the contract surface grows large. Overkill now. |
| **protobuf / buf** | Would shine if the wire were **binary** or you wanted **gRPC streaming** with generated clients on both ends. But your wire is **JSON over Redis** (lists/streams + pub/sub) — human-debuggable in `redis-cli`, no binary framing, no RPC. protobuf-over-Redis-as-JSON loses protobuf's advantages and adds `.proto` + buf tooling. **protobuf would have been the right call if you'd chosen a direct gRPC stream between API and worker instead of Redis decoupling** — you deliberately didn't (Redis is the only coupling). |
| **Hand-written canonical doc** (a `.md` both sides implement by hand) | The classic drift generator. No machine enforcement; the TS and Go shapes diverge the first time someone edits one side. This is precisely the failure the codegen seam exists to prevent. |

**JSON Schema codegen wins because:** the wire is already JSON, JSON Schema is the natural language-neutral description of JSON, both ecosystems have mature, idiomatic generators (`json-schema-to-typescript`, `omissis/go-jsonschema`), and the artifact is human-readable for debugging Redis payloads. **Confidence: HIGH.**

### 2.5 How to model the wire messages

Model each message as a **closed object with a discriminator field**, kept simple enough that both generators emit clean types. Group into the natural channels from `ARCHITECTURE.md`:

- **Job spec** (`jobs` list/stream, API → worker): `{ jobId, language, version, code, limits{ wallMs, idleMs, cpuMs, memoryMb, pids, outputKb }, interactive }`. Mirrors the manifest `defaultLimits`; the API may override within bounds.
- **Stdin chunk** (`stdin:<jobId>` pub/sub, API → worker): `{ seq, data }` (data base64 or UTF-8 string). Keep tiny; high frequency.
- **Control** (`ctrl:<jobId>` pub/sub, API → worker): a tagged union on `type` — `{ type: "start" }`, `{ type: "kill" }`, `{ type: "stdin-close" }` (EOF). One schema, `type` discriminator, optional per-type payload. **Prefer this flat discriminated shape over schema `oneOf`** so Go codegen stays clean.
- **Output events** (soketi channel `private-run-<jobId>`, worker → client): match the event shapes already in `ARCHITECTURE.md` — `stage {stage}`, `stdout {seq,chunk,truncated}`, `stderr {seq,chunk,truncated}`, `result {exitCode,reason,truncated}` where `reason ∈ exit|wall|idle|cpu|oom|killed`. These are produced by the worker but **belong in the contract** so the TS side (and any client typing the helper ships) stays in lockstep.

**Discriminator discipline:** every union (control messages, output events) uses a single string `type`/`event` field with a closed `enum`. This is the shape that round-trips cleanly through *both* `json-schema-to-typescript` (→ string-literal unions) and `omissis/go-jsonschema` (→ typed const enums), and it's the cheapest thing to keep stable. **Confidence: HIGH.**

---

## 3. Deployment targets + Runner backends

Dev = `docker compose` (already specified in `STACK.md`/`ARCHITECTURE.md` — unchanged). This section covers the **prod-recommended** targets and how each sandbox backend maps to the existing `Runner` interface (`Create → Sandbox{ Start, Stdin, Stdout, Stderr, Stats, Wait, Kill, Cleanup }`).

### 3.1 Fly.io Machines as a Firecracker sandbox backend — `FlyMachinesRunner`

**The model (resolved):** there are two distinct things people mean by "Fly + sandboxes," and only one of them is a Runner backend:

| Interpretation | What it is | Verdict |
|----------------|------------|---------|
| **(A) Worker calls the Fly Machines REST API to create an ephemeral Machine per execution** | A new `Runner` implementation (`FlyMachinesRunner`): `Create()` → `POST /apps/<app>/machines` (Firecracker microVM from your language OCI image), attach via the Machine's exec/stream API, `Kill`→stop, `Cleanup`→`DELETE` the machine. **Firecracker isolation per execution.** | **RECOMMENDED mapping.** This is what gives you "Firecracker microVM per sandbox" — the actual security upgrade over a shared Docker socket. |
| **(B) Worker runs *inside* a Fly Machine and uses a local runtime (Docker socket / gVisor)** | A *deployment topology*, not a Runner backend. The worker is just hosted on Fly; sandboxes are still local containers inside that one VM. | This is **fine as a way to host the existing `DockerRunner`/`gVisorRunner`**, but it does **not** add Firecracker-per-execution isolation, and Fly Machines don't expose a nested Docker socket by default. Don't confuse it with (A). |

**Recommendation:** implement **(A) `FlyMachinesRunner`** as a peer to `DockerSocketRunner` and the future `gVisorRunner`, behind the **same** `Runner` interface. Use the official Go SDK **`github.com/superfly/fly-go` v0.5.6** (verified on the Go proxy, 2026-05-27) for the Machines API. The mapping:

- `Create(spec)` → create a Machine from the language image with `restart.policy=no`, resource limits set via the Machine `guest` config (cpus/memory_mb), `network` restricted; do **not** start the entrypoint yet (honor the two-phase handshake — create stopped or create-then-hold).
- `Start()` → start the Machine / exec the entrypoint.
- `Stdin/Stdout/Stderr` → attach to the Machine's process streams.
- `Stats()` → CPU clock via Fly metrics or in-VM cgroup read.
- `Kill()` → stop the Machine; `Cleanup()` → `DELETE` the Machine (ephemeral, scale-to-zero billing — you pay only while it runs).

**OPEN QUESTION to flag for a phase (MEDIUM confidence):** the interactive-stdin streaming ergonomics differ from the Docker `ContainerAttach` hijacked-conn model. The Machines API's exec/stream surface for a **long-lived interactive process with bidirectional stdin** is less battle-tested than Docker attach, and per-execution Machine create latency (hundreds of ms) plus billing granularity must be **benchmarked against the interactive-session model** (a session holds a Machine for its whole life — that's the cost unit). Validate: (1) can you keep stdin open and stream stdout in real time through the Machines API the way the three-clocks session needs; (2) is create+destroy-per-session latency/cost acceptable. The `Runner` interface makes this swappable, so ship `DockerSocketRunner` first and treat `FlyMachinesRunner` as a parallel backend to validate, not a blocker. **Confidence: HIGH on the architectural mapping; MEDIUM on interactive-streaming fit and cost — must benchmark.**

### 3.2 Upstash Redis + the Go worker's persistent SUBSCRIBE — the real gotcha

**Verdict (HIGH confidence): Upstash does NOT support native TCP blocking SUBSCRIBE / BRPOP / XREAD BLOCK.** Verified against Upstash's own REST/compatibility documentation:

- **Pub/Sub `SUBSCRIBE` is REST/SSE only** — there is no native-protocol persistent blocking SUBSCRIBE. The subscribe endpoint is HTTP Server-Sent-Events.
- **Blocking list commands `BLPOP`/`BRPOP`/`BRPOPLPUSH` are explicitly unsupported.**
- **Blocking `XREAD`/`XREADGROUP` (`BLOCK`) are explicitly unsupported** (non-blocking XREAD is fine).

This breaks **three** things the **worker** does (none of which the API does — see §1.4):
1. The worker `SUBSCRIBE`s to `stdin:<jobId>` / `ctrl:<jobId>` (ownership-by-subscription, the core trick).
2. The worker `BRPOP`s/`BRPOPLPUSH`s to claim jobs.
3. The future Streams upgrade relies on `XREAD BLOCK` / `XREADGROUP BLOCK`.

**Therefore Upstash cannot be the worker's Redis if you keep the pub/sub + blocking-claim design.** Three options:

| Option | What it means | When to choose |
|--------|---------------|----------------|
| **(1) Split Redis (RECOMMENDED for the Fly prod target as stated)** | Worker → a **native-TCP Redis** that supports blocking SUBSCRIBE/BRPOP/XREAD (e.g. Fly's own Redis/Valkey, a self-hosted Redis on Fly, or any managed native-protocol Redis). API → can still use Upstash *or* the same native Redis. But running a real Redis on Fly means **Upstash buys you little** — so in practice this collapses to "use a native-TCP Redis for everything." | If you want the existing pub/sub + blocking design unchanged. **This is the clean recommendation: drop Upstash for this service and run a native-protocol Redis/Valkey** that both API and worker share. Upstash's value is serverless/edge connection-pooling, which this long-lived worker fleet doesn't need. |
| **(2) Use Upstash REST SSE for subscribe in the worker** | Replace the worker's native SUBSCRIBE with Upstash's `/subscribe` SSE endpoint and poll-based (non-blocking) claim. | Only if you're *committed* to Upstash. Costs you the elegant native ownership-by-subscription model and adds HTTP-streaming reconnect logic + non-blocking poll loops. **Not recommended** — it fights the architecture. |
| **(3) Move stdin/queue to Redis Streams sooner with NON-blocking XREAD + short poll** | Streams `XREAD` (no BLOCK) in a tight poll loop works on Upstash. | A middle path, but it forces the Streams upgrade earlier than `PROJECT.md` planned and adds polling latency to interactive stdin. Only if Upstash is mandated. |

**Bottom line:** the milestone context lists "Upstash Redis" as the prod recommendation, but **that recommendation only holds for the API side**. For the **worker**, Upstash's lack of blocking SUBSCRIBE/BRPOP/XREAD BLOCK is disqualifying for the current design. **Recommend running a single native-protocol Redis/Valkey (e.g. on Fly) that both API and worker share, and dropping Upstash** unless a concrete serverless/edge constraint reappears. If Upstash must stay, it forces Redis Streams + non-blocking `XREAD` polling for the worker sooner (Option 3) — exactly the "this forces Streams sooner" risk the question anticipated. **Confidence: HIGH on the limitation and the verdict.**

### 3.3 k8s `RuntimeClass=gvisor` — `gVisorRunner`

Maps to the `Runner` interface essentially the same way `STACK.md §4` describes for Docker `Runtime="runsc"`, but expressed as a Kubernetes `RuntimeClass`:

- Cluster has gVisor installed (e.g. GKE Sandbox, or `containerd` + `runsc` with a `RuntimeClass` named `gvisor`).
- The `gVisorRunner` creates the sandbox **Pod** with `spec.runtimeClassName: gvisor` and the same hardening (resource limits/requests, `securityContext` with `readOnlyRootFilesystem`, dropped caps, `NetworkPolicy` deny-all for net=none-equivalent, seccomp profile via `securityContext.seccompProfile`).
- `Create/Start/Stdin/Stdout/Stderr/Kill/Cleanup` map to the Kubernetes API (create Pod held/attach via the exec/attach streaming API / delete Pod). Same `Runner` contract; different control plane.
- The per-language behavioral-parity caveat from `STACK.md §4` applies identically (runsc Sentry governs syscalls; validate each language image under gVisor). **Confidence: HIGH on the interface mapping; MEDIUM on parity (empirical, same as STACK.md).**

So the backend lineup behind one interface is: **`DockerSocketRunner` (dev/today) → `FlyMachinesRunner` (Firecracker, §3.1) → `gVisorRunner` (k8s RuntimeClass, future)** — none touch session/clock logic.

### 3.4 Scale-to-zero + autoscaling by queue depth (Fly)

Two cooperating mechanisms:

1. **Machines auto start/stop (the worker fleet):** Fly Machines support **auto start on traffic and auto stop when idle**, and can **scale to zero**. For a *worker* (not a web service) the cleaner driver is queue depth, below — but auto-stop-when-idle is the scale-to-zero primitive.
2. **`fly-autoscaler` by queue depth (RECOMMENDED for worker scaling):** the official **`superfly/fly-autoscaler`** (latest release **v0.3.1**, verified) is a **metrics-based** autoscaler designed exactly for "scale background workers by pending work / queue depth." It polls a metric source (Prometheus, etc.), computes desired Machine count on a ~15s reconcile loop, and starts/stops Machines. Config like `FAS_CREATED_MACHINE_COUNT = "min(50, qdepth / 2)"` maps **Redis `LLEN jobs` (queue depth)** → worker count.

**Important constraint:** **`fly-autoscaler` will NOT scale to zero — it always keeps ≥1 Machine running.** So:
- Use **`fly-autoscaler` for the worker fleet** to scale *up* by queue depth (publish `LLEN jobs` as a metric), keeping a warm floor of ≥1.
- Use **Machines auto-stop/auto-start (scale-to-zero)** for components that can be cold (e.g. an idle demo deployment), accepting cold-start latency.
- For genuine **scale-to-zero workers**, you'd combine auto-stop with a wake-on-enqueue trigger — but for a code-execution service where queue depth maps to live interactive sessions, the `fly-autoscaler` "warm floor of 1, scale up by `LLEN jobs`" model is the right default. **Confidence: HIGH** on the mechanisms; **MEDIUM** on exact scaling expression (tune to slot capacity per `ARCHITECTURE.md` Pattern 5 — scale by depth *and* per-worker live-sandbox slots).

### 3.5 Deployment "do NOT use" / gotcha list

| Gotcha | Why | Mitigation |
|--------|-----|------------|
| Pointing the **worker** at Upstash | No native blocking SUBSCRIBE/BRPOP/XREAD BLOCK → breaks stdin pub/sub + claim | Native-TCP Redis/Valkey for the worker (§3.2 Option 1) |
| Assuming "Fly = Firecracker sandbox" means hosting the worker on Fly | Hosting ≠ per-execution microVM isolation | `FlyMachinesRunner` that *creates a Machine per execution* (§3.1 model A) |
| Expecting `fly-autoscaler` to scale workers to zero | It always keeps ≥1 | Pair with Machines auto-stop for true zero; otherwise warm floor of 1 |
| Treating Upstash as "the prod Redis" wholesale | True for the API, false for the worker | Asymmetric: API can use Upstash; worker needs native Redis |
| Letting the soketi channel-auth helper become core | Violates the trust boundary (auth is the upstream app's job) | Keep it optional/flagged (§1.5) |

---

## 4. Consolidated version table (verified 2026-06-02)

| Component | Package / module | Version | Source |
|-----------|------------------|---------|--------|
| Hono | `hono` | 4.12.23 | npm |
| Node adapter | `@hono/node-server` | 2.0.4 | npm |
| Validator mw | `@hono/zod-validator` | 0.8.0 | npm |
| Schema lib | `zod` | 4.4.3 | npm |
| (alt) std validator | `@hono/standard-validator` | 0.2.2 | npm |
| Redis (TS) | `ioredis` | 5.11.0 | npm |
| Rate limit | `hono-rate-limiter` | 0.5.3 | npm |
| Pusher SDK (optional) | `pusher` | 5.3.3 | npm |
| TS type gen | `json-schema-to-typescript` | 15.0.4 | npm |
| Zod gen | `json-schema-to-zod` | 2.8.1 | npm |
| Go struct gen | `github.com/omissis/go-jsonschema` | v0.23.1 | Go proxy |
| Fly Go SDK | `github.com/superfly/fly-go` | v0.5.6 | Go proxy |
| Fly autoscaler | `superfly/fly-autoscaler` | v0.3.1 | GitHub releases |
| (rejected, ref) | `node-redis` (`redis`) | 6.0.0 | npm — RESP3-default major; avoid for this gateway |
| (rejected, ref) | `@sinclair/typebox` | 0.34.49 | npm — not the contract source of truth |
| (rejected, ref) | `quicktype` | 23.2.6 | npm — Go output less idiomatic |

---

## 5. Confidence summary

| Area | Confidence | Note |
|------|------------|------|
| Hono + Node runtime choice | HIGH | Standard, verified versions |
| Validation deriving from contract | HIGH | Generate Zod from schema; do not hand-write |
| Constant-time bearer auth | HIGH | sha256 + `timingSafeEqual` / `hono/bearer-auth` |
| ioredis + API-side Upstash safety | HIGH | API is non-blocking-only → Upstash-safe |
| Contract: JSON Schema → TS/Go | HIGH | `json-schema-to-typescript` + `omissis/go-jsonschema` |
| Drift check | HIGH | regenerate + `git diff --exit-code` in CI |
| protobuf/TypeSpec rejection rationale | HIGH | wire is JSON-over-Redis, not binary/gRPC |
| **Upstash pub/sub verdict (worker)** | **HIGH** | no native blocking SUBSCRIBE/BRPOP/XREAD BLOCK — use native Redis for the worker |
| **Fly `FlyMachinesRunner` model** | **HIGH (model) / MEDIUM (interactive fit + cost)** | API-driven Machine-per-execution; benchmark streaming + latency |
| gVisor RuntimeClass mapping | HIGH (interface) / MEDIUM (parity) | same as STACK.md runsc caveat |
| Fly autoscale by queue depth | HIGH (mechanism) / MEDIUM (tuning) | `fly-autoscaler` won't scale to zero |

---

## Sources

- npm registry (`npm view <pkg> version`) — hono 4.12.23, @hono/node-server 2.0.4, @hono/zod-validator 0.8.0, @hono/standard-validator 0.2.2, zod 4.4.3, ioredis 5.11.0, redis (node-redis) 6.0.0, pusher 5.3.3, json-schema-to-typescript 15.0.4, json-schema-to-zod 2.8.1, hono-rate-limiter 0.5.3, @sinclair/typebox 0.34.49, quicktype 23.2.6 — **HIGH**
- Go module proxy (`proxy.golang.org`) — omissis/go-jsonschema v0.23.1, atombender/go-jsonschema v0.23.0, superfly/fly-go v0.5.6 — **HIGH**
- GitHub releases API — superfly/fly-autoscaler v0.3.1 — **HIGH**
- Upstash REST API / compatibility docs — pub/sub is REST/SSE only; `BLPOP`/`BRPOP`/`BRPOPLPUSH` unsupported; blocking `XREAD`/`XREADGROUP` unsupported — **HIGH**: https://upstash.com/docs/redis/features/restapi
- omissis/go-jsonschema is the maintained successor to atombender/go-jsonschema (same project) — **HIGH**: https://github.com/omissis/go-jsonschema
- Fly.io Machines API = REST/JSON, Firecracker microVMs, used for ephemeral per-execution sandboxes (CodeCrafters pattern); superfly/fly-go official Go client — **HIGH**: https://fly.io/docs/machines/api/working-with-machines-api/ , https://github.com/superfly/fly-go
- Fly metrics-based autoscaling / fly-autoscaler — scales by queue depth, ~15s reconcile, will NOT scale to zero (keeps ≥1) — **HIGH**: https://fly.io/docs/launch/autoscale-by-metric/ , https://github.com/superfly/fly-autoscaler
- Hono built-in bearer-auth uses `timingSafeEqual`; Node `crypto.timingSafeEqual` requires equal-length buffers (hash-first pattern) — **HIGH** (Hono docs + Node crypto docs)

---
*Supplement to STACK.md — Hono API + shared contract + deployment targets. STACK.md remains authoritative for the Go worker internals.*
*Researched: 2026-06-02*
