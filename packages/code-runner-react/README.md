<p align="center">
  <img src="https://raw.githubusercontent.com/teovillanueva/code-runner/main/.github/assets/banner-react.svg" alt="@teovilla/code-runner-react" width="100%" />
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/@teovilla/code-runner-react"><img src="https://img.shields.io/npm/v/@teovilla/code-runner-react?logo=npm&color=cb3837" alt="npm" /></a>
  <img src="https://img.shields.io/badge/React-%E2%89%A518-61dafb?logo=react&logoColor=white" alt="React >=18" />
  <img src="https://img.shields.io/badge/pusher--js-%E2%89%A58-300d4f" alt="pusher-js >=8" />
  <a href="https://github.com/teovillanueva/code-runner/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-MIT-2ea44f.svg" alt="MIT" /></a>
</p>

# @teovilla/code-runner-react

The **browser** real-time SDK for the [code-runner](https://github.com/teovillanueva/code-runner) service.

- `<CodeRunnerProvider>` — one pusher-js client wired to your self-hosted soketi.
- `useCodeRunnerJob(...)` — subscribe to a job's `private-run-<jobId>` channel and get ordered `stdout` / `stderr`, the current `stage`, and the terminal `result`.

> **Output-only and token-free.** This package never holds the code-runner bearer token. It receives live output over soketi, and delegates actions (stdin / kill) to callbacks that hit *your* backend — which uses [`@teovilla/code-runner-sdk-node`](https://www.npmjs.com/package/@teovilla/code-runner-sdk-node).

## Install

```bash
npm i @teovilla/code-runner-react react pusher-js
```

`react` (>=18) and `pusher-js` (>=8) are peer dependencies you provide.

## Quickstart

### 1. Wrap your app

```tsx
import { CodeRunnerProvider } from "@teovilla/code-runner-react";

export function App({ children }: { children: React.ReactNode }) {
  return (
    <CodeRunnerProvider
      appKey={import.meta.env.VITE_SOKETI_APP_KEY}
      host="localhost"
      port={6001}
      useTLS={false}
      // your backend route that signs the private-channel auth with sdk-node's
      // createChannelAuthorizer (the soketi APP_SECRET lives only there)
      authEndpoint="/channel-auth"
    >
      {children}
    </CodeRunnerProvider>
  );
}
```

### 2. Subscribe to a job

```tsx
import { useCodeRunnerJob } from "@teovilla/code-runner-react";

function JobView({ jobId }: { jobId: string }) {
  const { stage, stdout, stderr, result, status, sendStdin, kill } =
    useCodeRunnerJob({
      jobId,
      // The browser has no token. These call back into YOUR backend, which
      // uses @teovilla/code-runner-sdk-node to talk to the gateway.
      onStdin: (chunk) =>
        fetch(`/api/jobs/${jobId}/stdin`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ chunk }),
        }),
      onKill: () => fetch(`/api/jobs/${jobId}/kill`, { method: "POST" }),
    });

  return (
    <div>
      <div>stage: {stage ?? "—"} · status: {status}</div>
      <pre>{stdout}</pre>
      <pre style={{ color: "crimson" }}>{stderr}</pre>
      <button onClick={() => sendStdin("hello\n")}>send stdin</button>
      <button onClick={() => kill()}>kill</button>
      {result && (
        <div>
          exit {String(result.exitCode)} · {result.durationMs}ms
          {result.timedOut && " · timed out"}
          {result.truncated && " · output truncated"}
        </div>
      )}
    </div>
  );
}
```

`stdout` / `stderr` are reassembled in `seq` order from the worker's chunks, so
they always render correctly even if soketi delivers out of order.

## The full circle

```mermaid
sequenceDiagram
    participant B as Browser (this SDK)
    participant Y as Your backend (sdk-node)
    participant CR as code-runner gateway
    participant SK as soketi
    Y->>CR: execute() + start()  (bearer token)
    Y-->>B: { jobId }
    B->>Y: authorize private-run-id (authEndpoint)
    Y-->>B: signed auth (APP_SECRET stays server-side)
    B->>SK: subscribe private-run-id
    SK-->>B: stage / stdout / stderr / result
    B->>Y: sendStdin / kill
    Y->>CR: forward to gateway
```

1. Your backend enqueues + starts a job with [`@teovilla/code-runner-sdk-node`](https://www.npmjs.com/package/@teovilla/code-runner-sdk-node) and returns `{ jobId }`.
2. The browser calls `useCodeRunnerJob({ jobId })`; pusher-js authorizes the
   `private-run-<jobId>` subscription against your `authEndpoint`, which signs
   with sdk-node's `createChannelAuthorizer`.
3. Live `stage` / `stdout` / `stderr` / `result` events stream in.
4. `sendStdin` / `kill` post to your backend, which forwards to the gateway via sdk-node.

The bearer token and soketi `APP_SECRET` never leave the server.

## License

MIT
