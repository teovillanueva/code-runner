---
phase: 01-foundation-wire-contract
plan: 01
subsystem: core
tags: [keys, manifest, contract, loader, validator]
dependency_graph:
  requires: []
  provides: [internal/keys, internal/manifest, languages/python-3.12/manifest.json]
  affects: [apps/worker, apps/api]
tech_stack:
  added: []
  patterns: [glob-based manifest discovery, sentinel errors, immutable limits merge]
key_files:
  created:
    - internal/keys/keys.go
    - internal/keys/keys_test.go
    - internal/manifest/manifest.go
    - internal/manifest/manifest_test.go
    - internal/manifest/testdata/valid/python-3.12/manifest.json
    - internal/manifest/testdata/malformed/bad-missing-field/manifest.json
    - internal/manifest/testdata/duplicate/python-a/manifest.json
    - internal/manifest/testdata/duplicate/python-b/manifest.json
    - languages/python-3.12/manifest.json
  modified: []
decisions:
  - "Used stdlib testing only (no testify) per plan spec"
  - "ErrNotFound and ErrDuplicate are sentinel errors wrappable with errors.Is"
  - "Registry uses bare language key (first version wins) plus language@version key for explicit version pinning"
  - "runtime.Caller(0) used in tests to derive absolute testdata path — works regardless of cwd"
metrics:
  duration: "~10 minutes"
  completed: "2026-06-02"
---

# Phase 1 Plan 01: Keys + Manifest Loader Summary

Go-side wire contract consumers: Redis/channel/event key package plus a glob-based manifest loader with validation, alias resolution, and immutable limits merge.

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | Verify committed contract + OSS scaffolding | (no new commit — verified intact) | Makefile, wire.schema.json, LICENSE, .env.example |
| 2 | internal/keys — Go mirror of contract keys/channels/events | 6dcafbe | internal/keys/keys.go, internal/keys/keys_test.go |
| 3 | internal/manifest — boot loader, validator, resolver, limits-merge | c739039 | internal/manifest/manifest.go, manifest_test.go, testdata/*, languages/python-3.12/manifest.json |

## Test Results

```
ok  github.com/teovillanueva/code-runner/internal/keys     0.190s
ok  github.com/teovillanueva/code-runner/internal/manifest 0.348s
```

All ten named manifest test cases pass:
- TestLoadValid
- TestLoadMalformedErrors
- TestLoadDuplicateErrors
- TestResolveByName
- TestResolveByAlias
- TestResolveByVersion
- TestResolveUnknownNotFound
- TestListContents
- TestMergeLimitsPartialOverride
- TestMergeLimitsDoesNotMutateBase

## Deviations from Plan

None — plan executed exactly as written.

## Known Stubs

None. The manifest loader is fully wired; no placeholder data flows to callers.

## Self-Check: PASSED

- internal/keys/keys.go: FOUND
- internal/keys/keys_test.go: FOUND
- internal/manifest/manifest.go: FOUND
- internal/manifest/manifest_test.go: FOUND
- languages/python-3.12/manifest.json: FOUND
- Commits 6dcafbe and c739039: FOUND in git log
