---
phase: 08-distributed-observability
plan: 01
subsystem: observability
tags: [opentelemetry, w3c-trace-context, traceparent, wire-contract, json-schema, zod, go, tdd]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: JSON-Schema wire contract (single source of truth) + TS/zod/Go codegen + drift gate
provides:
  - Optional W3C traceparent/tracestate on JobSpec across TS types, zod validators, and Go structs
  - Executable cross-language trace_id byte-format equivalence proof (Go extract round-trip)
  - TS contract test asserting traceparent optionality (backward-compatible no-op)
  - Functional contract drift gate (make contract-check now actually regenerates)
affects: [08-02-worker-trace-extract, 08-api-sdk-trace-inject, distributed-observability]

# Tech tracking
tech-stack:
  added:
    - "go.opentelemetry.io/otel v1.44.0 (propagation + trace) promoted to a direct require"
  patterns:
    - "Trace carrier rides on JobSpec as two flat top-level W3C-named optional strings (D-11/D-12), NOT a nested object and NOT on ControlMessage/StdinMessage"
    - "Cross-seam W3C propagation via propagation.TraceContext{} + propagation.MapCarrier keyed on the W3C header names"
    - "Untrusted traceparent fails closed: absent/malformed yields an invalid SpanContext (fresh trace), never panics"

key-files:
  created:
    - internal/worker/trace_test.go
    - apps/api/test/traceparent-contract.test.ts
    - .planning/phases/08-distributed-observability/08-01-SUMMARY.md
  modified:
    - packages/contract/schema/wire.schema.json
    - packages/contract/gen/go/wire/wire.gen.go
    - packages/contract/gen/ts/types.ts
    - packages/contract/gen/ts/schemas.ts
    - Makefile
    - go.mod

key-decisions:
  - "D-11: trace carrier on JobSpec only (not ControlMessage/StdinMessage)"
  - "D-12: two flat optional top-level strings traceparent + tracestate matching W3C header names, not a nested object"
  - "D-04 (descope): OBS-05 Prometheus /metrics pull endpoint + admin port dropped; export is OTLP push only; worker stays HTTP-server-free"
  - "Fields left out of JobSpec.required so old specs remain valid (backward-compatible no-op when OTEL is off)"

patterns-established:
  - "Pattern 1: cross-language trace correlation proven by an executable test BEFORE any SDK is wired (Wave 0 de-risking)"
  - "Pattern 2: self-contained test-only extract helper mirrors the planned production propagator wiring, so the test is the contract 08-02 must satisfy"
  - "Pattern 3: untrusted W3C identifiers crossing the Redis seam fail closed to a fresh trace"

requirements-completed: [OBS-02]

# Metrics
duration: 18min
completed: 2026-06-03
---

# Phase 8 Plan 01: Cross-Seam Trace Carrier Summary

**Optional W3C traceparent/tracestate added to JobSpec across TS+zod+Go, with an executable Go round-trip proving cross-language trace_id byte-format equivalence and a TS test asserting the field is an optional, backward-compatible no-op.**

## Performance

- **Duration:** ~18 min
- **Started:** 2026-06-03T13:40:00Z
- **Completed:** 2026-06-03T13:52:00Z
- **Tasks:** 2
- **Files modified:** 10 (4 contract, 2 new tests, Makefile, go.mod, schema, summary)

## Accomplishments
- `JobSpec` now carries optional W3C `traceparent` + `tracestate` strings, regenerated cleanly across Go structs (`*string`, `omitempty`), TS types (`traceparent?: string`), and zod (`.optional()`) with the drift gate green.
- A Go extract round-trip test proves a known TS-format traceparent decodes to a `SpanContext` whose `TraceID().String()` equals the embedded 16-byte trace_id — the headline cross-language acceptance criterion, de-risked before any SDK wiring.
- Security fail-closed behavior is encoded: absent (nil) and malformed/oversized traceparent values yield an invalid `SpanContext` with no panic (threat T-08-01 / RESEARCH Security V5).
- TS contract test confirms the zod `JobSpec` parses specs with the field absent (backward-compatible) and present (value preserved), and that it is optional but typed as a string.

## Task Commits

Each task was committed atomically:

1. **Task 1: Add traceparent/tracestate to JobSpec + regenerate** - `49f3e66` (feat)
2. **Deviation (Rule 1): fix Makefile pnpm filter so the drift gate actually runs** - `9f3767d` (fix)
3. **Task 2: Cross-language extract round-trip (Go) + TS optionality test** - `b1379d4` (test)

