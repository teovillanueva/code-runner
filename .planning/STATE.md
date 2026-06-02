# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-06-02)

**Core value:** Run untrusted code in a hardened, resource-bounded sandbox with a live interactive stdin session and reliable real-time output — without ever leaking a container, a subscription, or a session slot.
**Current focus:** Phase 1 — Foundation & Manifest Schema

## Current Position

Phase: 1 of 7 (Foundation & Manifest Schema)
Plan: 0 of TBD in current phase
Status: Ready to plan
Last activity: 2026-06-02 — Roadmap created (7 phases, all v1 requirements mapped)

Progress: [░░░░░░░░░░] 0%

## Performance Metrics

**Velocity:**
- Total plans completed: 0
- Average duration: — min
- Total execution time: 0.0 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| - | - | - | - |

**Recent Trend:**
- Last 5 plans: —
- Trend: —

*Updated after each plan completion*

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Roadmap]: Build bottom-up — prove interactive Python E2E (stdin + three clocks) before language fan-out
- [Roadmap]: Lock load-bearing interfaces (Runner/Sandbox, Queue, StdinTransport) in Phase 1 so Docker→gVisor and pub/sub→Streams are later swaps, never core rewrites
- [Roadmap]: Lifecycle/cleanup hardening (Phase 4) precedes scale work (Phase 5) — leaks compound under concurrency
- [Roadmap]: DEV-01/02/03 (compose + TS stub + E2E script) land in Phase 3 where the first end-to-end run happens

### Pending Todos

None yet.

### Blockers/Concerns

- Phase 2 needs deeper research during planning: seccomp allowlist per language, moby v28 HostConfig field names, cgroup v1/v2 detection
- Phase 3 needs empirical validation: per-language output buffering under non-TTY pipes
- Phase 5 needs a decision: LMOVE reliable-list vs Stream consumer group; heartbeat TTL sizing
- Abuse suite must run on Linux CI, not only macOS dev (cgroup v2-in-VM hides v1 prod behavior)

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| *(none)* | | | |

## Session Continuity

Last session: 2026-06-02
Stopped at: ROADMAP.md and STATE.md created; REQUIREMENTS.md traceability updated
Resume file: None
