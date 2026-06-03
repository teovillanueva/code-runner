# Phase 9: Artifacts & Pullable Run Output - Pattern Map

**Mapped:** 2026-06-03
**Files analyzed:** 16 new/modified surfaces
**Analogs found:** 15 / 16 (one new-but-shaped-by-multiple-analogs: `S3Store`)

This phase is schema-first (`wire.schema.json` is the single source of truth; `gen/**` is never hand-edited) and follows three established swap-seam precedents (`Runner`, `StdinTransport`, `DockerSandbox` extension). Almost every new file has a strong in-repo analog. Concrete excerpts below.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `packages/contract/schema/wire.schema.json` (add `Artifact`, `RunResult`, `Limits` caps, `ExecuteRequest.collectOutput`) | config (schema) | transform | existing `$defs` (`ResultEvent`, `Limits`, `ExecuteRequest`) in same file | exact |
| `packages/contract/src/index.ts` (add `keys.jobOutput`, `events.artifact`) | config | transform | existing `keys` / `events` exports in same file | exact |
| `internal/keys/keys.go` (add `JobOutputKey`, `EventArtifact`) | utility | transform | existing `JobStatusKey` / `EventResult` in same file | exact |
| `internal/artifactstore/store.go` (new `ArtifactStore` interface) | service (seam) | file-I/O | `internal/runner/runner.go` `Runner`, `internal/stdintransport/transport.go` `StdinTransport` | exact (seam shape) |
| `internal/artifactstore/s3.go` (new `S3Store`, minio-go) | service | file-I/O | `internal/runner/docker.go` `DockerSocketRunner` (env-driven client ctor + boot-time setup) | role-match |
| `internal/runner/docker.go` (add `ReadArtifacts` via `CopyFromContainer`) | service (extension) | file-I/O / streaming | existing `copyFilesToContainer` (`CopyToContainer` + tar) + `CPUReader()`/`Limits()` methods, same file | exact |
| `internal/worker/worker.go` (`DockerSandbox` ext, `Sinks` accumulation, artifact read in teardown, `RunResult` persist) | service | event-driven / request-response | existing `DockerSandbox` interface + `sync.Once` teardown + `Sinks` closures, same file | exact |
| `internal/jobstore/jobstore.go` (add `WriteRunResult` w/ TTL, `ReadRunResult`) | model (persistence) | CRUD | existing `WriteStatus` / `ReadStatus` / `ReadSpec`, same file | exact |
| `internal/publisher/publisher.go` (add `Artifact` metadata trigger) | service | pub-sub | existing `Result()` trigger + `triggerOutput` chunking | exact |
| `internal/config/config.go` + `apps/worker/main.go` (S3 + 3 TTL env vars, ordering invariant) | config | transform | existing `Config` struct + `configFromEnv()` env parsing | exact |
| `apps/api/src/routes/jobs.ts` (add `GET /v1/jobs/:id/output`) | route | request-response | existing `GET /v1/jobs/:id` handler, same file | exact |
| `packages/code-runner-sdk-node/src/client.ts` (add `getOutput`) | controller (client) | request-response | existing `getJob()` method, same file | exact |
| `packages/code-runner-react/src/useCodeRunnerJob.ts` (expose `artifacts`) | hook | event-driven | existing soketi event binding (`onResult`/`onStdout`), same file | exact |
| `languages/python-3.12/Dockerfile` (matplotlib + `MPLBACKEND=Agg`) | config (image) | batch | existing `pip install` layer, same file | exact |
| `languages/r-4.4/Dockerfile` (graphics stack + `R_DEFAULT_PACKAGES` reconcile) | config (image) | batch | existing `install.packages` + `ENV R_DEFAULT_PACKAGES`, same file | exact |
| `docker-compose.yml` + `.env.example` (MinIO service + S3/TTL vars) | config (infra) | — | existing `redis`/`soketi` services + `.env.example` blocks | exact |
| `deploy/fly/worker/fly.toml` + `docs/deploy-fly.md` (Tigris wiring) | config (deploy) | — | existing `[env]`/secrets block in same `fly.toml` | exact |

## Pattern Assignments

### `packages/contract/schema/wire.schema.json` (config/schema, transform)

**Analog:** existing `$defs` in the same file (`ResultEvent` at lines 203-217, `Limits` at 8-22, `ExecuteRequest` at 83-95).

