// Code generated from schema/wire.schema.json by `pnpm contract`. DO NOT EDIT.

/**
 * Lifecycle state of a job.
 */
export type JobState = "queued" | "starting" | "running" | "done" | "killed" | "error";
export type ControlType = "start" | "kill" | "stdin_close";
export type StagePhase = "queued" | "compiling" | "running";

/**
 * Resource limits for a sandbox. The three clocks (wall/idle/cpu) plus memory, pids and output caps.
 */
export interface Limits {
  /**
   * Max total lifetime of the session in ms. Kills unconditionally.
   */
  wallTimeMs: number;
  /**
   * Max ms with no stdout and no stdin before the sandbox is killed.
   */
  idleMs: number;
  /**
   * Max accumulated CPU time in ms (cgroup), independent of wall-clock.
   */
  cpuMs: number;
  /**
   * Memory cap in MiB. memory == memory-swap (no swap).
   */
  memoryMb: number;
  /**
   * Max number of processes/threads (pids-limit).
   */
  pids: number;
  /**
   * Max combined stdout+stderr in KiB before truncation.
   */
  outputKb: number;
  /**
   * Max number of captured artifact files before excess is dropped (artifactsTruncated=true).
   */
  maxArtifacts: number;
  /**
   * Max total bytes across all captured artifacts before excess is dropped (artifactsTruncated=true).
   */
  maxArtifactBytes: number;
}
/**
 * Optional per-request override of a subset of Limits.
 */
export interface LimitsOverride {
  wallTimeMs?: number;
  idleMs?: number;
  cpuMs?: number;
  memoryMb?: number;
  pids?: number;
  outputKb?: number;
  maxArtifacts?: number;
  maxArtifactBytes?: number;
}
/**
 * A single source file submitted by the caller.
 */
export interface FileInput {
  /**
   * Relative path written into the sandbox workspace; may contain '/' subdirectories (e.g. data/input.csv). Must not escape the workspace; absolute paths and traversal are rejected.
   */
  name: string;
  /**
   * File content. Interpreted per `encoding`: utf8 text by default, or base64-encoded arbitrary bytes when encoding=base64.
   */
  content: string;
  /**
   * How content is encoded. utf8=text (default, back-compat); base64=arbitrary bytes.
   */
  encoding?: "utf8" | "base64";
}
/**
 * A language package manifest (languages/<lang-version>/manifest.json).
 */
export interface Manifest {
  language: string;
  version: string;
  aliases: string[];
  /**
   * Pre-built image with all libs baked in (no runtime dependency resolution).
   */
  image: string;
  /**
   * Main file name, e.g. main.py.
   */
  entrypoint: string;
  /**
   * Optional compile command (argv). null for interpreted languages.
   */
  compile: string[] | null;
  /**
   * Run command (argv).
   *
   * @minItems 1
   */
  run: [string, ...string[]];
  /**
   * Whether the language supports a live interactive stdin session.
   */
  interactive: boolean;
  /**
   * Optional zygote pre-import set: importable module/package names the zygote parent loads once at warm time so forked children share their pages copy-on-write. A NON-EMPTY array opts the language into the ZygoteRunner tier (interpreted, heavy-import languages like Python/R); absent or empty routes the language to the DockerSocketRunner tier (compiled or no-import languages like Rust/SQLite). Names are passed verbatim to the language agent (Python: importlib.import_module; R: library()).
   */
  preimport?: string[] | null;
  defaultLimits: Limits;
}
/**
 * Public language descriptor returned by GET /v1/languages.
 */
export interface LanguageInfo {
  language: string;
  version: string;
  aliases: string[];
  interactive: boolean;
}
/**
 * Body of POST /v1/execute.
 */
export interface ExecuteRequest {
  /**
   * Language name or alias.
   */
  language: string;
  /**
   * Optional explicit version; if omitted the only/most-recent match is used.
   */
  version?: string;
  /**
   * @minItems 1
   */
  files: [FileInput, ...FileInput[]];
  limits?: LimitsOverride;
  /**
   * Opt-in: when true, the worker accumulates stdout/stderr and captures workspace artifacts into a pullable RunResult (GET /v1/jobs/:id/output). Defaults to false.
   */
  collectOutput?: boolean;
}
/**
 * 202 response of POST /v1/execute.
 */
export interface ExecuteResponse {
  jobId: string;
  /**
   * soketi channel the client must subscribe to (private-run-<jobId>).
   */
  channel: string;
  status: "queued";
}
/**
 * The fully-resolved job the API enqueues for the worker. The API resolves the manifest so the worker stays language-agnostic at runtime.
 */
export interface JobSpec {
  jobId: string;
  channel: string;
  language: string;
  version: string;
  image: string;
  entrypoint: string;
  compile: string[] | null;
  /**
   * @minItems 1
   */
  run: [string, ...string[]];
  interactive: boolean;
  /**
   * @minItems 1
   */
  files: [FileInput, ...FileInput[]];
  limits: Limits;
  /**
   * Unix epoch ms when the API enqueued the job.
   */
  enqueuedAtMs: number;
  /**
   * Resolved opt-in flag from ExecuteRequest; when true the worker persists a RunResult and captures artifacts. The API always writes an explicit boolean (default false).
   */
  collectOutput?: boolean;
  /**
   * W3C trace-context header for cross-seam trace correlation (optional).
   */
  traceparent?: string;
  /**
   * W3C tracestate header (optional).
   */
  tracestate?: string;
}
/**
 * Response of GET /v1/jobs/:id.
 */
