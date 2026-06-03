---
phase: 04
slug: abuse-suite-safety-validation
status: verified
nyquist_compliant: true
wave_0_complete: true
created: 2026-06-03
---

# Phase 04 — Validation Strategy

> Per-phase validation contract. Retroactive audit: this phase **is** the verification
> backbone — its deliverable is the abuse suite that gates the language fan-out.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | `go test` (build tag `abuse`) |
| **Config file** | none — `Makefile` target `abuse` + `.github/workflows/abuse.yml` |
| **Quick run command** | `go test -tags=abuse -run xxxNoMatch ./internal/worker/` (compile-check) |
| **Full suite command** | `make abuse` (Docker cgroup v2 + redis:7 on 6381 + `executor/python:3.12`) |
| **CI command** | `.github/workflows/abuse.yml` → `make abuse` on `ubuntu-latest` (real cgroup v2) |
| **Estimated runtime** | ~60–120 s on Linux CI |

---

## Sampling Rate

- **Local pre-merge:** `make abuse` (requires Docker cgroup v2)
- **CI (authoritative):** `abuse.yml` runs on every `push` + `pull_request`
- **Gate:** "abuse / abuse" must be green before any language fan-out PR merges (TEST-08)
- **Note:** behavior diverges on macOS Docker Desktop; the Linux CI run is the real cgroup OOM/CPU gate (TEST-07)

---

## Per-Task Verification Map

| Requirement | Test | Type | Automated Command | File Exists | Status |
|-------------|------|------|-------------------|-------------|--------|
| TEST-01 (fork bomb contained by `--pids-limit`, worker survives) | `TestAbuseForkBomb` (`internal/worker/abuse_test.go:413`) | integration (full worker path) | `make abuse` | ✅ | ✅ green (CI) |
| TEST-02 (OOM killed by memory cap, worker survives) | `TestAbuseOOM` (`abuse_test.go:497`) | integration | `make abuse` | ✅ | ✅ green (CI) |
| TEST-03 (infinite loop killed by wall clock, `timedOut=true`) | `TestAbuseInfiniteLoop` (`abuse_test.go:576`) | integration | `make abuse` | ✅ | ✅ green (CI) |
| TEST-04 (stdin-blocked killed by idle clock, `idleTimedOut=true`) | `TestAbuseIdleBlockedStdin` (`abuse_test.go:697`) | integration | `make abuse` | ✅ | ✅ green (CI) |
| TEST-05 (read-to-EOF clean exit after `stdin_close`, exitCode 0) | `TestAbuseEofCleanExit` (`abuse_test.go:759`) | integration | `make abuse` | ✅ | ✅ green (CI) |
| TEST-06 (giant output `truncated=true`, worker keeps draining) | `TestAbuseGiantOutput` (`abuse_test.go:833`) | integration | `make abuse` | ✅ | ✅ green (CI) |
| (bonus) CPU clock evasion (compute hidden behind stdin caught by CPU clock) | `TestAbuseCpuClockEvasion` (`abuse_test.go:629`) | integration | `make abuse` | ✅ | ✅ green (CI) |
| TEST-07 (suite runs on Linux CI, real cgroup v2) | `.github/workflows/abuse.yml` (`runs-on: ubuntu-latest`, `make abuse`) | CI workflow | push + PR trigger | ✅ | ✅ wired |
| TEST-08 (abuse suite gates the language fan-out) | `abuse.yml` runs on PRs + README "Safety Gate" required-check note (`README.md:546,598`) | CI gate + doc | required status check | ✅ | ✅ wired |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

Compile-check verified locally this audit: `go test -tags=abuse -run xxxNoMatch ./internal/worker/` → `ok` (suite builds clean). Full red/green behavior is exercised by the Linux CI gate, not macOS dev (per TEST-07 rationale).

---

## Wave 0 Requirements

Existing infrastructure covers all phase requirements — the abuse suite IS the infrastructure (no framework install needed; `go test` + Docker + the `make abuse` target already present).

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Enabling "abuse / abuse" as a required status check on `main` | TEST-08 | GitHub branch-protection setting is repo-owner configuration, not code | Repo owner: Settings → Branches → add "abuse / abuse" as a required status check on `main` |

All executable phase behaviors (TEST-01..07) have automated verification. TEST-08's enforcement has one repo-config step that lives outside the codebase (documented above).

---

## Validation Sign-Off

- [x] All requirements have an automated test or CI-wired verification
- [x] Sampling continuity: no 3 consecutive requirements without automated verify
- [x] Wave 0 covers all MISSING references (none — pre-existing infra)
- [x] No watch-mode flags
- [x] Linux CI gate exercises real cgroup OOM/CPU behavior (TEST-07)
- [x] `nyquist_compliant: true` set in frontmatter

## Validation Audit 2026-06-03

| Metric | Count |
|--------|-------|
| Requirements (TEST-01..08) | 8 |
| Covered (automated/CI) | 8 |
| Partial | 0 |
| Missing | 0 |
| Manual-only residual | 1 (branch-protection toggle, repo-config) |

**Approval:** approved 2026-06-03
