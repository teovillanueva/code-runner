# @teovilla/code-runner-contract

The shared **wire contract** for [code-runner](https://github.com/teovillanueva/code-runner) — the
single source of truth for the types, validators, and channel/event names exchanged between the
gateway, the worker, and the client SDKs.

You usually don't depend on this package directly — it's a transitive dependency of
[`@teovilla/code-runner-sdk-node`](https://www.npmjs.com/package/@teovilla/code-runner-sdk-node)
and [`@teovilla/code-runner-react`](https://www.npmjs.com/package/@teovilla/code-runner-react),
which re-export the types you need. Install it on its own only if you're building your own client.

```bash
npm install @teovilla/code-runner-contract
```

## What's inside

- **Types** — `ExecuteRequest`, `ExecuteResponse`, `JobStatus`, `JobState`, `LanguageInfo`,
  `FileInput`, `Limits`, `LimitsOverride`, and the soketi event payloads `StageEvent`,
  `OutputChunkEvent`, `ResultEvent`, `StdinMessage`, `ControlMessage`.
- **Zod validators** — a `<Name>Schema` for each type.
- **Helpers** — `channelForJob(jobId)` → `private-run-<jobId>`, `stdinChannel`, `controlChannel`,
  `keys`, and `events` (`{ stage, stdout, stderr, result }`).

```ts
import { channelForJob, events, type ResultEvent } from "@teovilla/code-runner-contract";

const channel = channelForJob(jobId); // "private-run-<jobId>"
```

The contract is generated from `schema/wire.schema.json`. To consume the raw JSON Schema:

```ts
import schema from "@teovilla/code-runner-contract/schema" with { type: "json" };
```

## License

MIT
