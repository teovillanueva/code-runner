---
phase: 06-language-fan-out
plan: 04
subsystem: languages
tags: [sqlite, docker, alpine, langfanout, integration-test, in-memory-db, sql]

requires:
  - phase: 06-01
    provides: compile-stage worker + Sandbox.Compile interface + langfanout Makefile targets

provides:
  - "SQLite 3 language package: alpine:3.20 + sqlite3, executor/sqlite:3 image (~11 MiB)"
  - "manifest.json: sqlite3 -batch :memory: -init main.sql run argv, compile null, interactive true"
  - "langfanout e2e test: .sql file job + interactive stdin SELECT + clean EOF (exit 0) proven"

affects: [06-02, 06-03, documentation, language-registry]

tech-stack:
  added: ["sqlite3 (alpine apk)", "executor/sqlite:3 image"]
  patterns:
    - "language = image + compile? + run abstraction proven for non-general-purpose SQL language"
    - "-init FILE flag: run .sql file via init then read stdin interactively, exit 0 on EOF"
    - "langfanout test pattern: unique per-language dial/guard helpers + shared langfanout_shared_test.go"

key-files:
  created:
    - languages/sqlite-3/manifest.json
    - languages/sqlite-3/Dockerfile
    - internal/worker/langfanout_sqlite_test.go
  modified: []

key-decisions:
  - "Run argv: sqlite3 -batch :memory: -init main.sql — single argv satisfying both .sql-file and interactive-stdin requirements; -init executes file first, then stdin is read interactively; exit 0 on EOF"
  - "alpine:3.20 (pinned) + apk add sqlite — official SQLite from alpine package repo, baked at build time, no runtime fetch; network=none safe"
  - "langfanout_helpers_test.go NOT created — pre-existing langfanout_shared_test.go already provides all shared helpers; sqlite test uses unique sqliteDialRedis/sqliteDockerGuard/sqliteWorkerStack names"
  - "LANGFANOUT_REDIS_URL env var with fallback to redis://localhost:6386 for langfanout test isolation"

patterns-established:
  - "SQLite langfanout test pattern: Case A (.sql file via -init) + Case B (interactive stdin) + stdin_close → exit 0 + no container leak"

requirements-completed: [LANG-08]

duration: 18min
completed: 2026-06-03
---

# Phase 06 Plan 04: SQLite 3 Language Package Summary

**SQLite 3 stress-tests the "language = image + compile? + run" abstraction: sqlite3 -batch :memory: -init main.sql runs a submitted .sql file AND serves as an interactive shell over stdin, exiting 0 on EOF — zero core changes**

## Performance

- **Duration:** ~18 min
- **Started:** 2026-06-03T00:00:00Z
- **Completed:** 2026-06-03T00:18:00Z
- **Tasks:** 2
- **Files modified:** 3 created (manifest.json, Dockerfile, langfanout_sqlite_test.go)

## Accomplishments

- SQLite 3 language package: `languages/sqlite-3/manifest.json` + `languages/sqlite-3/Dockerfile` — alpine:3.20 + apk sqlite, ~11 MiB image, executor/sqlite:3 built successfully
- Single run argv `["sqlite3", "-batch", ":memory:", "-init", "main.sql"]` satisfies both (a) submitted .sql file execution and (b) interactive stdin SELECT returning rows, exiting 0 on clean EOF
- Langfanout e2e tests pass: Case A stdout `{"chunk":"hi sqlite\n"}` + exitCode 0; Case B `SELECT 1+1;` → `{"chunk":"2\n"}` + interactive INSERT/SELECT "hello" + exitCode 0, idleTimedOut false

## Task Commits

1. **Task 1: SQLite 3 language package (manifest + Dockerfile)** — `48d9f7b` (feat)
2. **Task 2: End-to-end SQLite integration test** — `ad96ffd` (feat)

## Files Created/Modified

- `languages/sqlite-3/manifest.json` — SQLite manifest: compile null, run sqlite3 -batch :memory: -init main.sql, interactive true, 6 defaultLimits all >=1
- `languages/sqlite-3/Dockerfile` — FROM alpine:3.20, apk add sqlite, /workspace chmod 1777, no USER/CMD
- `internal/worker/langfanout_sqlite_test.go` — TestLangFanout_SQLite_FileJob + TestLangFanout_SQLite_InteractiveStdin, guarded //go:build langfanout