**Pattern — `$defs` object with `additionalProperties:false` + `required`** (lines 203-217, the closest shape for `RunResult`):
```jsonc
"ResultEvent": {
  "title": "ResultEvent",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "exitCode": { "type": ["integer", "null"] },
    "signal": { "type": ["string", "null"] },
    "timedOut": { "type": "boolean", "description": "..." },
    "idleTimedOut": { "type": "boolean", "description": "..." },
    "truncated": { "type": "boolean", "description": "..." },
    "durationMs": { "type": "integer" }
  },
  "required": ["exitCode", "signal", "timedOut", "idleTimedOut", "truncated", "durationMs"]
}
```
- `RunResult` (R2) = `ResultEvent` fields **plus** `stdout` (string), `stderr` (string), `artifacts` (array of `Artifact`), `artifactsTruncated` (boolean). Reuse the exact `["integer","null"]` / `["string","null"]` nullable shape for `exitCode`/`signal`.
- `Artifact` (R1, D-01 URL-only): flat object `{ name, mimeType, bytes, url }`, all required, no `oneOf`/discriminator — model on `FileInput` (lines 37-47) which is a flat `{name, content}` required object. **Avoid `oneOf`** per D-01 (go-jsonschema fragility).
- Reference arrays via `"$ref": "#/$defs/Artifact"` exactly as `ExecuteRequest.files` does (`"items": { "$ref": "#/$defs/FileInput" }`, line 91).

**Pattern — `Limits` numeric caps with defaults** (lines 13-21): add `maxArtifacts` (default 20) and `maxArtifactBytes` (default 4 MB) to BOTH `Limits` (lines 8-22) and `LimitsOverride` (lines 23-36). Caps use `"type": "integer", "minimum": 1`. Add to `Limits.required`; `LimitsOverride` has no `required` (all optional).

**Pattern — `ExecuteRequest` optional boolean** (lines 83-95): add `collectOutput` as `{ "type": "boolean" }`, NOT in `required` (opt-in, R3). `JobSpec` (lines 108-130) must carry it through to the worker — add `collectOutput` there too (the API resolves spec → worker reads it).

**After editing: `make contract` (regen) then `make contract-check` (drift gate). NEVER hand-edit `packages/contract/gen/**`.** Add a contract unit test mirroring `packages/contract/test/manifest.test.ts` — round-trip an `Artifact` and a `RunResult` with ≥1 artifact through the generated zod validator (R1/R2 acceptance).

---

### `packages/contract/src/index.ts` (config, transform)

**Analog:** existing `keys` and `events` const exports, same file (lines 18-33).

**Pattern — `keys` builder + `events` map** (lines 18-33):
```typescript
export const keys = {
  jobQueue: "jobs:queue",
  jobStatus: (jobId: string): string => `job:${jobId}:status`,
  jobSpec: (jobId: string): string => `job:${jobId}:spec`,
} as const;

export const events = {
  stage: "stage",
  stdout: "stdout",
  stderr: "stderr",
  result: "result",
} as const;
```
- Add `jobOutput: (jobId: string): string => \`job:${jobId}:output\`` to `keys` (D-09 suggested name `job:<id>:output`).
- Add `artifact: "artifact"` to `events`.
- **Lockstep requirement:** the same two additions go into `internal/keys/keys.go` (see below). The file header comment already states "kept in lockstep with the Go worker's internal/keys".

---

### `internal/keys/keys.go` (utility, transform)

**Analog:** existing `JobStatusKey` and the event-name const block, same file (lines 14-31).

**Pattern — key builder + event const** (lines 14-31):
```go
const (
	EventStage  = "stage"
	EventStdout = "stdout"
	EventStderr = "stderr"
	EventResult = "result"
)

func JobStatusKey(jobID string) string {
	return fmt.Sprintf("job:%s:status", jobID)
}
```
- Add `EventArtifact = "artifact"` to the const block.
- Add `JobOutputKey(jobID string) string { return fmt.Sprintf("job:%s:output", jobID) }`.
- Keep the `// Matches keys.jobOutput(id) in index.ts.` doc-comment convention used by every existing builder.

---

### `internal/artifactstore/store.go` (service / seam, file-I/O) — NEW

**Analog:** `internal/runner/runner.go` `Runner` interface (lines 71-76) and `internal/stdintransport/transport.go` `StdinTransport` (lines 24-42). Both are "interface + concrete impl selected at boot" seams.

**Pattern — package-doc explaining the swap seam** (`transport.go` lines 1-12):
```go
// Package stdintransport defines the StdinTransport interface that abstracts
// how stdin chunks are routed ...
//
// MVP implementation: Redis pub/sub ...
// Planned upgrade ... swap the concrete impl ... without changing any caller
// ... This swap is the sole reason the interface exists.
```
- Mirror this exactly: `// Package artifactstore defines the ArtifactStore interface ... shipped impl: S3Store (minio-go) ... the seam is preserved for future backends (D-02).`

