---
phase: 07-oss-release-deployment
plan: 01
subsystem: docs
tags: [readme, documentation, api-reference, deployment, oss]

# Dependency graph
requires:
  - phase: 06-language-fanout
    provides: "Four language packages (python-3.12, rust-1.83, r-4.4, sqlite-3), docker-compose, Makefile with build-images/e2e/abuse targets, wire contract schema"
provides:
  - "Complete OSS README covering quickstart, full /v1/* API contract, channel-auth trust boundary, deployment-per-target, add-a-language guide, env var reference, contributing + make-targets table"
  - "DOCS-01: Quickstart (clone → make build-images → docker compose up → make e2e)"
  - "DOCS-02: All 8 /v1/* endpoints + /health documented with exact request/response shapes from routes"
  - "DOCS-03: Deployment (dev/prod/future) accurate per PROJECT.md Key Decisions"
  - "DOCS-04: Add-a-language guide with manifest field reference, Rust (compiled) + SQLite (non-general) examples"
  - "CHAN-01: Channel auth HMAC responsibility, trust boundary, optional /v1/channel-auth helper"
affects:
  - "Any contributor adding a language (DOCS-04 is the guide)"
  - "Self-hosters integrating code-runner (API Reference is the contract)"

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "README as single source of truth for self-hosters: all commands verified against Makefile"
    - "API contract documented exactly from routes/* and wire.schema.json — no invented fields"
    - "Channel auth HMAC pattern documented, secret boundary enforced in prose"

key-files:
  created: []
  modified:
    - README.md

key-decisions:
  - "Wrote all sections in a single complete README pass rather than multiple partial edits — avoids merge conflicts and ensures structural coherence"
  - "Removed internal .planning/PROJECT.md link from FlyMachinesRunner section — README must stand alone for a self-hoster"
  - "Deployment section uses exact language from docs/scaling.md and PROJECT.md Key Decisions — no paraphrase"

patterns-established:
  - "API Reference: one subsection per endpoint, consistent method/path/auth/request/responses format"
  - "Output events documented with exact JSON shapes from wire.schema.json $defs"
  - "Manifest fields table covers all required fields from wire.schema.json Manifest definition"

requirements-completed: [DOCS-01, DOCS-02, DOCS-03, DOCS-04, CHAN-01]

# Metrics
duration: 25min
completed: 2026-06-03
---

# Phase 7 Plan 01: OSS README Summary