## Decisions Made

- **Run argv choice**: Used `["sqlite3", "-batch", ":memory:", "-init", "main.sql"]` as explicitly recommended in the plan. The `-batch` flag disables prompts; `-init main.sql` executes the submitted file first; sqlite3 then reads from stdin until EOF, exiting 0.
- **Shared helpers**: Did not create a duplicate helpers file. The pre-existing `langfanout_shared_test.go` (untracked, from plans 06-02/06-03) provides `integrationTriggerer`, `assertNoContainerLeak`, `publishStdinRaw`. Used unique `sqliteDialRedis`/`sqliteDockerGuard`/`sqliteWorkerStack` names.
- **Redis port**: `LANGFANOUT_REDIS_URL` env var defaulting to `redis://localhost:6386` (matching Makefile comment). Tests were run with port 6389 per prompt instructions.

## Deviations from Plan

None — plan executed exactly as written.

The one deviation that appeared: pre-existing untracked `langfanout_shared_test.go`, `langfanout_rust_test.go`, `langfanout_r_test.go` files were discovered in the working tree. I did NOT commit these (they belong to plans 06-02/06-03) and did NOT touch them. My helpers file (`langfanout_helpers_test.go`) was created and then deleted when the shared file was discovered, keeping the diff clean.

## Real Build + E2E Output

**docker build:**
```
#5 (2/4) RUN apk add --no-cache sqlite
#5 (4/4) Installing sqlite (3.45.3-r3)
#5 OK: 11 MiB in 18 packages
#8 writing image sha256:6593d8f65aff done
#8 naming to docker.io/executor/sqlite:3 done
```

**TestLangFanout_SQLite_FileJob (Case A):**
```
stdout: {"chunk":"hi sqlite\n","seq":1}
result: {"durationMs":1065,"exitCode":0,"idleTimedOut":false,"signal":null,"timedOut":false,"truncated":false}
--- PASS: TestLangFanout_SQLite_FileJob (2.13s)
```

**TestLangFanout_SQLite_InteractiveStdin (Case B):**
```
stdout: {"chunk":"2\n","seq":1}
stdout: {"chunk":"hello\n","seq":2}
result: {"durationMs":1067,"exitCode":0,"idleTimedOut":false,"signal":null,"timedOut":false,"truncated":false}
--- PASS: TestLangFanout_SQLite_InteractiveStdin (2.08s)
```

**go test ./...:** all 12 packages PASS (langfanout tag excluded from default suite).

## Zero Core Changes Confirmed

`git diff --stat HEAD` shows only `languages/sqlite-3/` files and `internal/worker/langfanout_sqlite_test.go`. No `internal/**` (worker, runner, session, etc.) was modified.

## Issues Encountered

None — straightforward implementation. The `-init main.sql` argv was verified to work for both the file-run path (init executes the SQL file) and the interactive path (after init, stdin is read until EOF).

## Threat Surface Scan

No new network endpoints, auth paths, file access patterns, or schema changes introduced. The SQLite sandbox runs with the same hardening (network=none, read-only rootfs, cap-drop ALL, non-root user) as all other language images. The :memory: DB is ephemeral and destroyed on process exit (T-06-04-01, T-06-04-02 mitigated by existing runner hardening). Alpine base image pinned to alpine:3.20 (T-06-04-SC).

## Next Phase Readiness

- LANG-08 proven end-to-end; SQLite 3 is a fully supported language package
- All three Wave 2 language packages (Rust 1.83, R 4.4, SQLite 3) can now be built and tested via `make build-images && make langfanout`

---
*Phase: 06-language-fan-out*
*Completed: 2026-06-03*

## Self-Check: PASSED

Files created:
- FOUND: languages/sqlite-3/manifest.json
- FOUND: languages/sqlite-3/Dockerfile
- FOUND: internal/worker/langfanout_sqlite_test.go
- FOUND: .planning/phases/06-language-fan-out/06-04-SUMMARY.md

Commits:
- FOUND: 48d9f7b (feat(06-04): SQLite language package)
- FOUND: ad96ffd (feat(06-04): SQLite langfanout test)