**Pattern — minimal interface** (`runner.go` lines 71-76):
```go
type Runner interface {
	Create(ctx context.Context, spec wire.JobSpec) (Sandbox, error)
}
```
- `ArtifactStore` should expose a small surface: `Put(ctx, jobID, name, mimeType string, data []byte) (url string, err error)` returning the presigned GET URL (R7), and `EnsureLifecycle(ctx) error` for the boot-time bucket-lifecycle rule (R15). Keep it SDK-agnostic — return `string` URLs, accept `[]byte`, no minio types leak through the interface.
- Compile-time assertion idiom from `docker.go` line 121: `var _ ArtifactStore = (*S3Store)(nil)`.

---

### `internal/artifactstore/s3.go` (service, file-I/O) — NEW

**Analog:** `internal/runner/docker.go` `NewDockerSocketRunner` (lines 159-189) — an env/config-driven client constructor with boot-time setup; plus `apps/worker/main.go` `configFromEnv` for env reading.

**Pattern — config-driven constructor that fails closed** (`docker.go` lines 159-189): `NewDockerSocketRunner` reads cfg, builds the moby client with option list, reads a profile at construction, returns `(*T, error)`. Mirror for `NewS3Store(cfg config.Config) (*S3Store, error)`:
- Read `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_ENDPOINT_URL_S3` / `AWS_REGION` / `BUCKET_NAME` from cfg, with `ARTIFACT_S3_*` overrides (D-03). All env enters via `config.Config` — **never read `os.Getenv` in this package** (publisher.go line 125 sets this precedent: "this package never reads env vars directly").
- When S3 env is unset, the worker constructs **no** `S3Store` (nil) and artifact capture is disabled but output pull still works (D-04, R7). The nil-store branch is handled in the worker, mirroring `if w.store != nil` guards throughout `worker.go`.

**minio-go is a NEW dependency** (not yet in `go.mod`; only `go-redis/v9 v9.20.0` present). Per CLAUDE.md prefer minio-go over aws-sdk-go-v2 (D-02). Use Context7 to confirm the current `github.com/minio/minio-go/v7` API for `PresignedGetObject`, `PutObject`, and `SetBucketLifecycle` (R7/R15) before coding.

**Boot-time setup pattern** (`EnsureLifecycle`, R15): mirror how `main.go` does one-time boot wiring (Redis Ping at lines 119-124, manifest Load at 96-99) — call `EnsureLifecycle` once during worker boot in `run()`, log on error, fail fast if `objectTTL < presignedURLTTL` (R15 ordering invariant; enforce at config load — see config.go below).

---

### `internal/runner/docker.go` — `ReadArtifacts` (service extension, file-I/O/streaming)

**Analog (in same file):** `copyFilesToContainer` (lines 444-469) is `CopyToContainer` + tar **write**; `ReadArtifacts` is the mirror image — `CopyFromContainer` + tar **read**. Also `CPUReader()` (627-629) / `Limits()` (633-635) are the precedent for adding a method that the worker reaches via the `DockerSandbox` type assertion.

**Pattern — tar marshalling around a Copy call** (lines 444-469):
```go
func (r *DockerSocketRunner) copyFilesToContainer(ctx context.Context, containerID string, files []wire.FileInput) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		content := []byte(f.Content)
		hdr := &tar.Header{ Name: filepath.Base(f.Name), Mode: 0644, Size: int64(len(content)) }
		// ... WriteHeader + Write
	}
	tw.Close()
	return r.cli.CopyToContainer(ctx, containerID, sandboxWorkDir, &buf, container.CopyToContainerOptions{})
}
```
- `ReadArtifacts` reverses it: call `cli.CopyFromContainer(ctx, containerID, sandboxWorkDir)` → returns an `io.ReadCloser` tar stream → iterate with `tar.NewReader` → for each `*tar.Header` of `Typeflag == tar.TypeReg`, read bytes. **The `archive/tar`, `bytes`, `io` imports are already present** (docker.go lines 14-23).
- It is a method on `*dockerSandbox` (the running handle, which holds `containerID` + `spec`), NOT on `DockerSocketRunner` — it reads from a live container. Place it next to `CPUReader()`/`Limits()` (lines 620-635).
- **Workspace-diff (D-05/R4):** the worker passes the input file-name set (snapshot at job creation from `spec.Files[].Name`); `ReadArtifacts` returns only `TypeReg` entries whose basename is NOT in the input set, NOT `.compile_ready` (the `compileRunMarker` constant, line 86 = `/workspace/.compile_ready`), and NOT compile outputs (e.g. a Rust `prog` binary). The exclusion set is the input names + markers.
- **Constraint (R5/D-07):** `ReadArtifacts` MUST be callable BEFORE `Cleanup()`. `Cleanup` (589-618) and `Kill` (567-583) both force-remove the volume with `RemoveVolumes:true` — reading after races a gone volume. Confirmed: the worker calls it inside teardown before `sb.Cleanup()`.

