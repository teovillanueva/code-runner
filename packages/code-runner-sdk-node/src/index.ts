// Public surface of @teovilla/code-runner-sdk-node.

export { CodeRunnerClient } from "./client.ts";
export type {
  CodeRunnerClientOptions,
  FetchLike,
} from "./client.ts";

export {
  CodeRunnerError,
  UnauthorizedError,
  NotFoundError,
  ValidationError,
  CapacityError,
  RateLimitError,
} from "./errors.ts";

export {
  signChannelAuth,
  createChannelAuthorizer,
} from "./channelAuth.ts";
export type {
  SignChannelAuthArgs,
  ChannelAuthResponse,
  CreateChannelAuthorizerArgs,
} from "./channelAuth.ts";

// Re-export the relevant wire-contract types so consumers get a single import
// surface. They resolve from @teovilla/code-runner-contract, a published
// (transitive) dependency — type-only here, so nothing is pulled in at runtime.
export type {
  ExecuteRequest,
  ExecuteResponse,
  JobStatus,
  JobState,
  LanguageInfo,
  FileInput,
  Limits,
  LimitsOverride,
  StagePhase,
  StageEvent,
  OutputChunkEvent,
  ResultEvent,
} from "@teovilla/code-runner-contract";
