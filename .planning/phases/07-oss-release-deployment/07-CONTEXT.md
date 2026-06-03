# Phase 7: OSS Release & Deployment - Context

**Gathered:** 2026-06-03
**Status:** Ready for planning
**Mode:** Auto-generated (discuss skipped — autonomous run)

<domain>
## Phase Boundary

Ship the open-source release: a complete README that gets a self-hoster from clone → running interactive execute, documents the full API contract and the "add a language" guide, covers deployment per target, and documents the upstream channel-auth responsibility. Plus the broader CI (beyond the existing abuse gate): lint + unit tests (Go + JS) + contract drift check, so the repo is genuinely CI-guarded and contribution-ready.

The phase requirement IDs are DOCS-01..04 + CHAN-01, but the user explicitly also wants the full CI matrix (lint, unit, integration, contract drift) — include it here (the abuse-on-Linux gate from Phase 4 already exists at `.github/workflows/abuse.yml`).
</domain>

<decisions>
## Implementation Decisions

### README (DOCS-01..04, CHAN-01) — make it the real, complete project README
The current `README.md` (~180 lines) has partial sections (safety gate, scaling) added in earlier phases. Rewrite/extend it into a coherent whole:
- **What it is + architecture:** the polyglot monorepo, the data flow diagram (upstream app → Hono API → Redis → Go worker → hardened sandbox; output → soketi), the trust boundary (all trusted input via the API; soketi output-only), and the live interactive-session + three-clocks model. Self-hostable, MIT.
- **DOCS-01 Quickstart:** prerequisites (Docker, Go, Node/pnpm), `cp .env.example .env`, build the language images (`make build-images`), `docker compose up`, and run the e2e (`make e2e` / `scripts/e2e.sh`) — show the expected interactive output.
- **DOCS-02 API contract reference:** every `/v1/*` endpoint (method, auth header, request body, responses incl. 202/429/4xx) — `POST /v1/execute`, `/jobs/:id/start|stdin|stdin/close|kill`, `GET /jobs/:id`, `GET /v1/languages`; the wire/output events on `private-run-<jobId>` (`stage`, `stdout`, `stderr`, `result`) with their shapes; the start-handshake sequence (execute → subscribe → start). Note the contract is generated from `packages/contract/schema/wire.schema.json`.
- **DOCS-03 Deployment per target:** dev (docker compose); prod (long-lived **worker nodes** on Fly/any Linux host that launch sandboxes internally, scaled to/from zero by queue depth, gVisor `--runtime=runsc` for extra isolation; native-protocol Redis + soketi; API anywhere) — reference `docs/scaling.md`; future k8s `RuntimeClass=gvisor`; and the v2 `FlyMachinesRunner` microVM-per-execution option with its trade-offs. Be accurate per `.planning/PROJECT.md` Key Decisions (scaling unit = worker node).
- **DOCS-04 Add-a-language guide:** the package model — create `languages/<lang-version>/{manifest.json, Dockerfile}`, the manifest fields (incl. `compile` nullable for compiled languages and the generic compile stage), build the image, the loader auto-discovers it at boot, zero core changes. Use Rust (compiled) and SQLite (non-general-purpose) as worked examples.
- **CHAN-01 Channel auth:** document how the UPSTREAM app authorizes the browser's private soketi channel using the app key/secret (HMAC signature of `socket_id:channel_name`), referencing the stub's implementation (`apps/stub`) and the optional non-core API helper (`/v1/channel-auth`, flag-gated). Make clear the secret never leaves the upstream/API trust boundary.
- **Config/env reference:** table of every env var (from `.env.example`).
- A short **Contributing** note + pointer to the safety gate (abuse suite must be green) + `make` targets table.

### CI (the broader matrix — beyond Phase 4's abuse gate)
`.github/workflows/ci.yml` on `ubuntu-latest`, push + pull_request, with jobs:
- **lint:** `gofmt`/`go vet` (and golangci-lint if easy to add), plus `pnpm -r typecheck`.
- **go-unit:** `go test ./...` (the Docker-free suite).
- **js:** `pnpm install` + `pnpm -r test` (the contract node:test + API vitest — provide a Redis service for the API tests that need it, or ensure they can run; check whether `apps/api` tests require live Redis and add a `redis` service if so).
- **contract-drift:** install Go + node + `go-jsonschema`, run `make contract-check` (regenerate + `git diff --exit-code`) so a schema/codegen drift fails CI.
- **(optional) integration:** the docker/redis-guarded Go integration tests (`-tags=docker`, `-tags=worker_integration`) on ubuntu (has Docker) — can be a separate job; the abuse gate already runs `-tags=abuse`.
Keep YAML correct + idiomatic (can't execute here — author carefully, validate YAML, ensure tool versions + the Redis env var/ports the JS/Go tests expect line up).

### Mechanics
- Verify the README's commands actually work where checkable (e.g. `make build-images` builds all four; `make e2e` passes — already proven; `make contract-check` passes). Validate the new CI YAML. Keep `go test ./...` + `pnpm -r test` green.
</decisions>

<canonical_refs>
## Canonical References — downstream agents MUST read
- `README.md` (current partial), `.env.example`, `Makefile` (targets incl. build-images, e2e, abuse, test-docker, contract-check), `docs/scaling.md`, `docs/redis-constraint.md`
- `.github/workflows/abuse.yml` (existing CI pattern — REDIS_URL/port wiring, service usage)
- `apps/api/src/routes/*` (endpoint behavior for the API reference), `apps/stub/src/index.ts` (channel-auth example), `apps/api/src/channelAuth.ts` (optional helper)
- `packages/contract/schema/wire.schema.json` + `packages/contract/src/index.ts` (the contract + events/channels for the API reference)
- `languages/python-3.12/` + `languages/rust-1.83/` + `languages/sqlite-3/` (add-a-language examples), `internal/manifest` (loader)
- `.planning/PROJECT.md` (Key Decisions — deployment model accuracy), `docker-compose.yml`, `CLAUDE.md`
- `.planning/REQUIREMENTS.md` (DOCS-01..04, CHAN-01)
</canonical_refs>

<specifics>
## Specific Ideas

Phase 7 requirement IDs: DOCS-01, DOCS-02, DOCS-03, DOCS-04, CHAN-01. PLUS the broader CI matrix (lint/unit/js/contract-drift) the user asked for.
Validate what's checkable on this machine (README commands, YAML validity, contract-check, build-images). The CI workflows run on GitHub when pushed (a remote exists). Keep everything green.
</specifics>

<deferred>
## Deferred Ideas

gVisor/Fly runners implementation (v2). Redis Streams (v2). crate/CRAN vendoring (v2). A published image registry / release automation could be future work — Phase 7 ships the source-level OSS release (README, license, .env.example, CI, docs).
</deferred>