**`DockerSandbox` extension (worker.go lines 144-148):**
```go
type DockerSandbox interface {
	runner.Sandbox
	CPUReader() runner.CPUUsageFunc
	Limits() wire.Limits
}
```
- Add `ReadArtifacts(ctx context.Context, exclude map[string]bool) ([]CapturedArtifact, error)` (or similar) to this interface. The worker already type-asserts `sb.(DockerSandbox)` (worker.go lines 677-681). The core `runner.Sandbox` interface (runner.go 85-142) stays SDK-agnostic — **do not add `ReadArtifacts` there** (D-06).

---

### `internal/worker/worker.go` (service, event-driven / request-response)

**Analog (in same file):** the `sync.Once` teardown closure (lines 527-574), the `Sinks` closures (lines 731-742), the `if ds, ok := sb.(DockerSandbox); ok` type-assert (lines 677-681), and the `publishError`/`toResultEvent` helpers (766-791).

**Pattern — output accumulation in `Sinks` (D-08, R6):** the current `Sinks` discard after publishing:
```go
sinks := session.Sinks{
	Stdout: func(b []byte) {
		if pubErr := w.pub.Stdout(jobID, string(b)); pubErr != nil { ... }
	},
	Stderr: func(b []byte) {
		if pubErr := w.pub.Stderr(jobID, string(b)); pubErr != nil { ... }
	},
}
```
- When `spec.CollectOutput`, the closures ALSO append to two capped `bytes.Buffer`s captured in the closure. Reuse the EXISTING `outputKb` budget + shared `truncated` semantics — the session pump (`internal/session/pump.go` lines 84-130) already enforces the cap and only forwards within-budget bytes to the sink, so appending in the sink gives you exactly the soketi-streamed bytes (one truncation semantics, R6/D-08). **Do not introduce a second cap.** The `Sinks` struct (`interactive.go` 20-25) is unchanged — accumulation lives entirely in the worker's closures.

**Pattern — `sync.Once` teardown ordering (D-07, R5):** current teardown (lines 528-573) does, in order: record terminal → close subs → remove owned job → release slot → publish result → write status → `sb.Cleanup()`. Insert the artifact + RunResult work **before `sb.Cleanup()`** (line 570):
```go
teardown := func(result runner.Result, state wire.JobState) {
	teardownOnce.Do(func() {
		// ... existing: recordTerminal, close subs, RemoveOwnedJob, releaseSlot, pub.Result, WriteStatus ...

		// NEW (only when spec.CollectOutput): read artifacts BEFORE Cleanup.
		if spec.CollectOutput {
			runResult := assembleRunResult(result, stdoutBuf, stderrBuf, truncated)
			if ds, ok := sb.(DockerSandbox); ok && w.artifacts != nil {
				captured, _ := ds.ReadArtifacts(ctx, inputNameSet) // before Cleanup (D-07)
				// apply caps (maxArtifacts/maxArtifactBytes → artifactsTruncated), upload each
				for _, a := range capped {
					url, _ := w.artifacts.Put(ctx, jobID, a.Name, a.MimeType, a.Data)
					runResult.Artifacts = append(runResult.Artifacts, wire.Artifact{...Url: url})
					w.pub.Artifact(jobID, ...) // metadata-only soketi event (R8)
				}
			}
			w.store.WriteRunResult(ctx, jobID, runResult, w.cfg.RunResultTTL) // R6
		}

		if cleanErr := sb.Cleanup(); cleanErr != nil { ... } // unchanged — stays LAST
	})
}
```
- Follow the existing best-effort error style: `log.Warn(...)` on every sub-step, never fail the job (R5 acceptance: a 25-file job still terminates with its real exit code).
- The `DockerSandbox` type-assert + nil-store guard mirrors lines 677-681 and the pervasive `if w.store != nil` pattern.
- Add `artifacts artifactstore.ArtifactStore` and `cfg.RunResultTTL time.Duration` to the `Worker` struct (lines 181-198) and `Config` (161-177); thread through `New`/`NewWithTransport` (200-251) and `apps/worker/main.go` construction (line 167). A nil `artifacts` field = artifacts disabled but RunResult (stdout/stderr) still persists (D-04).

