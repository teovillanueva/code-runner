<p align="center">
  <img src="https://raw.githubusercontent.com/teovillanueva/code-runner/main/.github/assets/banner-contract.svg" alt="@teovilla/code-runner-contract" width="100%" />
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/@teovilla/code-runner-contract"><img src="https://img.shields.io/npm/v/@teovilla/code-runner-contract?logo=npm&color=cb3837" alt="npm" /></a>
  <img src="https://img.shields.io/badge/JSON%20Schema-TS%20·%20Zod%20·%20Go-a78bfa" alt="JSON Schema → TS · Zod · Go" />
  <a href="https://github.com/teovillanueva/code-runner/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-MIT-2ea44f.svg" alt="MIT" /></a>
</p>

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
