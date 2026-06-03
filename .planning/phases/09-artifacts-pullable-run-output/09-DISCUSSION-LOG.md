# Phase 9: Artifacts & Pullable Run Output - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-06-03
**Phase:** 09-artifacts-pullable-run-output
**Areas discussed:** Artifact wire shape, S3 client + env mapping, Artifact-read seam, Image shims (+ artifact capture mechanism)

---

## Artifact wire shape

| Option | Description | Selected |
|--------|-------------|----------|
| Flat, optional `contentBase64?` + `url?` | Both nullable, exactly-one, clean codegen across targets | |
| Discriminated union (`storage` tag) | Semantically explicit, risks messy go-jsonschema/zod output | |
| **URL-only** (`{name, mimeType, bytes, url}`) | User-driven: object storage is the only backend; drop inline base64 | ✓ |

**User's choice:** "sería solo url mejor no?" → URL-only.
**Notes:** Accepted the implication that object storage becomes mandatory for the artifacts feature (output pull still works without S3). Overrides SPEC R1/R7/R8/R13; `InlineStore` dropped.

---

## S3 client + env mapping

| Option | Description | Selected |
|--------|-------------|----------|
| **minio-go, reads `AWS_*`** | Lighter; presigned + SetBucketLifecycle; reads Tigris-injected `AWS_*` with `ARTIFACT_S3_*` override; zero-translation on Fly | ✓ |
| aws-sdk-go-v2, own env | Canonical AWS SDK, heavier; explicit `ARTIFACT_S3_*` needs mapping on Fly | |

**User's choice:** minio-go, reads `AWS_*` (recommended).
**Notes:** Tigris (`fly storage create`) wiring stays zero-translation.

---

## Artifact-read seam

| Option | Description | Selected |
|--------|-------------|----------|
| **`DockerSandbox` extension method** | `ReadArtifacts(...)` next to `CPUReader`/`Limits`, via `CopyFromContainer`; core interface stays SDK-agnostic | ✓ |
| Core `runner.Sandbox` method | Uniform but couples the interface; forces future runners to implement extraction | |

**User's choice:** `DockerSandbox` extension (recommended). Follows the established precedent.

---

## Image shims + artifact capture mechanism

This area evolved across several turns from "how to dump plots" into "how to capture artifacts at all".

| Option | Description | Selected |
|--------|-------------|----------|
| Port edalef Lambda shims | Reuse proven `plt.show()` interception + R `png()`/`dev.off()` logic | |
| Build native shims | New matplotlib backend, no port | |
| Custom matplotlib backend (in image) | Official extension: `show()` → `savefig` to dir; transparent for student code | |
| **No shim — workspace-diff capture, relative names** | Worker captures new files in cwd `/workspace`; user saves with a relative name; zero per-language code, no magic path | ✓ |

**User's choice:** Workspace-diff capture (new files in cwd), users save with relative names.
**Notes / progression:**
- User first asked whether artifact capture could be done "a nivel worker sin implementación x lenguaje".
- Explained that `plt.show()` under headless `Agg` writes nothing — no syscall/LD_PRELOAD/seccomp hook can manufacture a file the library never writes; interception can only happen at the Python API layer.
- User then asked "¿no hay una manera de forzar los writes via syscall hook?" — confirmed not viable for the above reason.
- User's real concern surfaced: "no quiero que estén forzados a saber en qué ruta guardarlo" (don't want users to hardcode a magic path).
- Resolution: capture = diff of the working directory (cwd `/workspace`). Users save with a relative name (`savefig('plot.png')`); the worker picks up any new file. No `artifactsDir` field, no shim, no magic path. Overrides SPEC R4/R5/R10/R11.

---

## Claude's Discretion

- Redis key name for the persisted `RunResult` (suggested `job:<id>:output`), the `collectOutput` field name, and the artifact filename collision policy — left to planner/researcher.

## Deferred Ideas

- Blocking `POST /v1/run` endpoint.
- Env-gated webhooks (push delivery of `RunResult`).
- Transparent `plt.show()` capture via an opt-in custom matplotlib backend (future per-language enhancement).
- Inline base64 `InlineStore` (zero-config no-bucket path) — dropped in favor of mandatory object storage.