---

### `internal/jobstore/jobstore.go` (model, CRUD)

**Analog (in same file):** `WriteStatus` (lines 73-83) and `ReadStatus` (88-102).

**Pattern — JSON SET (here WITH TTL) + GET-or-ErrNotFound:**
```go
func (s *Store) WriteStatus(ctx context.Context, st wire.JobStatus) error {
	b, _ := json.Marshal(st)
	if err := s.client.Set(ctx, keys.JobStatusKey(st.JobId), b, 0).Err(); err != nil { ... }
	return nil
}
func (s *Store) ReadStatus(ctx context.Context, jobID string) (wire.JobStatus, error) {
	b, err := s.client.Get(ctx, keys.JobStatusKey(jobID)).Bytes()
	if errors.Is(err, redis.Nil) { return wire.JobStatus{}, fmt.Errorf("...: %w", ErrNotFound) }
	// ... unmarshal
}
```
- `WriteRunResult(ctx, jobID, rr wire.RunResult, ttl time.Duration)` = `WriteStatus` but pass `ttl` (not `0`) as the 3rd `Set` arg → `keys.JobOutputKey(jobID)`. The existing status/spec writes use `0` (no expiry); the RunResult key is the FIRST keyed write with a real TTL (R6/D-09).
- `ReadRunResult(ctx, jobID) (wire.RunResult, error)` = `ReadStatus` against `keys.JobOutputKey`, returning the existing `ErrNotFound` sentinel (line 26) on `redis.Nil`. The API route maps this to 404 (R9). `IsNotFound` (106-108) already exists for callers.

---

### `internal/publisher/publisher.go` (service, pub-sub)

**Analog (in same file):** `Result()` (lines 179-181) for a single metadata trigger, and `triggerOutput` (187-197) + `maxEventBytes` (line 78) for the size discipline.

**Pattern — single-event trigger** (lines 179-181):
```go
func (p *Publisher) Result(jobID string, ev wire.ResultEvent) error {
	return p.t.Trigger(keys.ChannelForJob(jobID), keys.EventResult, ev)
}
```
- Add `Artifact(jobID string, a wire.Artifact) error` = `p.t.Trigger(keys.ChannelForJob(jobID), keys.EventArtifact, a)`. One trigger per captured artifact at end of run (R8).
- **Size constraint (R8, < 10 KB):** the `artifact` event is metadata-only (`name, mimeType, bytes, url`) and never carries file bytes, so it is inherently small — no chunking needed (unlike `triggerOutput`). The existing `maxEventBytes = 8 * 1024` constant (line 78) documents soketi's ~10 KB ceiling; an `Artifact` JSON (a presigned URL + 3 small fields) is well under it. No `splitChunk` machinery required.

---

### `internal/config/config.go` + `apps/worker/main.go` (config, transform)

**Analog (in same files):** the `Config` struct fields (config.go 15-70) + `Default()` (93-105), and `configFromEnv()` (main.go 212-266).

**Pattern — env field + parse-with-fallback** (main.go 238-242):
```go
if v := os.Getenv("WORKER_MAX_SANDBOXES"); v != "" {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		cfg.MaxSandboxes = n
	}
}
```
- Add to `Config`: `S3Endpoint`, `S3Bucket`, `S3AccessKeyID`, `S3SecretAccessKey`, `S3Region` (read from `AWS_*` with `ARTIFACT_S3_*` overrides, D-03), `RunResultTTL time.Duration` (default ~600 s, R6/D-11), `PresignedURLTTL time.Duration` (default 24 h, D-11), `S3ObjectTTL time.Duration` (default a small number of days, R15/D-11).
- Add corresponding `os.Getenv` blocks in `configFromEnv()`. For the `AWS_*`-then-`ARTIFACT_S3_*`-override pattern: read `AWS_ENDPOINT_URL_S3` first, then overwrite with `ARTIFACT_S3_ENDPOINT` if set.
- **Ordering invariant (R15/D-11):** enforce `S3ObjectTTL >= PresignedURLTTL` at config load — fail fast. There is no precedent for a validating config loader in this repo (`Default()` + `configFromEnv` never error today), so add a `Validate() error` method on `Config` and call it in `main.go run()` after `configFromEnv()`, returning the error (boot-fail pattern matches the existing `return fmt.Errorf(...)` style at main.go 98/115/143).

---

### `apps/api/src/routes/jobs.ts` (route, request-response)

