// Public surface of @teovilla/code-runner-react.

export { CodeRunnerProvider } from "./provider.tsx";
export type { CodeRunnerProviderProps } from "./provider.tsx";

export { useCodeRunnerJob } from "./useCodeRunnerJob.ts";
export type {
  UseCodeRunnerJobArgs,
  UseCodeRunnerJobResult,
  JobStatusState,
} from "./useCodeRunnerJob.ts";

// Re-export the relevant wire-contract event types. They resolve from
// @teovilla/code-runner-contract, a published dependency — type-only, so nothing
// is bundled into the browser at runtime.
export type {
  StageEvent,
  StagePhase,
  OutputChunkEvent,
  ResultEvent,
} from "@teovilla/code-runner-contract";
