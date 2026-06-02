# Phase 6: Language Fan-out (Rust, R, SQLite) - Context

**Gathered:** 2026-06-03
**Status:** Ready for planning
**Mode:** Auto-generated (discuss skipped — autonomous run)

<domain>
## Phase Boundary

Prove the manifest extensibility invariant by adding Rust, R 4.4, and SQLite 3 — each as `languages/<lang-version>/{manifest.json, Dockerfile}` with ZERO language-specific changes to the Go worker or the API. Includes the SQLite case, which stress-tests whether "language = image + compile? + run" holds for something that is NOT a general-purpose language (SQL via an interactive `sqlite3` shell + a `.sql` file).
</domain>

<decisions>
## Implementation Decisions

### IMPORTANT — one generic core gap to close FIRST (Task 0, not language-specific)
The worker does NOT yet handle `manifest.compile` (verified: `JobSpec.Compile` is unused in production code). Rust is compiled, so the CORE must support a **generic compile stage** before any compiled language works. This is generic infra (one-time), after which Rust/R/SQLite are pure folder+image:
- In the worker/session run path: if `JobSpec.compile != null`, BEFORE the run command, execute the compile command **inside the sandbox** (same hardening, same network=none, writable `/workspace` anonymous volume), emit `stage compiling`, enforce limits (reuse the wall/CPU clocks for the compile step — a compile-bomb must be killed as a tree, like any other), and only on compile success (exit 0) proceed to emit `stage running` + run the produced binary. On compile failure, stream the compiler stderr and publish a terminal `result` with the non-zero exit code (no run). Keep this generic — driven entirely by the manifest's `compile`/`run` argv, no "if rust" anywhere.
- Decide cleanly where this lives (likely `internal/session` / `internal/worker` run path, possibly a `Sandbox.Exec`/second-command capability on the runner). Extend the `Runner`/`Sandbox` interface if needed (update the stub). This is the ONLY core change allowed in this phase; everything else is manifest+Dockerfile.

### Rust (LANG-06)
- `languages/rust-1.83/{manifest.json, Dockerfile}`. Image `executor/rust:1.83` from `rust:1.83-slim` (or pin a current 1.x). Non-root, PYTHON-style unbuffered N/A. `compile: ["rustc","-O","main.rs","-o","/tmp/prog"]` (or `/workspace/prog`), `run: ["/tmp/prog"]` (match the compile output path; use the writable workspace/tmpfs), `entrypoint: "main.rs"`, `interactive: true`, sensible defaultLimits (compile needs more wall/cpu/mem than run — set generous-but-bounded limits). Verify a compile-error program yields the compiler stderr + non-zero result, and a compile-bomb is killed.

### R 4.4 (LANG-07)
- `languages/r-4.4/{manifest.json, Dockerfile}`. Image `executor/r:4.4` from `r-base:4.4.x` (or rocker/r-ver). Preinstall a few common libs. `compile: null`, `run: ["Rscript","main.R"]`, `entrypoint: "main.R"`, `interactive: true`. Force unbuffered/line-buffered output if needed for streaming. (r-base image is large — build will take time; that's fine.)

### SQLite 3 (LANG-08) — the abstraction stress test
- `languages/sqlite-3/{manifest.json, Dockerfile}`. Small image (`alpine` + `sqlite`). The "language" is SQL against an ephemeral in-memory DB. Support BOTH: a `.sql` file (`entrypoint: "main.sql"`) and an interactive `sqlite3` shell reading stdin. Pick a clean `run` argv that works with the existing model — e.g. `run: ["sqlite3",":memory:",".read main.sql"]` runs the file then drops to interactive reading stdin, OR `run: ["sqlite3","-batch",":memory:"]` with the file content fed via stdin; the planner picks the approach that (a) executes a submitted `.sql` file AND (b) supports interactive stdin (`SESS` model), with `compile: null`, `interactive: true`. Ensure clean EOF behavior (stdin_close → sqlite exits 0).

### Validation / tests (per language, through the worker)
- Build each image locally (network available). For EACH language run a real job through the worker (Redis + Docker) asserting it works end-to-end: Rust (compile + run + a compile-error case), R (`Rscript` prints + interactive), SQLite (`.sql` file executes + interactive `SELECT` over stdin + clean EOF). Reuse the worker integration / abuse harness. The abuse-suite gate (Phase 4) should conceptually pass for the new images (at least spot-check pids/output for one).
- Prove the "zero core changes per language" invariant: after Task 0, adding each language touches ONLY `languages/<lang>/` (+ maybe compose/docs to build the image). Call this out explicitly.
</decisions>

<canonical_refs>
## Canonical References — downstream agents MUST read
- `internal/session/interactive.go`, `internal/worker/worker.go` (the run path — where the generic compile stage goes), `internal/runner/docker.go` (sandbox exec/run; extend for a compile command if needed), `internal/runner/runner.go` + `stub.go` (interface + stub to update)
- `internal/publisher` (stage events: queued/compiling/running), `packages/contract/gen/go/wire` (`StagePhase` has `compiling`), `internal/manifest` (loader + resolver)
- `languages/python-3.12/{manifest.json,Dockerfile}` (the reference language package), `internal/manifest/manifest.go`
- `internal/worker/integration_test.go`, `internal/worker/abuse_test.go` (harness to reuse)
- `.planning/research/STACK.md`, `.planning/research/FEATURES.md` (compile vs interpreted, SQLite-as-language, per-language buffering), `CLAUDE.md`
- `docker-compose.yml` / a `make build-images` target (add the new images)
</canonical_refs>

<specifics>
## Specific Ideas

Phase 6 requirement IDs: LANG-06 (Rust compile+run), LANG-07 (R Rscript), LANG-08 (SQLite in-memory, .sql file + interactive shell).
ACTUALLY build the three images and run a real job per language through the worker; paste the real output (compile→run for Rust incl. a compile-error case, Rscript for R, interactive SELECT + .sql for SQLite). Keep `go test ./...` green; guard infra tests with build tags.
</specifics>

<deferred>
## Deferred Ideas

Third-party crate/CRAN vendoring beyond baked-in libs (v2). The full README/deploy docs + broader CI matrix (Phase 7) — though adding the new images to a `make build-images` + the abuse/CI matrix can be noted for Phase 7. gVisor/Fly runners (v2).
</deferred>