**Analog (in same file):** the entire `GET /v1/jobs/:id` handler (lines 8-27).

**Pattern — Redis-GET-only, 404-on-missing, 500-on-malformed** (lines 9-26):
```typescript
app.get("/v1/jobs/:id", async (c) => {
  const jobId = c.req.param("id");
  const redis = getRedis();
  const statusJson = await redis.get(keys.jobStatus(jobId));
  if (!statusJson) {
    return c.json({ error: `Job not found: ${jobId}` }, 404);
  }
  let status: unknown;
  try { status = JSON.parse(statusJson); }
  catch { return c.json({ error: "Internal error: malformed job status" }, 500); }
  return c.json(status, 200);
});
```
- `GET /v1/jobs/:id/output` is byte-for-byte this shape against `keys.jobOutput(jobId)` instead of `keys.jobStatus(jobId)` (R9, D-09). 404 covers all three cases (unknown / not-collected / expired) because each leaves the key absent — `!outputJson` is the single check.
- Bearer auth (R9, 401) is applied centrally by `app.use("/v1/*", bearerAuth)` in `apps/api/src/app.ts` line 28 — the route needs NO per-handler auth code. Register the route inside the existing `registerJobsRoutes(app)` function (no new file).
- The API never calls the worker (the file header comment "API-11: only Redis GET, never calls worker" still holds).

---

### `packages/code-runner-sdk-node/src/client.ts` (client, request-response)

**Analog (in same file):** `getJob()` (lines 69-72); error mapping in `throwForStatus` (154-189); `NotFoundError` (errors.ts 28-33).

**Pattern — typed GET helper** (lines 69-72):
```typescript
/** GET /v1/jobs/:id — fetch job status. 404 -> NotFoundError. */
getJob(id: string): Promise<JobStatus> {
  return this.request<JobStatus>("GET", `/v1/jobs/${encodeURIComponent(id)}`);
}
```
- Add `getOutput(id: string): Promise<RunResult>` = same shape against `/v1/jobs/${encodeURIComponent(id)}/output`. Import `RunResult` from `@teovilla/code-runner-contract` (alongside the existing `JobStatus` import, lines 7-12).
- 404 → `NotFoundError` is already handled centrally by `throwForStatus` (lines 171-172) and the bearer is auto-attached in `request()` (line 114) — `getOutput` needs no extra error or auth code (R12).

---

### `packages/code-runner-react/src/useCodeRunnerJob.ts` (hook, event-driven)

**Analog (in same file):** the event-binding lifecycle in `useEffect` (lines 75-118) and the `result` state pattern (`onResult`, lines 101-104).

**Pattern — bind a soketi event → accumulate state → unbind on cleanup** (lines 91-116):
```typescript
const [result, setResult] = useState<ResultEvent | null>(null);
const onResult = (data: ResultEvent) => { setResult(data); setStatus("done"); };
ch.bind(EVENTS.result, onResult);
// cleanup:
ch.unbind(EVENTS.result, onResult);
```
- Add an `artifacts` state array + an `onArtifact` handler that appends each `Artifact` event (R13). Bind `EVENTS.artifact` (add `artifact: "artifact"` to the local `EVENTS` const, lines 19-24, mirroring contract `events`), reset `artifacts` in the effect's reset block (lines 77-83), unbind in cleanup.
- Add `artifacts: Artifact[]` to `UseCodeRunnerJobResult` (lines 38-46) and the returned object (line 126). Import `Artifact` type from the contract (lines 8-13). Browser fetches each `url` directly — no bearer (R13, trust boundary unchanged).

---

### `languages/python-3.12/Dockerfile` (image, batch)

**Analog (in same file):** the `pip install` layer (lines 32-35) and the `ENV` block (22-26).

**Pattern — extend the baked-deps layer** (lines 32-35):
```dockerfile
RUN pip install --no-cache-dir \
    numpy==2.2.6 \
    pandas==2.2.3 \
    requests==2.32.3
```
- Add `matplotlib==<pinned>` to this list (R10). Pin a known-good version (use Context7 / PyPI to confirm a current 3.x compatible with numpy 2.2). All deps baked at build — runtime stays `--network=none`.
- Add `ENV MPLBACKEND=Agg` alongside the existing `ENV PYTHONUNBUFFERED=1` block (R10) so headless `savefig` works. **No `plt.show()` shim** (D-10) — `show()` under Agg is intentionally a no-op. Document the save-to-cwd convention (`plt.savefig('plot.png')`) in a comment, matching the file's existing heavy-comment style.

---