**Complete self-hostable project README: quickstart (clone→run), full /v1/* API contract with exact wire shapes, channel-auth HMAC trust boundary, worker-node deployment model, manifest-driven add-a-language guide with Rust/SQLite examples**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-06-03T00:00:00Z
- **Completed:** 2026-06-03T00:25:00Z
- **Tasks:** 3
- **Files modified:** 1 (README.md)

## Accomplishments

- Rewrote README from 180-line partial to 613-line complete OSS project document
- Documented all 8 `/v1/*` endpoints + `/health` with exact method, auth, request body, and all response codes cross-checked against `apps/api/src/routes/*`
- All four soketi output events (`stage`, `stdout`, `stderr`, `result`) documented with exact payload shapes from `packages/contract/schema/wire.schema.json`
- Deployment section accurately describes worker-node scaling unit, gVisor `--runtime=runsc` upgrade path, FlyMachinesRunner v2 trade-offs per PROJECT.md Key Decisions
- Add-a-language guide with all manifest fields (including nullable `compile`), Rust (compiled) and SQLite (non-general-purpose) as worked examples — zero core changes assertion
- Channel-auth section: upstream HMAC responsibility, trust boundary, optional `ENABLE_CHANNEL_AUTH` helper
- Env var reference table for all 15 vars from `.env.example`
- `make contract-check` passed: no drift
- `make build-images` succeeded: all 4 language images built (python:3.12, rust:1.83, r:4.4, sqlite:3)
- All 8 make targets referenced in README confirmed to exist in Makefile

## Task Commits

1. **Task 1: Rewrite README — what-it-is, quickstart, API contract, channel-auth, env reference** - `ea6702e` (docs)
2. **Task 2: Deployment, add-a-language, contributing** - _(included in Task 1 commit — all sections written together for structural coherence)_
3. **Task 3: Validate README commands** - `76b8aee` (docs)

## Files Created/Modified

- `/Users/teovillanueva/code-runner/README.md` — Complete rewrite: 518 net insertions, 85 deletions. Sections: What it is (arch/data-flow/trust boundary/three-clocks), Quickstart (DOCS-01), API Reference (DOCS-02), Channel Auth (CHAN-01), Environment Variables, Deployment (DOCS-03), Adding a Language (DOCS-04), Contributing + Make targets, Interactive Flow, Safety Gate, License.

## Decisions Made

- Wrote all plan sections in a single README pass (Task 1 commit) rather than splitting Task 1 content from Task 2 content via separate Edit calls. This avoids structural fragmentation and ensures heading hierarchy is coherent. Task 2 commit was the validation-only fix.
- Removed the internal `.planning/PROJECT.md` link from the FlyMachinesRunner section (found during Task 3 validation). The README must stand alone for a self-hoster with no access to the planning directory.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Removed .planning/ internal path from README**
- **Found during:** Task 3 (validation)
- **Issue:** The FlyMachinesRunner section referenced `[PROJECT.md Key Decisions](.planning/PROJECT.md)` — an internal path not accessible to self-hosters
- **Fix:** Removed the link; the prose description already conveys the information
- **Files modified:** README.md
- **Verification:** `grep -q '.planning/' README.md` returned non-zero (not found)
- **Committed in:** `76b8aee` (Task 3 commit)

---

**Total deviations:** 1 auto-fixed (Rule 1 - broken internal link)
**Impact on plan:** Minor fix; no scope change. The removed link was supplementary to already-present prose.

## Issues Encountered

- Task 2 content (Deployment, Adding a Language, Contributing) was written in the same Write call as Task 1 content — the plan intended these as separate Edit operations. This was more efficient and produced a coherent document. The Task 2 verification gates all pass against the committed README.

## Validation Results

| Check | Result |
|-------|--------|
| `make contract-check` | PASS — no drift |
| `make build-images` (python, rust, r, sqlite) | PASS — all 4 images built |
| All 8 make targets in Makefile | PASS |
| No `.planning/` paths in README | PASS (fixed in Task 3) |
| All 8 `/v1/*` endpoints documented | PASS |
| `/health` documented | PASS |
| All 4 output events (stage/stdout/stderr/result) | PASS |
| Response codes 202/429/401 present | PASS |
| Env vars EXECUTOR_API_TOKEN/REDIS_URL/SOKETI_APP_SECRET/MAX_QUEUE_DEPTH/ENABLE_CHANNEL_AUTH | PASS |
| README >= 300 lines | PASS (613 lines) |

## Known Stubs

None — all content is accurate against shipped code.

## Threat Flags

None — no new network endpoints or trust-boundary changes. SOKETI_APP_SECRET handling documented as env-only, never persisted (T-07-01 mitigated). All endpoint auth requirements documented exactly from routes (T-07-02 mitigated).

## Next Phase Readiness

- README complete; self-hosters can follow the quickstart to a running interactive execute
- API contract documented accurately against shipped routes — integrators can build against it
- Channel-auth trust boundary documented — upstream apps know their responsibility
- Phase 07-02 (CI matrix) already complete; OSS release readiness satisfied

## Self-Check: PASSED

- README.md exists: FOUND
- Task 1 commit ea6702e: FOUND
- Task 3 commit 76b8aee: FOUND
- No .planning/ paths: VERIFIED
- All section headings present: VERIFIED

---
*Phase: 07-oss-release-deployment*
*Completed: 2026-06-03*
