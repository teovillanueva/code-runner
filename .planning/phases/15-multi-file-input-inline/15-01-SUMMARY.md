---
phase: 15
plan: "01"
subsystem: input-files
tags: [contract, runner, worker, zygote, api, sdk-node, docs]
requires: [FILES-01, FILES-02, FILES-03, FILES-04, FILES-05, FILES-06, FILES-07, FILES-08]
provides:
  - FileInput.encoding (utf8|base64) on the wire
  - subdir-aware workspace file materialization
  - worker-side path sanitization (host-escape-only)
  - MAX_FILES_BYTES size cap (413) + base64/path validation (400)
  - SDK Buffer/text passthrough
affects: [packages/contract, internal/runner, internal/worker, apps/api, packages/code-runner-sdk-node, languages/python-3.12, languages/r-4.4, docs]
tech-stack:
  added: []
  patterns: [shared path sanitizer reused across runner+worker+API, base64-over-relay for zygote tier]
key-files:
  created:
    - internal/runner/files.go
    - internal/runner/files_test.go
    - internal/runner/docker_files_test.go
    - internal/worker/exclude_set_test.go
    - apps/api/src/files.ts
    - apps/api/test/execute-files.test.ts
    - packages/code-runner-sdk-node/src/files.ts
    - packages/code-runner-sdk-node/test/files.test.ts
    - packages/contract/test/file-input.test.ts
    - docs/input-files.md
  modified:
    - packages/contract/schema/wire.schema.json
    - packages/contract/gen/** (generated)
    - internal/runner/docker.go
    - internal/runner/zygote.go
    - internal/runner/zygote_relay.go
    - internal/runner/zygote_test.go
    - internal/worker/worker.go
    - internal/worker/worker_artifacts_test.go
    - languages/python-3.12/zygote_agent.py
    - languages/python-3.12/zygote_selftest.py
    - languages/r-4.4/zygote_hard.c
    - apps/api/src/config.ts
    - apps/api/src/routes/execute.ts
    - packages/code-runner-sdk-node/src/client.ts
    - packages/code-runner-sdk-node/src/index.ts
    - .env.example
    - README.md
decisions:
  - Reject ".." traversal outright (stricter than path-collapse) — worker never trusts the path.
  - R zygote native helper is WIP and does not stage files[]; R ships on the Docker tier which already has full parity. Documented the gap + required parity in zygote_hard.c.
metrics:
  duration: ~135 min
  completed: 2026-06-09
---

# Phase 15 Plan 01: Multi-file Input (inline base64 + subdirs) Summary

Inline multi-file input for `/v1/execute`: callers send text **and binary**
(`encoding:"base64"`) files in **subdirectories**, materialized safely under
`/workspace` with worker-side path sanitization. Zero new infrastructure;
fully backward compatible.

## What changed per task

### Task 1 — Contract (`2a4d36d`)
Added optional `encoding` enum (`utf8` default, `base64`) to `FileInput` and
tightened the `name` description (relative path, may contain `/`, no escape).
Regenerated Go structs (`FileInputEncoding` + `Encoding` field), TS types, and
zod validators via `pnpm contract`. `make contract-check` passes (deterministic
regen, no drift).

### Task 2 — Shared Go helpers (`a82fdd0`)
`internal/runner/files.go`:
- `decodeFileContent(wire.FileInput) ([]byte, error)` — utf8 verbatim (default)
  or base64-decode with clear errors.
- `SanitizeWorkspacePath(name) (string, error)` — preserves subdirs; rejects
  empty, absolute, and **any `..` segment**. Uses `path` (forward-slash wire).
  Exported (renamed from internal) so the worker reuses the exact same primitive.
Table-driven unit tests for both.

### Task 3 — DockerSocketRunner (`7149195`)
`copyFilesToContainer` now delegates to a pure, unit-testable `buildFilesTar`:
per-file path sanitization + content decode, explicit `tar.TypeDir` parent-dir
entries (deterministic layout). `ReadArtifacts` captures nested files and
excludes on the **full relative path** (strips the `workspace/` prefix via new
`stripWorkspacePrefix`); `CapturedArtifact.Name` is now a relative path. Unit
tests (no Docker): base64 round-trip, subdir materialization, traversal/base64
rejection, flat-file backward-compat, prefix stripping.

### Task 4 — Worker exclude set (`6186b3c`)
`buildArtifactExcludeSet` keys input files by full sanitized relative path
(reusing `runner.SanitizeWorkspacePath`), so a subdir input (`data/in.csv`) is
correctly excluded — not just its basename. Unsanitizable names are skipped.
Added a pure (no-Redis) unit test plus a subdir-exclusion assertion to the
Redis-gated compile-output test.

### Task 5 — ZygoteRunner / agents parity (`058d411`)
`helloFile` gains `Encoding` (`omitempty`, absent→utf8); `zygote.go` threads
`f.Encoding` verbatim through the relay. The Python agent's `_materialize_files`
base64-decodes when `encoding=="base64"` before writing `wb`, keeping its
existing str/bytes branch, subdir creation, and traversal guard. Go test asserts
encoding flows through `buildHello`; a base64+subdir self-test case was added to
the Python relay self-test. **R:** see Deviations.

### Task 6 — API (`23cc17c`)
`config.maxFilesBytes` from `MAX_FILES_BYTES` (default 8 MiB). `apps/api/src/files.ts`
mirrors the worker sanitizer in TS and sums **decoded** bytes. `execute.ts`, after
manifest resolution and before the capacity check, returns **400** on invalid
base64 / absolute / traversal paths and **413** when total decoded bytes exceed
the cap — preserving the 400/413-before-429 ordering. The worker still
re-sanitizes regardless. Pure unit + HTTP tests (413, 400×2, happy base64+subdir,
backward-compat 202).

### Task 7 — Node SDK (`d18ba3c`)
`packages/code-runner-sdk-node/src/files.ts`: `SdkFileInput` union (raw
`FileInput` | text | `{name, data: Buffer|Uint8Array}`); `toFileInput`/
`toFileInputs` base64-encode binary transparently. `client.executeFiles`
normalizes mixed inputs then delegates to the unchanged `execute()`. Helpers
exported. Tests cover Buffer/Uint8Array/text/raw normalization and wire base64
serialization.

### Task 8 — Docs (`b09d5e0`)
`docs/input-files.md` (FileInput shape, subdirs, base64, SDK Buffer helper, size
cap, validation table, worker-side safety, scope note). README execute example
shows `encoding`+subdirs, adds the 413 row, the `MAX_FILES_BYTES` env row, and an
`executeFiles` binary snippet. `.env.example` gains `MAX_FILES_BYTES`.

### Follow-up — Contract test (`fa83cfe`)
`packages/contract/test/file-input.test.ts`: asserts the generated zod defaults
`encoding=utf8`, accepts utf8/base64, rejects unknown encoding, accepts subdir
names, still requires name+content.

## Commit SHAs

| Task | SHA | Message |
| --- | --- | --- |
| 1 | `2a4d36d` | feat(contract): FileInput.encoding + subdir-aware name |
| 2 | `a82fdd0` | feat(runner): shared file decode + workspace path sanitizer |
| 3 | `7149195` | feat(runner): docker runner writes binary + subdir files |
| 4 | `6186b3c` | fix(worker): exclude input artifacts by full relative path |
| 5 | `058d411` | feat(zygote): binary + subdir file parity across relay + agents |
| 6 | `23cc17c` | feat(api): MAX_FILES_BYTES cap (413) + base64/path validation (400) |
| 7 | `d18ba3c` | feat(sdk-node): accept binary Buffer input files |
| 8 | `b09d5e0` | docs: multi-file input + MAX_FILES_BYTES |
| +1 | `fa83cfe` | test(contract): FileInput.encoding contract test |

## Test results

All green (Redis-backed tests run against a throwaway `redis:7-alpine` on :6380):

- `go build ./...` — clean (exit 0)
- `go vet ./...` — clean
- `go test ./...` — all packages `ok` (runner, worker, jobstore, session, …)
- `pnpm -r test` — contract (16), sdk-node (34), api (81), react (20) — all pass
- `make contract-check` — no drift

### Linux-only / skipped tests (expected, per the existing pattern)
- **Docker-tagged runner integration** (`//go:build docker`,
  `docker_integration_test.go`, `zygote_integration_test.go`,
  `zygote_abuse_test.go`) — not run on macOS without the `docker` tag. The new
  file logic is covered by the **non-Docker** unit tests (`buildFilesTar`,
  `stripWorkspacePrefix`, `decodeFileContent`, `SanitizeWorkspacePath`,
  `buildArtifactExcludeSet`, `buildHello`).
- **Python zygote relay self-test** (`zygote_selftest.py`) — requires a live
  privileged Linux pool container; I added a `test_base64_subdir` case that runs
  when the harness runs on Linux. Not executed here (macOS, no privileged
  container). Verified the script compiles (`py_compile`).

## Backward-compat confirmation

A request with **no `encoding`** and **flat filenames** behaves exactly as
before — proven by explicit regression tests:
- `TestBuildFilesTar_BackwardCompat` (Go): a flat no-encoding file → a single
  regular tar entry with verbatim bytes and **no** dir entries.
- `decodeFileContent` test: absent encoding → content taken verbatim as bytes.
- API `backward-compat … behaves exactly as before (202)` test.
- Contract test: absent `encoding` defaults to `utf8`.
- The existing `apps/api` execute suite (19 tests, all flat/no-encoding) is
  unchanged and green.

## Deviations / decisions

1. **`..` traversal rejected, not collapsed.** The locked design described
   collapsing `..` inside the workspace root. Execution rule #4 mandates
   *rejecting* `..` traversal. `path.Clean("/"+name)` actually folds `../x` to
   `/x` (inside root), so a post-clean check never fires — I therefore reject any
   path containing a literal `..` segment outright. This is strictly safer and
   matches the worker-never-trusts intent. Mirrored identically in the TS API
   validator.
2. **R agent parity (rule #6).** Investigation found the R zygote path is an
   explicit **WIP**: `zygote_agent.R` is a thin shim over native
   `zygote_hard.c`, whose `materialize_and_run` **ignores `files_json`** and
   sources only the bare entrypoint (job-file staging is unimplemented — see
   `.planning/decisions/ZYGOTE-R-STATUS.md`). R has **no preimport** in its
   manifest, so it routes to the **DockerSocketRunner tier**, which already has
   full base64 + subdir + sanitization parity (Task 3). I left the WIP helper
   functionally unchanged and documented the gap + the exact parity required
   (per-file encoding, subdir creation, traversal guard anchored at
   `CHILD_WORKDIR`) in `zygote_hard.c` for the future implementer. R's
   production multi-file path is therefore covered.
3. **Exported `runner.SanitizeWorkspacePath`** (renamed from unexported) so the
   worker and runner share one sanitizer; the API has a faithful TS mirror. The
   worker remains the trust boundary (it re-sanitizes regardless of the API).
4. **Refactored `copyFilesToContainer` → pure `buildFilesTar`** so the tar-build
   logic (the file-materialization correctness) is unit-testable without a
   Docker daemon on macOS/CI.

## Known stubs
None.

## Self-Check: PASSED
- `internal/runner/files.go`, `apps/api/src/files.ts`,
  `packages/code-runner-sdk-node/src/files.ts`, `docs/input-files.md` — present.
- All 9 commits present in `git log` on branch `phase-15-multi-file-input`.