export interface JobStatus {
  jobId: string;
  channel: string;
  language: string;
  version: string;
  state: JobState;
  updatedAtMs: number;
}
/**
 * Published on stdin:<jobId>. Carries a chunk to write to the process stdin.
 */
export interface StdinMessage {
  /**
   * Bytes to write to stdin (UTF-8).
   */
  chunk: string;
}
/**
 * Published on ctrl:<jobId>. Routes lifecycle control to the owning worker.
 */
export interface ControlMessage {
  type: ControlType;
}
/**
 * soketi event 'stage' on private-run-<jobId>.
 */
export interface StageEvent {
  phase: StagePhase;
}
/**
 * soketi events 'stdout' and 'stderr' on private-run-<jobId>.
 */
export interface OutputChunkEvent {
  chunk: string;
  /**
   * Monotonic sequence number for ordering.
   */
  seq: number;
}
/**
 * Terminal soketi event 'result' on private-run-<jobId>.
 */
export interface ResultEvent {
  exitCode: number | null;
  signal: string | null;
  /**
   * Killed by the wall-clock or cpu clock.
   */
  timedOut: boolean;
  /**
   * Killed by the idle clock.
   */
  idleTimedOut: boolean;
  /**
   * Output exceeded outputKb and was truncated.
   */
  truncated: boolean;
  durationMs: number;
}
/**
 * A single file captured from the sandbox working directory, referenced by an object-storage presigned URL. URL-only (no inline bytes): object storage is the single backend.
 */
export interface Artifact {
  /**
   * Relative file name as written by the program into its working directory (e.g. plot.png).
   */
  name: string;
  /**
   * Best-effort detected MIME type of the captured file.
   */
  mimeType: string;
  /**
   * Size of the captured file in bytes.
   */
  bytes: number;
  /**
   * Presigned GET URL the consumer/browser fetches directly (no bearer).
   */
  url: string;
}
/**
 * Result of the compile stage for compiled languages (Piston-style separate `compile` object). Present in RunResult.compile only when a compile step actually ran. Compiler diagnostics conventionally go to stderr; a non-zero exitCode means compilation failed and the run stage did NOT execute.
 */
export interface CompileResult {
  /**
   * Exit code of the compile command. Non-zero means compilation failed and the run stage did not execute.
   */
  exitCode: number | null;
  /**
   * Signal that terminated the compile command, if any.
   */
  signal: string | null;
  /**
   * Accumulated stdout of the compile command.
   */
  stdout: string;
  /**
   * Accumulated stderr of the compile command (compiler diagnostics).
   */
  stderr: string;
  /**
   * Interleaved stdout+stderr of the compile stage, in emission order (mirrors Piston's compile.output).
   */
  output: string;
  /**
   * Wall-clock time of the compile stage in ms.
   */
  durationMs: number;
}
/**
 * The persisted, pullable result of a collected run (GET /v1/jobs/:id/output). ResultEvent's terminal fields plus accumulated stdout/stderr and captured artifacts.
 */
export interface RunResult {
  exitCode: number | null;
  signal: string | null;
  /**
   * Killed by the wall-clock or cpu clock.
   */
  timedOut: boolean;
  /**
   * Killed by the idle clock.
   */
  idleTimedOut: boolean;
  /**
   * Output exceeded outputKb and was truncated.
   */
  truncated: boolean;
  durationMs: number;
  /**
   * Accumulated stdout of the RUN stage (within the outputKb budget; same bytes streamed to soketi). Compile-stage output lives in `compile`, not here.
   */
  stdout: string;
  /**
   * Accumulated stderr of the RUN stage (within the outputKb budget; same bytes streamed to soketi). Compile-stage diagnostics live in `compile.stderr`, not here.
   */
  stderr: string;
  compile?: CompileResult1;
  /**
   * Captured workspace artifacts, each referenced by presigned URL.
   */
  artifacts: Artifact[];
  /**
   * True when captured files exceeded maxArtifacts/maxArtifactBytes and excess was dropped.
   */
  artifactsTruncated: boolean;
}
/**
 * Compile-stage result for compiled languages; absent for interpreted languages or when no compile step ran. Mirrors Piston's separate `compile` object — build logs are kept distinct from the run stdout/stderr.
 */
export interface CompileResult1 {
  /**
   * Exit code of the compile command. Non-zero means compilation failed and the run stage did not execute.
   */
  exitCode: number | null;
  /**
   * Signal that terminated the compile command, if any.
   */
  signal: string | null;
  /**
   * Accumulated stdout of the compile command.
   */
  stdout: string;
  /**
   * Accumulated stderr of the compile command (compiler diagnostics).
   */
  stderr: string;
  /**
   * Interleaved stdout+stderr of the compile stage, in emission order (mirrors Piston's compile.output).
   */
  output: string;
  /**
   * Wall-clock time of the compile stage in ms.
   */
  durationMs: number;
}
