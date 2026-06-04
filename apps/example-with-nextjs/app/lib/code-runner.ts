// Server-only wiring for the code-runner SDK.
//
// This module holds the EXECUTOR_API_TOKEN and the soketi APP_SECRET. It must
// never be imported from a Client Component — `import "server-only"` makes that
// a build error.

import "server-only";
import {
  CodeRunnerClient,
  createChannelAuthorizer,
} from "@teovilla/code-runner-sdk-node";

function required(name: string): string {
  const v = process.env[name];
  if (!v) {
    throw new Error(
      `Missing env var ${name}. Copy .env.example to .env.local and fill it in.`,
    );
  }
  return v;
}

let client: CodeRunnerClient | null = null;

/** Lazily-built singleton gateway client carrying the bearer token. */
export function getClient(): CodeRunnerClient {
  if (!client) {
    client = new CodeRunnerClient({
      baseUrl: process.env["CODE_RUNNER_API_URL"] ?? "http://localhost:8080",
      token: required("EXECUTOR_API_TOKEN"),
    });
  }
  return client;
}

let authorizer:
  | ((socketId: string, channelName: string) => { auth: string })
  | null = null;

/** Soketi private-channel authorizer signed with the server-side APP_SECRET. */
export function getChannelAuthorizer() {
  if (!authorizer) {
    authorizer = createChannelAuthorizer({
      appKey: required("SOKETI_APP_KEY"),
      appSecret: required("SOKETI_APP_SECRET"),
    });
  }
  return authorizer;
}
