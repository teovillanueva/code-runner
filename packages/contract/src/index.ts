// Public entrypoint for the shared wire contract.
// Re-exports the generated TS types and zod validators so consumers import from
// a single stable path: `@teovilla/code-runner-contract`.
export * from "../gen/ts/types.js";
export * from "../gen/ts/schemas.js";

// Shared manifest loader + resolver — lets the Hono API list and resolve
// languages with zero hardcoded identifiers.
export * from "./manifest.js";

// Channel + Redis key conventions live alongside the contract so both the API
// and (via documentation) the worker agree on them.
export const channelForJob = (jobId: string): string => `private-run-${jobId}`;
export const stdinChannel = (jobId: string): string => `stdin:${jobId}`;
export const controlChannel = (jobId: string): string => `ctrl:${jobId}`;

/** Redis key/queue conventions (kept in lockstep with the Go worker's internal/keys). */
export const keys = {
  /** List the API LPUSHes jobs onto and the worker BRPOPs from. */
  jobQueue: "jobs:queue",
  /** Hash holding the JSON-encoded JobStatus for a job. */
  jobStatus: (jobId: string): string => `job:${jobId}:status`,
  /** Hash holding the JSON-encoded JobSpec for a job. */
  jobSpec: (jobId: string): string => `job:${jobId}:spec`,
  /** Key holding the JSON-encoded RunResult (collected output) for a job; written with a TTL. */
  jobOutput: (jobId: string): string => `job:${jobId}:output`,
} as const;

/** soketi event names emitted on the private-run-<jobId> channel. */
export const events = {
  stage: "stage",
  stdout: "stdout",
  stderr: "stderr",
  result: "result",
  artifact: "artifact",
  /**
   * Live interleaved build log of the compile stage (compiled languages only),
   * emitted in emission order during the `compiling` stage. Kept on its OWN
   * event — separate from run stdout/stderr — so the client can show a dedicated
   * real-time build panel. The persisted RunResult.compile carries the same
   * bytes split into stdout/stderr/output (Piston-style).
   */
  compileOutput: "compile_output",
} as const;
