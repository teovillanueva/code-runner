---
phase: 01-foundation-wire-contract
plan: "03"
subsystem: worker-boot + contract-manifest-loader
tags: [go, typescript, manifest, runner, stdintransport, boot, lang-discovery]
dependency_graph:
  requires: [01-01, 01-02]
  provides: [worker-binary, ts-manifest-loader]
  affects: [apps/worker, packages/contract]
tech_stack:
  added: [node:test (TS via --experimental-strip-types), allowImportingTsExtensions]
  patterns: [manifest-driven boot, stub seam construction, shared contract-validated loader]
key_files:
  created:
    - apps/worker/main.go
    - apps/worker/main_test.go
    - packages/contract/src/manifest.ts
    - packages/contract/test/manifest.test.ts
  modified:
    - packages/contract/src/index.ts
    - packages/contract/package.json
    - packages/contract/tsconfig.json
decisions:
  - "Used os.Stat check before manifest.Load to produce a clear error on missing dir (glob returns empty not error)"
  - "Used .ts extensions in manifest.ts imports + allowImportingTsExtensions tsconfig option to enable direct Node.js execution with --experimental-strip-types"
  - "Test runner updated to node --experimental-strip-types --test 'test/**/*.ts' for native TS test execution without a build step"
  - "Type-cast result.data as Manifest to bridge zod's string[] inference vs. the generated [string, ...string[]] tuple type"
metrics:
  duration_minutes: 20
  completed_date: "2026-06-02"
  tasks_completed: 2
  files_created: 4
  files_modified: 3
---

# Phase 1 Plan 03: Boot-load manifests, wire stubs, shared TS manifest loader Summary

**One-liner:** Worker binary discovers languages at boot from manifests via manifest.Load/List, wires Runner + StdinTransport stubs; shared TS loader uses generated ManifestSchema for API-side non-hardcoded language resolution.

## What Was Built

### Task 1: apps/worker/main.go — boot-load manifests, wire stubs

`apps/worker/main.go` is the minimal Phase-1 worker entrypoint. It:
- Resolves the languages directory from `LANGUAGES_DIR` env var (default: `"languages"`)
- Calls `manifest.Load(dir)` — exits non-zero with a clear message on failure
- Iterates `registry.List()` and logs each `LanguageInfo` (language, version, aliases, interactive) via `slog` — no language name is hardcoded
- Constructs `runner.NewStub()` and `stdintransport.NewStub()`, proving both Phase-1 seams compile into one binary without Docker/Redis

The `run(dir string, out io.Writer) error` helper is factored out of `main()` for testability. Tests (`main_test.go`) assert boot output contains "python" and a missing directory yields a non-nil error.

### Task 2: packages/contract/src/manifest.ts — shared TS manifest loader

`manifest.ts` exports three functions used by the future Hono API:
- `loadManifests(dir)` — globs `<dir>/*/manifest.json`, JSON-parses each, validates with the generated `ManifestSchema` (throws naming the offending file on failure)
- `toLanguageInfo(manifests)` — maps to `LanguageInfo[]` for `GET /v1/languages`, with zero hardcoded identifiers
- `resolveManifest(manifests, language, version?)` — resolves by language name or alias, with optional version; throws a clear not-found error

All three are re-exported from `packages/contract/src/index.ts` so the Hono API imports from `@code-runner/contract`.

Seven `node:test` tests cover: load-valid (real languages/ dir yields python), load-malformed-throws (error names file), toLanguageInfo shape, resolve-by-name, resolve-by-alias, resolve-by-alias+version, resolve-unknown-throws.

## Test/Build Results

```
go build ./...                                              PASS
go test ./...                                              PASS (all 6 packages)
  apps/worker           ok  (2 tests: python detected, missing dir errors)
pnpm --filter @code-runner/contract test                   PASS (7/7 tests)
pnpm --filter @code-runner/contract typecheck              PASS
make contract-check                                        PASS (no drift)
```

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] manifest.Load returns empty registry (not error) for missing directories**
- **Found during:** Task 1 test execution
- **Issue:** `filepath.Glob` returns `(nil, nil)` when the pattern matches nothing, so `manifest.Load` returns an empty registry for a non-existent directory instead of an error.
- **Fix:** Added `os.Stat(dir)` check in `run()` before calling `manifest.Load`, returning a descriptive error if the directory does not exist.
- **Files modified:** `apps/worker/main.go`

**2. [Rule 3 - Blocking] TypeScript .ts extension imports needed for Node.js direct execution**
- **Found during:** Task 2 test setup
- **Issue:** The contract package uses `moduleResolution: "Bundler"` with `.js` extension imports (TypeScript convention). Node.js `--experimental-strip-types` does not remap `.js` → `.ts` at runtime, causing module-not-found errors.
- **Fix:** Changed imports in `manifest.ts` to use `.ts` extensions; added `allowImportingTsExtensions: true` to `tsconfig.json` (valid with `noEmit: true`). Updated test script to `node --experimental-strip-types --test 'test/**/*.ts'`.
- **Files modified:** `packages/contract/src/manifest.ts`, `packages/contract/tsconfig.json`, `packages/contract/package.json`

**3. [Rule 1 - Type] Zod schema infers `string[]` for `run` field vs. tuple type `[string, ...string[]]`**
- **Found during:** Task 2 typecheck
- **Issue:** `ManifestSchema.safeParse()` returns `z.infer<typeof ManifestSchema>` where `run` is `string[]`, but the hand-written `Manifest` interface (in `types.ts`) declares `run: [string, ...string[]]`. TypeScript rejects the assignment.
- **Fix:** Added `result.data as Manifest` cast with an explanatory comment (zod validates the `min(1)` constraint at runtime, making the cast safe).
- **Files modified:** `packages/contract/src/manifest.ts`

## Known Stubs

None — `manifest.ts` wires to real filesystem I/O and real generated schema validation.

## Threat Flags

None — no new network endpoints, auth paths, or external surface introduced.

## Self-Check: PASSED

- `apps/worker/main.go` exists: FOUND
- `apps/worker/main_test.go` exists: FOUND
- `packages/contract/src/manifest.ts` exists: FOUND
- `packages/contract/test/manifest.test.ts` exists: FOUND
- Commit 8789d4c (Task 1): FOUND
- Commit cb5e93b (Task 2): FOUND
