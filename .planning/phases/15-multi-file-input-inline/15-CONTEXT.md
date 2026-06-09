# Phase 15 — Multi-file Input (inline) · Context

**Goal:** Let callers send multiple input files (text + binary, in subdirectories) inline in `/v1/execute`, materialized safely under `/workspace` before the run. Zero new infrastructure. Independently shippable.

**Requirements:** FILES-01..FILES-08 (see REQUIREMENTS.md).

## Locked design (from the design discussion — do NOT re-litigate)

- `files[]` already exists in the wire contract. Two real gaps: (1) `content` is UTF-8-only → binary impossible; (2) the Docker runner write path flattens paths with `filepath.Base` → no subdirs.
- **`encoding` field** on `FileInput`: `"utf8"` (default) | `"base64"`. Default `utf8` keeps every existing text caller working with zero changes. `base64` carries arbitrary bytes (xlsx/parquet/images/zip).
- **Subdirectory paths** in `FileInput.name` (e.g. `data/input.csv`). Sanitized with a path-clean anchored at the workspace root so `..`/absolute paths collapse *inside* `/workspace` and can never escape.
- **Sanitization is enforced in the worker** regardless of API validation (threat model = host-escape-only; the worker never trusts the path even though the API is the trusted seam).
- **Worker pulls nothing external here** — fully inline. CAS/presigned is Phase 16.

## Surfaces (all must be touched — this is cross-cutting)

1. **Contract** — `packages/contract/schema/wire.schema.json`: add `encoding` to `FileInput` (enum `utf8|base64`, default `utf8`, optional → backward-compat); tighten the `name` description (relative path, may contain `/`, must not escape). Then `pnpm contract` → `make contract-check` must pass. NEVER hand-edit `packages/contract/gen/**`.
2. **Go runner — DockerSocketRunner** (`internal/runner/docker.go`):
   - `copyFilesToContainer` (~447): decode `f.Content` per `f.Encoding` (base64→bytes else raw); replace `filepath.Base(f.Name)` with a shared `sanitizeWorkspacePath` that PRESERVES subdirs but blocks escape; add tar **directory entries** for parent dirs (or verify Docker's Untar auto-creates parents — verify, don't assume).
   - Artifact read side `ReadArtifacts` (~670-710) currently uses `filepath.Base(hdr.Name)` and "skips nested paths". Make capture consistent with subdir inputs — at minimum the exclude check must compare on the same path form the exclude set now uses (full relative path).
3. **Worker exclude set** (`internal/worker/worker.go` ~1118 `buildArtifactExcludeSet`): use the full sanitized **relative path**, not `filepath.Base`, so a subdir input (`data/input.csv`) is correctly excluded from artifact capture. Update `worker_artifacts_test.go` expectations accordingly.
4. **Go runner — ZygoteRunner parity** (`internal/runner/zygote.go` ~191, `zygote_relay.go` ~60 `helloFile`): the agent's `_materialize_files` (python `zygote_agent.py` ~361) ALREADY handles subdirs + str/bytes and has its own traversal guard — good. But binary travels over the relay as a JSON **string**, so: add `Encoding` to `helloFile` (backward-compatible, absent→utf8), pass it through the relay, and have the agent base64-decode when `encoding=="base64"` before writing `wb`. Check the **R agent** (`languages/r-4.4/zygote_agent.R`) for the same materialization path and apply parity. Keep the agents' existing traversal guards.
5. **API** (`apps/api/src/config.ts` + `apps/api/src/routes/execute.ts`): add `maxFilesBytes` to `Config` (env `MAX_FILES_BYTES`, sensible default e.g. 8 MiB). In the execute handler, AFTER zod validation: compute total decoded bytes (base64 decode each file once), reject >cap with **413**; reject invalid base64 or an escaping/absolute path with **400** (clear message) BEFORE enqueue. Keep the existing manifest-resolution-before-capacity ordering.
6. **Node SDK** (`packages/code-runner-sdk-node`): accept text and binary (`Buffer`) files; when a `Buffer` is given, base64-encode and set `encoding:"base64"` transparently. Keep the raw `FileInput` path working.
7. **Tests:** Go runner unit (base64 round-trip, subdir materialization, traversal rejection), worker exclude-set test update, API tests (413 cap, 400 base64/path), contract test for the new field. `go build ./... && go test ./...`, `pnpm -r test`, `make contract-check` all green.
8. **Docs:** `docs/` — document multi-file input, `encoding`, subdirs, the size cap env, and a binary-file example. Update `.env.example` with `MAX_FILES_BYTES`.

## Constraints
- Atomic commits; end each message with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Contract is the fragile seam: schema → `pnpm contract` → consumers → `make contract-check`.
- Backward compatibility is a hard requirement: a request with no `encoding` and flat filenames must behave exactly as today.
