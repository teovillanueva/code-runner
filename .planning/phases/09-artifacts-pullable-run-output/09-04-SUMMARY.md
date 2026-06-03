---
phase: 09-artifacts-pullable-run-output
plan: 04
subsystem: worker
tags: [artifacts, runresult, soketi, redis-ttl, docker-copyfromcontainer, workspace-diff]

# Dependency graph
requires:
  - phase: 09-01
    provides: "wire.Artifact / wire.RunResult / Limits.MaxArtifacts+MaxArtifactBytes / ExecuteRequest.CollectOutput contract types; keys.JobOutputKey + keys.EventArtifact"
  - phase: 09-02
    provides: "artifactstore.ArtifactStore interface + worker.Config.Artifacts and worker.Config.RunResultTTL (Config-borne, no New change)"
provides:
  - "dockerSandbox.ReadArtifacts (CopyFromContainer + tar read) on the widened DockerSandbox extension interface"
  - "Publisher.Artifact metadata-only soketi event"
  - "jobstore.WriteRunResult(ttl) / ReadRunResult against job:<id>:output"
  - "Worker teardown: stdout/stderr accumulation, artifact read+cap+upload+event before Cleanup, TTL'd RunResult persist"
affects: [09-05 (GET /v1/jobs/:id/output reads job:<id>:output via ReadRunResult), SDK getOutput, React artifacts]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Workspace-diff artifact capture: CopyFromContainer + tar read with a basename exclude set (inputs + .compile_ready + compile-output binary)"
    - "Read-before-Cleanup ordering inside the single sync.Once teardown (D-07)"
    - "One-truncation-semantics output accumulation in the Sinks closures (reuse outputKb budget, no second cap)"
    - "Redis JSON write WITH a real TTL (first keyed write that expires)"

key-files:
  created:
    - internal/worker/worker_artifacts_test.go
  modified:
    - internal/runner/docker.go
    - internal/worker/worker.go
    - internal/jobstore/jobstore.go
    - internal/publisher/publisher.go

key-decisions:
  - "compileOutputBasename derives the binary basename from the compile argv '-o <target>' token, falling back to the first run-argv token; added to the exclude set so a compiled binary is never an artifact (R4)"
  - "RunResult.Artifacts initialised to a non-nil empty slice so the persisted JSON is [] (not null) on zero captures"
  - "Caps applied as keep-first-within-(MaxArtifacts AND MaxArtifactBytes); any drop sets ArtifactsTruncated=true; job keeps its real exitCode"
  - "artifactSandbox test fake overrides CPUReader to return runner.CPUUsageFunc (alias) so the sb.(DockerSandbox) assertion succeeds — the embedded scriptedSandbox returns the distinct named session.CPUUsageFunc"
  - "Artifact tests use the repo's dialOrSkip live-Redis convention (TEST_REDIS_URL); the worker run path stays in-process, the store is the only Redis touch-point"

patterns-established:
  - "DockerSandbox extension method reached via sb.(DockerSandbox) type assertion (next to CPUReader/Limits) — core runner.Sandbox stays SDK-agnostic (D-06)"
  - "Best-effort teardown sub-steps: nil store / capture failure / upload error log.Warn and never fail the job; sb.Cleanup() stays LAST"

requirements-completed: [R4, R5, R6, R8]

# Metrics
duration: ~25min
completed: 2026-06-03
---

# Phase 9 Plan 04: Worker Artifact Capture & Pullable Run Output Summary

**Wired workspace-diff artifact capture and TTL'd RunResult persistence into the worker's single sync.Once teardown — reading new /workspace files via CopyFromContainer BEFORE Cleanup, excluding inputs + .compile_ready + the compile-output binary, applying 20-file/4-MB caps, uploading via the Config-borne ArtifactStore, emitting metadata-only artifact events, and persisting accumulated stdout/stderr + artifacts to job:<id>:output — all gated by collectOutput and gracefully degrading to zero artifacts with a nil store.**

## Performance

- **Duration:** ~25 min
- **Tasks:** 3 / 3
- **Files created:** 1
- **Files modified:** 4

## Accomplishments

