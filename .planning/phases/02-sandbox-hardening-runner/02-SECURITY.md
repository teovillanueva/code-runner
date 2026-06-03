---
phase: 02
slug: sandbox-hardening-runner
status: verified
threats_open: 0
asvs_level: default
created: 2026-06-03
---

# Phase 02 — Security

> Per-phase security contract: threat register, accepted risks, and audit trail.
> Scope: the sandboxed container **is** the untrusted thing; the hardening flags, the
> three clocks, the kill primitive, and the output boundary are the wall.

---

## Trust Boundaries

| Boundary | Description | Data Crossing |
|----------|-------------|---------------|
| untrusted code (sandbox) → host | The container is the untrusted thing; hardening flags are the wall | syscalls, resource pressure |
| worker → host docker socket | Root-equivalent; socket mounted ONLY in worker, NEVER into the sandbox | container lifecycle ops |
| sandbox → network | Must be none; no path to Redis/soketi/metadata | (denied — NetworkMode=none) |
| untrusted code → output pipes | stdout/stderr is attacker-influenced volume; must not OOM/block the worker | byte stream (capped) |
| untrusted code → CPU/wall budget | Code may hide compute behind interactivity; CPU clock is the real bound | compute time |
| worker → soketi | Output-only; worker triggers events, never reads | output events |
| config (env) → publisher creds | Secrets come from env config only, never hardcoded/logged | soketi app secret |

---

## Threat Register

| Threat ID | Category | Component | Disposition | Mitigation (evidence) | Status |
|-----------|----------|-----------|-------------|------------------------|--------|
| T-02-01 | DoS | output pump | mitigate | `internal/session/pump.go:58-98` shared `atomic.Int64` combined byte budget, always-drain to EOF past cap, `truncated` via CAS; wired `lifecycle.go:82-92` | closed |
| T-02-02 | DoS | clocks | mitigate | `internal/session/clocks.go` three independent goroutines: wall (21-31), idle (40-64), CPU (72-93, real cgroup usage) | closed |
| T-02-03 | EoP | seccomp profile | mitigate | `profiles/seccomp/runner.json` `defaultAction: SCMP_ACT_ERRNO`; ptrace/mount/umount2/bpf/keyctl/add_key/request_key/userfaultfd/kexec_load/perf_event_open/unshare/setns/clone3 absent from allowlist | closed |
| T-02-04 | Tampering | go module deps | accept | `go.mod` exact pins + `go.sum` integrity (132 lines) — see Accepted Risks | closed |
| T-02-05 | Info Disclosure | soketi creds | mitigate | `internal/publisher/publisher.go:60-81` creds from `config.Config` only; no `os.Getenv`/hardcode; Secret never logged; boundary `doc.go:6-12` | closed |
| T-02-06 | DoS | soketi event size | mitigate | `publisher.go:23` `maxEventBytes=8KB`; `splitChunk:150-226` verifies serialized size per piece | closed |
| T-02-07 | Tampering | inbound soketi data | accept | output-only boundary `doc.go:6-12`; no read/subscribe path — see Accepted Risks | closed |
| T-02-08 | EoP | HostConfig hardening | mitigate | `internal/runner/docker.go:230-284` one path: CapDrop ALL, no-new-privileges, seccomp, ReadonlyRootfs, non-root 65534 | closed |
| T-02-09 | Spoofing/Escape | NetworkMode | mitigate | `docker.go:232` `NetworkMode:"none"` unconditional; asserted live `docker_integration_test.go:86` | closed |
| T-02-10 | DoS | memory/pids/tmpfs | mitigate | `docker.go:274-279` Memory==MemorySwap (no swap), PidsLimit, NanoCPUs; sized tmpfs `/tmp` (183,254-256) | closed |
| T-02-11 | DoS | process tree | mitigate | `docker.go:505-512` Kill = ContainerKill + ContainerRemove(Force, RemoveVolumes); no bare-PID path | closed |
| T-02-12 | Tampering | docker socket exposure | mitigate | `docker.go` no Binds/docker.sock/`/var/run` in any mount; sandbox NetworkMode=none | closed |
| T-02-13 | Resource leak | container lifecycle | mitigate | `docker.go:518-547` sync.Once Cleanup force-removes container+volume; jobId label (224-226); funnelled `lifecycle.go:116-139` | closed |
| T-02-14 | DoS | test leak | mitigate | `internal/runner/testhelpers_test.go:127-153` `assertNoLeak` via label-filtered ContainerList | closed |
| T-02-15 | Repudiation | unverifiable hardening | mitigate | `docker_integration_test.go:80-140` ContainerInspect asserts ACTUAL applied flags | closed |
| T-02-16 | Tampering | accidental run without Docker | accept | `testhelpers_test.go:1` `//go:build docker` + `requireDocker` t.Skip — see Accepted Risks | closed |

*Status: open · closed*
*Disposition: mitigate (implementation required) · accept (documented risk) · transfer (third-party)*

---

## Accepted Risks Log

| Risk ID | Threat Ref | Rationale | Accepted By | Date |
|---------|------------|-----------|-------------|------|
| AR-02-01 | T-02-04 | docker/docker, pusher-http-go/v5, testify are well-known libraries pinned to exact versions in `go.mod` and checksummed in `go.sum` (Go module integrity). No private/unverified module sources. | secure-phase audit | 2026-06-03 |
| AR-02-02 | T-02-07 | soketi is output-only by design; the worker only triggers events, never reads/subscribes. `internal/publisher` exposes no inbound path. All trusted input enters via API/Redis. The architecture removes the inbound attack surface entirely. | secure-phase audit | 2026-06-03 |
| AR-02-03 | T-02-16 | The Docker integration suite is double-gated (`//go:build docker` tag + `requireDocker` runtime skip). Plain `go test ./...` cannot compile/run it, so non-Docker CI produces no false failures and no unintended container launches. | secure-phase audit | 2026-06-03 |

*Accepted risks do not resurface in future audit runs.*

---

## Security Audit Trail

| Audit Date | Threats Total | Closed | Open | Run By |
|------------|---------------|--------|------|--------|
| 2026-06-03 | 16 | 16 | 0 | gsd-security-auditor (opus) |

**Unregistered flags:** None. The `## Threat Flags` sections of 02-01..02-04 SUMMARY.md report no new network endpoints, auth paths, file-access patterns, or schema changes beyond the registered threats; seccomp/pump/clocks map to T-02-03/T-02-01/T-02-02.

---

## Sign-Off

- [x] All threats have a disposition (mitigate / accept / transfer)
- [x] Accepted risks documented in Accepted Risks Log
- [x] `threats_open: 0` confirmed
- [x] `status: verified` set in frontmatter

**Approval:** verified 2026-06-03
