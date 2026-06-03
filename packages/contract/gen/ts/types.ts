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
   * Relative file name written into the sandbox workspace.
   */
  name: string;
  /**
   * UTF-8 file content.
   */
  content: string;
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
   * Accumulated stdout (within the outputKb budget; same bytes streamed to soketi).
   */
  stdout: string;
  /**
   * Accumulated stderr (within the outputKb budget; same bytes streamed to soketi).
   */
  stderr: string;
  /**
   * Captured workspace artifacts, each referenced by presigned URL.
   */
  artifacts: Artifact[];
  /**
   * True when captured files exceeded maxArtifacts/maxArtifactBytes and excess was dropped.
   */
  artifactsTruncated: boolean;
}
