---
phase: 02-sandbox-hardening-runner
plan: 02
subsystem: publisher
tags: [soketi, pusher, output, chunking, seq, tdd]
dependency_graph:
  requires: [internal/config, internal/keys, packages/contract/gen/go/wire]
  provides: [internal/publisher]
  affects: []
tech_stack:
  added: [github.com/pusher/pusher-http-go/v5 (already in go.mod)]
  patterns: [triggerer interface for testability, per-job mutex-guarded seq counter, byte-budget chunking]
key_files:
  created:
    - internal/publisher/publisher.go
    - internal/publisher/publisher_test.go
    - internal/publisher/doc.go
  modified: []
decisions:
  - "maxEventBytes = 8 KB (8192 bytes) to leave ~2 KB headroom under soketi's 10 KB limit"
  - "triggerer interface injected at construction so tests never need a live soketi"
  - "seq counter is shared across Stdout and Stderr per job (one ordered stream for the client)"
  - "splitChunk operates on byte boundaries with UTF-8 rune-boundary adjustment"
  - "pusherClientInfoFromConfig exported (package-level) for test assertions on Host/Secure mapping"
metrics:
  duration: "~10 minutes"
  completed: "2026-06-02T20:11:54Z"
  tasks_completed: 2
  files_created: 3
  files_modified: 0
---

# Phase 02 Plan 02: Soketi/Pusher Publisher Summary

Soketi output publisher with env-config creds, monotonic per-job seq, and sub-10KB chunking.

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | Publisher with env creds + monotonic seq + per-event API | 9f29f0c | publisher.go, doc.go |
| 2 | Chunk output under the soketi event-size limit | 9f29f0c | publisher.go (extended) |

## What Was Built

### internal/publisher/publisher.go

- `Publisher` struct holding a `triggerer` interface (real impl: `*pusher.Client` via `pusherTriggerer` adapter) and a mutex-guarded `map[string]int` for per-job sequence counters.
- `New(cfg config.Config) (*Publisher, error)` constructs a `pusher.Client` with `Host = "<SoketiHost>:<SoketiPort>"`, `Secure = SoketiUseTLS`, `AppID`/`Key`/`Secret` from config. No env reads in this package.
- `Stage(jobID, phase)`, `Stdout(jobID, chunk)`, `Stderr(jobID, chunk)`, `Result(jobID, ev)` — four public methods matching the contract event shapes.
- `splitChunk(chunk string) []string` — splits a chunk so each `wire.OutputChunkEvent{Chunk, Seq}` JSON-serialises to <= `maxEventBytes` (8 KB). Verified by marshalling with a worst-case Seq before emitting.
- `maxEventBytes = 8 * 1024` constant documented with rationale (2 KB headroom for soketi's 10 KB limit).
- `pusherClientInfoFromConfig(cfg) clientInfo` exported for test assertions on Host/Secure mapping.

### internal/publisher/publisher_test.go

13 tests covering:
- Channel naming (`private-run-<jobId>`), event names (`stage`/`stdout`/`stderr`/`result`), payload shapes
- Monotonic seq: stdout→stderr→stdout for one job yields seq 1,2,3; two jobs have independent counters
- Config-to-client mapping: `Host == "host:port"`, `Secure == SoketiUseTLS`
- Chunking: small chunk = 1 event; oversized chunk = N>1 events, each serialised ≤ maxEventBytes; concatenation reconstructs input byte-for-byte; seq continues across chunked sends

### internal/publisher/doc.go

Package overview with trust-boundary note (output-only, no env reads, no credential logging) and concurrency note.

## Test Results

```
go test ./internal/publisher/ -count=1
PASS
ok  github.com/teovillanueva/code-runner/internal/publisher  0.343s
13/13 tests pass
```

```
go test ./internal/publisher/ -run Chunk -count=1
PASS
ok  github.com/teovillanueva/code-runner/internal/publisher  0.217s
```

```
go build ./...
(exit 0 — no errors)
```

```
grep -q 'os.Getenv' internal/publisher/*.go
PASS: no os.Getenv in publisher
```

## Deviations from Plan

None — plan executed exactly as written.

The TDD gate was followed:
1. `test(02-02)` commit: doc.go + publisher_test.go (failing — RED)
2. `feat(02-02)` commit: publisher.go (all 13 tests pass — GREEN)

## Threat Model Coverage

| Threat ID | Disposition | Evidence |
|-----------|-------------|---------|
| T-02-05 (cred disclosure) | mitigated | No os.Getenv or hardcoded creds; New() accepts Config only; never logs Secret |
| T-02-06 (event size DoS) | mitigated | maxEventBytes=8KB constant; splitChunk verified by json.Marshal check per piece |
| T-02-07 (inbound soketi tampering) | accepted | Package is output-only; documented in doc.go trust-boundary section |

## Self-Check: PASSED

Files exist:
- FOUND: internal/publisher/publisher.go
- FOUND: internal/publisher/publisher_test.go
- FOUND: internal/publisher/doc.go

Commits exist:
- FOUND: d3f4400 (RED gate)
- FOUND: 9f29f0c (GREEN)
