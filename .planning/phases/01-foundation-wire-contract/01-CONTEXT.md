# Phase 1: Foundation & Wire Contract - Context

**Gathered:** 2026-06-02
**Status:** Ready for planning
**Mode:** Auto-generated (discuss skipped — autonomous run)

<domain>
## Phase Boundary

Establish the polyglot monorepo, the single-source-of-truth wire contract, the manifest model, and the swap-boundary interfaces — then prove "drop a folder = a new language" and the Docker→gVisor / pub-sub→Streams seams without touching core later.

**IMPORTANT — much of this phase is already built and committed. Do NOT recreate:**
- Monorepo scaffold: `pnpm-workspace.yaml`, root `package.json`, root `go.mod` (module `github.com/teovillanueva/code-runner`), `Makefile`, `.gitignore`, `LICENSE` (MIT), `.env.example`.
- `packages/contract`: `schema/wire.schema.json` (single source of truth), `scripts/generate.mjs` (codegen), `src/index.ts` (re-exports + shared key/channel/event conventions), `tsconfig.json`, and committed generated artifacts `gen/ts/{types,schemas}.ts` + `gen/go/wire/wire.gen.go`. `pnpm contract` works; `make contract-check` is the drift gate; `go build ./...` passes.

**Remaining Phase 1 work to plan:**
- Manifest loader in Go (`internal/manifest`): read all `languages/*/manifest.json` at boot, validate against the contract `Manifest` shape, expose available languages + resolve language/alias+version → manifest, with clear errors on malformed/duplicate manifests. Per-request limits override defaults (resolution helper). Unit tests.
- Manifest loader in TS for the API (`apps/api` or a shared util) OR documented plan to reuse the contract: the API must list languages (GET /v1/languages) and resolve a manifest to build a JobSpec. Keep languages non-hardcoded.
- `Runner`/`Sandbox` interface skeleton (`internal/runner`) covering create-hardened-sandbox / attach pipes / enforce limits / kill / cleanup, with a no-op or stub implementation proving the seam compiles, plus a `StdinTransport` interface (`internal/stdintransport` or similar) so Redis pub/sub can later swap to Streams.
- `internal/keys` in Go mirroring the contract's shared Redis key/channel/event names exported from `@code-runner/contract`.
- A sample `languages/python-3.12/manifest.json` may be added to exercise the loader (the Dockerfile/image is Phase 3), OR a test fixture manifest under the loader's testdata.
- Unit tests: manifest loader (valid, malformed, duplicate, alias resolution, limits override), and a contract-drift test (or rely on `make contract-check` in CI).
</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
All implementation choices are at Claude's discretion (discuss skipped per autonomous run). Follow `.planning/research/` (STACK.md, STACK-API-CONTRACT-DEPLOY.md, ARCHITECTURE.md), `CLAUDE.md` "Environment & Build Notes", and the already-committed contract. Key locked facts:
- Single Go module at repo root; Go code under `apps/worker` (cmd) + `internal/*`; worker imports generated structs from `github.com/teovillanueva/code-runner/packages/contract/gen/go/wire`.
- API is Hono/TS in `apps/api`, imports `@code-runner/contract`.
- The contract is the fragile seam: never hand-edit `packages/contract/gen/**`; change the schema + regenerate.
- Manifest fields are exactly the contract `Manifest`: language, version, aliases, image, entrypoint, compile (nullable), run, interactive, defaultLimits{wallTimeMs,idleMs,cpuMs,memoryMb,pids,outputKb}.
</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Contract & conventions
- `packages/contract/schema/wire.schema.json` — the wire/manifest single source of truth
- `packages/contract/src/index.ts` — shared Redis keys, channel names, soketi event names
- `packages/contract/gen/go/wire/wire.gen.go` — generated Go structs (Manifest, JobSpec, Limits, etc.)

### Project context
- `.planning/REQUIREMENTS.md` — REQ-IDs (CONT-*, LANG-*, RUN-*, STDIN-*, CFG-*, OSS-*)
- `.planning/research/STACK.md` and `.planning/research/STACK-API-CONTRACT-DEPLOY.md` — stack decisions
- `CLAUDE.md` — Environment & Build Notes (toolchain, codegen, conventions)
</canonical_refs>

<specifics>
## Specific Ideas

Phase 1 requirement IDs: CONT-01..06, LANG-01..03, RUN-01, STDIN-04, CFG-04, OSS-01, OSS-02. CONT-*/OSS-* and most scaffolding are already satisfied by committed work — plans should VERIFY those exist and focus net-new effort on LANG-01..03 (manifest loader, non-hardcoded), RUN-01 (Runner/Sandbox interface), STDIN-04 (StdinTransport interface), and CFG-04 (document/encode the native-Redis-for-worker constraint).
</specifics>

<deferred>
## Deferred Ideas

The actual DockerSocketRunner implementation, three clocks, hardening (Phase 2); the Hono API endpoints + Redis wiring + Python image (Phase 3). Phase 1 is contracts, scaffolding, loader, and interface skeletons only.
</deferred>