### `languages/r-4.4/Dockerfile` (image, batch)

**Analog (in same file):** `install.packages` layer (64-70) and the `ENV R_DEFAULT_PACKAGES=base` line + its rationale comment (72-86).

**Pattern — reconcile `R_DEFAULT_PACKAGES`** (lines 72-86): the current `ENV R_DEFAULT_PACKAGES=base` disables grDevices/graphics to silence the popen/seccomp `utils` noise. For R11, make a file device (`png()`) usable WITHOUT reintroducing that noise:
- Add `grDevices` (and `graphics`) back to `R_DEFAULT_PACKAGES` (e.g. `base,grDevices,graphics`) — these do NOT trigger the `utils`/`popen("which uname")` path that caused the original EPERM noise (only `utils` does). Validate empirically that `png('chart.png'); plot(1:3); dev.off()` works under the hardened sandbox without stderr noise (R11 acceptance + the file's own comment about which packages cause the popen path).
- If the base `r-base:4.4.2` image lacks the cairo/X libs for a PNG device, add the apt graphics libs in a layer before the `install.packages` call (mirror the existing `RUN Rscript -e "..."` install style). Keep `R_DEFAULT_DEVICE` consistent (currently `pdf`, line 37) — the user explicitly opens `png()` so the default device is moot. **No auto-device shim** (D-10/R11). All baked at build, no `--network`.

---

### `docker-compose.yml` + `.env.example` (infra config)

**Analog (in same file):** the `redis` service (compose lines 26-35) and `soketi` service (42-62); the `.env.example` section blocks (lines 1-40).

**Pattern — a healthchecked service on the `code-runner` network** (compose 26-35):
```yaml
redis:
  image: redis:7-alpine
  restart: unless-stopped
  networks:
    - code-runner
  healthcheck:
    test: ["CMD", "redis-cli", "ping"]
    interval: 5s
    timeout: 3s
    retries: 10
```
- Add a `minio` service (R14): pinned image, `restart: unless-stopped`, `networks: [code-runner]`, a healthcheck (MinIO exposes `/minio/health/live`), env for root user/password and bucket, and a named volume for data. Wire the worker service's environment to point `AWS_ENDPOINT_URL_S3` at `http://minio:9000` and set `BUCKET_NAME` + creds (mirror how the `worker`/`api` services already read `REDIS_URL`/`SOKETI_*` env, compose lines 72-74, 125-126). The worker may need a `depends_on: minio` (the file uses `depends_on` with healthcheck conditions for redis/soketi — see lines 97-99).

**Pattern — documented env block** (`.env.example` 11-21, 22-39): each var has a comment explaining purpose + default. Add a new `# ── Artifacts / object storage ──` section documenting: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `BUCKET_NAME` (+ `ARTIFACT_S3_*` overrides), the three TTLs (`RUN_RESULT_TTL`/presigned-URL expiry/`ARTIFACT_S3_OBJECT_TTL`), the ordering invariant, the ephemeral-handoff lifecycle note, and the 1-day lifecycle-granularity caveat (R14/R15/D-11).

---

### `deploy/fly/worker/fly.toml` + `docs/deploy-fly.md` (deploy config)

**Analog (in same file):** the `[env]` block + the `# Secrets (fly secrets set ...)` comment (fly.toml lines 13-30).

**Pattern — non-secret config in `[env]`, secrets via `fly secrets`** (fly.toml 13-30):
```toml
[env]
  REDIS_URL = "redis://code-runner-redis.internal:6379"
  SOKETI_HOST = "code-runner-soketi.flycast"
  # ...
  # Secrets (fly secrets set -a code-runner-worker):
  #   SOKETI_APP_SECRET
  #   GHCR_TOKEN — only needed if ...
```
- Tigris injects `BUCKET_NAME`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINT_URL_S3`, `AWS_REGION` automatically on `fly storage create` — these land as **secrets on the worker app**, so they belong in the `# Secrets` comment list, NOT the `[env]` block (R16). Non-secret TTL config (`RUN_RESULT_TTL`, `ARTIFACT_S3_OBJECT_TTL`, presigned-URL expiry) goes in `[env]` like `WORKER_MAX_SANDBOXES`. The API app needs NO S3 creds (R16) — only the worker uploads/signs.
- `docs/deploy-fly.md`: add a `fly storage create` step + the credential-mapping table, in the same prose style as the existing doc (read it before editing — not loaded here, but it pairs 1:1 with the per-service `fly.toml` files).

## Shared Patterns

### Contract codegen drift gate
**Source:** CLAUDE.md "Environment & Build Notes" + `packages/contract/schema/wire.schema.json`.
**Apply to:** every contract change (Artifact, RunResult, Limits caps, collectOutput, events.artifact).
- Edit ONLY `wire.schema.json` → run `pnpm contract` (or `make contract`) → gate with `make contract-check` (regenerates + `git diff --exit-code`). The Go worker imports `github.com/teovillanueva/code-runner/packages/contract/gen/go/wire`; TS imports `@teovilla/code-runner-contract`. NEVER hand-edit `gen/**`.

### Swap-seam (interface + boot-selected impl)
**Source:** `internal/runner/runner.go` (Runner), `internal/stdintransport/transport.go` (StdinTransport).
**Apply to:** `internal/artifactstore` (`ArtifactStore` + `S3Store`).
- Package doc explains why the interface exists (future backends). Compile-time assertion `var _ ArtifactStore = (*S3Store)(nil)` (docker.go line 121 idiom). Interface stays SDK-agnostic (no minio types in signatures).

### Env-only config (no `os.Getenv` outside main / config)
**Source:** `internal/publisher/publisher.go` line 125 ("this package never reads env vars directly"); `apps/worker/main.go` `configFromEnv`.
**Apply to:** `S3Store`, all new TTL/S3 settings.
- All env enters via `config.Config`, parsed once in `configFromEnv()` with `strconv` + `n > 0` guards. Constructors take `cfg config.Config`. Boot-time validation (`Config.Validate()`) enforces the `objectTTL >= presignedURLTTL` invariant and fails fast (R15/D-11).

### Single `sync.Once` teardown, best-effort sub-steps
**Source:** `internal/worker/worker.go` lines 527-574.
**Apply to:** artifact read + RunResult persist (both inside the same `teardownOnce.Do`, BEFORE `sb.Cleanup()`).
- Every sub-step is best-effort (`log.Warn` on error, never fail the job). `sb.Cleanup()` stays the LAST call. `if w.store != nil` / `if ds, ok := sb.(DockerSandbox); ok` guard idioms (lines 539, 677-681) gate optional paths; a nil `ArtifactStore` disables capture but keeps RunResult persistence (D-04).

### Redis JSON persistence (SET + GET-or-ErrNotFound)
**Source:** `internal/jobstore/jobstore.go` `WriteStatus`/`ReadStatus`.
**Apply to:** `WriteRunResult` (the FIRST write with a non-zero TTL) / `ReadRunResult`.
- `json.Marshal` → `client.Set(ctx, key, b, ttl)`; `client.Get(...).Bytes()` → `redis.Nil` maps to the existing `ErrNotFound` sentinel → API 404.

### Bearer auth applied centrally
**Source:** `apps/api/src/app.ts` line 28 (`app.use("/v1/*", bearerAuth)`).
**Apply to:** `GET /v1/jobs/:id/output`.
- New `/v1/*` routes inherit bearer auth automatically (R9 401). No per-handler auth code.

### Typed SDK error mapping
**Source:** `packages/code-runner-sdk-node/src/client.ts` `throwForStatus` (154-189) + `errors.ts`.
**Apply to:** `getOutput` (404 → `NotFoundError`).
- The central `request()` path attaches the bearer and maps statuses; new methods are one-liners delegating to `request<T>(...)`. No new error class needed (`NotFoundError` exists).

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| (none — `S3Store` is the only fully-new impl, but its constructor/boot-setup/env shape is well-covered by `DockerSocketRunner` + `configFromEnv` analogs) | — | — | — |

The only genuinely new external dependency is **minio-go** (`github.com/minio/minio-go/v7`), not yet in `go.mod`. No in-repo S3/object-storage code exists, so confirm the minio-go API (`PutObject`, `PresignedGetObject`, `SetBucketLifecycle`/`PutBucketLifecycleConfiguration`) via Context7 / current docs before implementing `s3.go` and the `EnsureLifecycle` boot step. The *structure* (env-driven constructor returning `(*T, error)`, boot-time one-time setup, SDK-agnostic interface) is fully analog-covered.

## Metadata

**Analog search scope:** `internal/runner`, `internal/stdintransport`, `internal/keys`, `internal/publisher`, `internal/worker`, `internal/jobstore`, `internal/session`, `internal/config`, `apps/worker`, `apps/api/src`, `packages/contract`, `packages/code-runner-sdk-node`, `packages/code-runner-react`, `languages/*`, `deploy/fly/worker`, `docker-compose.yml`, `.env.example`.
**Files scanned:** 16 analog files read in full or in targeted sections.
**Pattern extraction date:** 2026-06-03
