# example-with-nextjs

A small Next.js (App Router) playground that exercises the two published
code-runner SDKs end-to-end:

- **`@teovilla/code-runner-sdk-node`** — server-side. Used by the route handlers
  under `app/api/*` to talk to the Hono gateway with the bearer token, and to
  sign soketi private-channel auth.
- **`@teovilla/code-runner-react`** — browser-side. `CodeRunnerProvider` +
  `useCodeRunnerJob` subscribe to the `private-run-<jobId>` channel over soketi
  and stream live `stdout`/`stderr`.

You write code in a Monaco editor, hit **Run**, and watch output stream in real
time. You can also pipe **stdin** to the running process and **kill** it.

> Trust boundary: the browser never holds the `EXECUTOR_API_TOKEN` or the soketi
> `APP_SECRET`. All trusted actions go through this app's own route handlers
> (`app/api/*`); soketi is output-only toward the client.

## Prerequisites

This app talks to a **running code-runner stack** (API + worker + soketi +
redis + MinIO). From the repo root:

```bash
docker compose up
```

The base compose file keeps api/soketi/minio on the internal network only. Since
this app runs on the **host**, it needs those published. Copy the committed
template — `docker compose up` auto-merges `docker-compose.override.yml`:

```bash
cp docker-compose.override.yml.example docker-compose.override.yml
```

It publishes (defaults; tweak if a port clashes):

| Service | Host port | Why                                              |
| ------- | --------- | ------------------------------------------------ |
| api     | `8080`    | route handlers → gateway                         |
| soketi  | `6001`    | browser pusher-js live output                    |
| minio   | `9000`    | browser fetches artifact presigned URLs          |

> The default `.env.example` points at these ports. If you remap them in the
> override, mirror the change in `.env.local`.

### Artifacts (presigned URLs)

The worker uploads captured files (e.g. a matplotlib figure) to MinIO and returns
a **presigned URL**. SigV4 signs the host, so the URL must be signed with the
address the **browser** will hit — not the worker's internal `minio:9000`. The
override sets `ARTIFACT_S3_PUBLIC_ENDPOINT=http://127.0.0.1:9000` on the worker:
it connects/uploads via `minio:9000` but signs URLs against `127.0.0.1:9000`.
Pick the **“Python · matplotlib (artifact)”** preset and Run to see a figure
preview render directly from its presigned URL.

> Why `127.0.0.1` and not `localhost`: cookies are scoped by host and ignore the
> port, so artifacts served from `localhost:9100` would receive every cookie the
> browser holds for `localhost` (the app on `:3000`, other local dev tools…). A
> large enough `Cookie` header makes MinIO reject the request with
> `MetadataTooLarge`. `127.0.0.1` is a separate cookie jar, so the artifact
> request stays cookie-free.

## Setup

```bash
cp apps/example-with-nextjs/.env.example apps/example-with-nextjs/.env.local
# adjust EXECUTOR_API_TOKEN / SOKETI_* to match your stack
pnpm install
pnpm --filter example-with-nextjs dev
```

Open http://localhost:3000.

## How it maps to the SDKs

| UI action            | Route handler                       | SDK call (`sdk-node`)                |
| -------------------- | ----------------------------------- | ------------------------------------ |
| Load language list   | `GET /api/languages`                | `client.listLanguages()`             |
| **Run**              | `POST /api/execute`                 | `client.execute()` + `client.start()`|
| Send stdin           | `POST /api/jobs/:id/stdin`          | `client.sendStdin()`                 |
| Close stdin (EOF)    | `POST /api/jobs/:id/stdin/close`    | `client.closeStdin()`                |
| Kill                 | `POST /api/jobs/:id/kill`           | `client.kill()`                      |
| Final result pull    | `GET /api/jobs/:id/output`          | `client.getOutput()`                 |
| Channel auth (soketi)| `POST /api/channel-auth`            | `createChannelAuthorizer()`          |

Live output (`stdout`/`stderr`/`compile_output`/`stage`/`result`/`artifact`)
arrives in the browser through `useCodeRunnerJob` — see
`app/_components/job-view.tsx`. The hook exposes `compileOutput` (a live,
real-time **build log** streamed on its own event during the `compiling` stage,
separate from run stdout/stderr) which the playground renders in a dedicated
**build** panel. Pick a compiled language (Rust) to watch it.

## Env vars

Server-only (route handlers):

- `CODE_RUNNER_API_URL` — gateway base URL (default `http://localhost:8080`)
- `EXECUTOR_API_TOKEN` — bearer, must match the API
- `SOKETI_APP_KEY` / `SOKETI_APP_SECRET` — the secret signs channel auth

Browser-exposed (public app key + connection only):

- `NEXT_PUBLIC_SOKETI_APP_KEY`
- `NEXT_PUBLIC_SOKETI_HOST` / `NEXT_PUBLIC_SOKETI_PORT` / `NEXT_PUBLIC_SOKETI_USE_TLS`