### Task 1 — ReadArtifacts + Artifact trigger + RunResult CRUD (commit 69c2aed)
- `internal/runner/docker.go`: added `CapturedArtifact{Name,MimeType,Data}` and `(*dockerSandbox).ReadArtifacts(ctx, exclude)` — the mirror of `copyFilesToContainer` (CopyFromContainer + `tar.NewReader`), returning only top-level `tar.TypeReg` entries whose basename is not excluded, with a defensive `.compile_ready` skip and `mime.TypeByExtension` MIME detection (fallback `application/octet-stream`). Added the `mime` import.
- `internal/worker/worker.go`: widened the `DockerSandbox` interface with `ReadArtifacts(...) ([]runner.CapturedArtifact, error)`.
- `internal/publisher/publisher.go`: added `Publisher.Artifact(jobID, wire.Artifact)` triggering `keys.EventArtifact` (metadata only, no chunking — well under maxEventBytes).
- `internal/jobstore/jobstore.go`: added `WriteRunResult(ctx, jobID, rr, ttl)` (SET `keys.JobOutputKey` with a real TTL) and `ReadRunResult` (GET → `redis.Nil` maps to `ErrNotFound`).

### Task 2 — Worker Sinks accumulation + teardown capture/persist (commit d190336)
- Output accumulation lives in the `Sinks` closures behind a `collectOutput` gate, appending the same within-budget bytes streamed to soketi (mutex-guarded `bytes.Buffer`s) — one truncation semantics, no second cap (D-08).
- Teardown inserts the capture/persist block BEFORE `sb.Cleanup()` (D-07): builds the `RunResult`, and when `w.cfg.Artifacts != nil` and `sb.(DockerSandbox)` asserts, computes the exclude set (`buildArtifactExcludeSet`: input basenames + `.compile_ready` + `compileOutputBasename(spec)`), calls `ReadArtifacts`, applies the `MaxArtifacts`/`MaxArtifactBytes` caps (drop excess → `ArtifactsTruncated=true`), uploads each via `w.cfg.Artifacts.Put`, attaches `wire.Artifact` + emits `w.pub.Artifact`, then persists via `w.store.WriteRunResult(..., w.cfg.RunResultTTL)`.
- All sub-steps best-effort (`log.Warn`, never fail the job); nil store/Artifacts persists stdout/stderr + zero artifacts; nothing written without `collectOutput`. `worker.New`/`NewWithTransport` unchanged; `apps/worker/main.go` untouched.

### Task 3 — Hermetic worker artifact tests (commit b39adb3)
- `internal/worker/worker_artifacts_test.go`: an `artifactSandbox` fake (records ReadArtifacts/Cleanup sequence, captures the exclude map, returns configurable `CapturedArtifact`s) + an in-memory `fakeArtifactStore`. Proves: read-before-Cleanup ordering; 2-PNG → exactly 2 artifacts (no truncation); 25-file → 20 artifacts + `ArtifactsTruncated=true` with the exitCode preserved; compiled-language exclude map contains the binary basename `app` (R4); no-`collectOutput` → `ReadRunResult` `ErrNotFound` + zero ReadArtifacts/Put; nil store → RunResult persists with zero artifacts, no panic.

## Verification

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./internal/worker/... ./internal/jobstore/... ./internal/runner/... ./internal/publisher/...` — green.
- `TEST_REDIS_URL=redis://localhost:6381 go test ./internal/worker/... -run TestArtifacts -v` — all 6 artifact tests PASS against live Redis (they SKIP cleanly when Redis is unreachable, matching the repo's `dialOrSkip` convention).
- `worker.New` signature unchanged (consumes `w.cfg.Artifacts` / `w.cfg.RunResultTTL`); `apps/worker/main.go` NOT modified (`git diff --stat` empty).
- Read-before-Cleanup confirmed by source order (ReadArtifacts at line 626, final `sb.Cleanup()` at line 676 in worker.go).

## Deviations from Plan

None — plan executed exactly as written. (PATTERNS.md suggested adding store/TTL to the Worker struct + New; the PLAN explicitly overrode this since plan 02 already placed them on Config — followed the PLAN: consumed `w.cfg.Artifacts`/`w.cfg.RunResultTTL`, no constructor change.)

## Notes for Downstream (09-05 / SDKs)

- The pull endpoint `GET /v1/jobs/:id/output` reads only `job:<id>:output` via `store.ReadRunResult`; `ErrNotFound` (absent/expired/not-collected) → 404.
- The `artifact` soketi event is best-effort and metadata-only; the authoritative artifact list is the pulled `RunResult.Artifacts`.
- Artifact tests are live-Redis gated (`TEST_REDIS_URL`, default `redis://localhost:6379`); CI/local must point at a reachable Redis to exercise (not skip) them.

## Self-Check: PASSED