_Note: this is the RED/GREEN test slice; 08-02 wires the real extract+link at `runJobFromSpec` against the contract these tests encode._

## Files Created/Modified
- `packages/contract/schema/wire.schema.json` - Added two optional string properties (`traceparent`, `tracestate`) to `JobSpec.properties`; NOT added to `required`.
- `packages/contract/gen/go/wire/wire.gen.go` - Regenerated: `Traceparent *string` / `Tracestate *string` (optional pointers).
- `packages/contract/gen/ts/types.ts` - Regenerated: `traceparent?: string` / `tracestate?: string`.
- `packages/contract/gen/ts/schemas.ts` - Regenerated: zod `.optional()` on both fields.
- `internal/worker/trace_test.go` - Go extract round-trip + fail-closed tests; self-contained propagator helper mirroring 08-02.
- `apps/api/test/traceparent-contract.test.ts` - vitest contract test for zod JobSpec optionality + value preservation.
- `Makefile` - Corrected the pnpm filter (`@code-runner/contract` → `@teovilla/code-runner-contract`) in `contract` + `contract-check`.
- `go.mod` - `go mod tidy` promoted `go.opentelemetry.io/otel` from indirect to a direct require.

## Decisions Made
- Implemented D-11 (carrier on JobSpec only) and D-12 (two flat top-level W3C-named optional strings, not a nested object) exactly as specified.
- Kept both fields out of `JobSpec.required` so pre-phase-08 JobSpec messages still validate (backward-compatible no-op when OTEL is off).
- Recorded D-04 descope of OBS-05 (no Prometheus `/metrics`, no admin port); nothing in this plan adds an HTTP server to the worker.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Corrected the pnpm filter in the Makefile contract targets**
- **Found during:** Task 1 (running `make contract-check` as the acceptance criterion)
- **Issue:** The contract package was renamed to `@teovilla/code-runner-contract`, but the `contract` and `contract-check` Makefile targets still filtered on the old name `@code-runner/contract`. `pnpm --filter @code-runner/contract generate` matched no project and silently skipped codegen, so `make contract-check`'s `git diff --exit-code` passed without ever regenerating — a non-functional drift gate giving false confidence.
- **Fix:** Pointed both targets at the actual package name so codegen genuinely runs before the diff check.
- **Files modified:** `Makefile`
- **Verification:** `make contract-check` now regenerates (`✓ generated ...`) and exits 0 with no drift after the committed contract change.
- **Committed in:** `9f3767d`

**Note on `files_modified` paths:** The plan's frontmatter listed `packages/contract/gen/ts/wire.ts` and `packages/contract/gen/go/wire/wire.go`. The actual codegen output paths are `gen/ts/types.ts` + `gen/ts/schemas.ts` and `gen/go/wire/wire.gen.go`. Codegen is authoritative (never hand-edited), so the real generated files were modified; this is a naming discrepancy in the plan, not a behavioral deviation.

---

**Total deviations:** 1 auto-fixed (1 bug, Rule 1)
**Impact on plan:** The fix was necessary for the plan's own acceptance criterion (`make contract-check` green) to be meaningful. No scope creep — one-line correctness fix to the verification tooling.

## Issues Encountered
- The worktree had no `node_modules` (deps live in the shared store). Ran `pnpm install --frozen-lockfile` to materialize already-declared workspace deps from the store (0 downloaded, all reused) — no new/untrusted package introduced.

## Known Stubs
None — the Go extract helper is intentionally test-only scaffolding (documented as such), encoding the contract that 08-02's production extract+link must satisfy. This is the planned RED/GREEN seam for Wave 0, not an unresolved stub.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- The wire carrier and the cross-language correlation contract are proven and committed; 08-02 can wire the real `propagation.TraceContext{}.Extract` + span link at `runJobFromSpec` (worker.go ~line 230) against `internal/worker/trace_test.go`.
- No blockers. The byte-format equivalence assumption — the phase's headline risk — is resolved GREEN.

## Self-Check: PASSED

All claimed files exist on disk and all four commits (`49f3e66`, `9f3767d`, `b1379d4`, `535269e`) are present in the branch history. Working tree clean.

---
*Phase: 08-distributed-observability*
*Completed: 2026-06-03*
