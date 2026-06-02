# CFG-04: Native Redis Requirement for the Worker

## Summary

The **code-runner worker requires a native Redis (or Valkey) TCP connection**.
API-only serverless Redis implementations such as **Upstash** (REST/HTTP API) are
**not viable** for the worker. They are acceptable for the API process only.

## Why the Worker Needs Native Redis

The worker relies on Redis features that require a persistent, blocking TCP
connection. None of these are available over HTTP REST APIs:

| Operation | Used For | Blocking? |
|-----------|----------|-----------|
| `SUBSCRIBE` / `UNSUBSCRIBE` | Receive stdin chunks on `stdin:<jobID>` (MVP pub/sub transport) | Yes — holds connection open |
| `BRPOP` / `BLPOP` / `LMOVE` | Claim the next job from the execution queue | Yes — blocks until item available |
| `XREAD BLOCK` / `XREADGROUP` | Consume jobs/stdin from Redis Streams (planned upgrade) | Yes — blocks until new entry |

These commands maintain a long-lived connection that "blocks" at the server,
waiting for new data. HTTP REST APIs (Upstash, Momento, etc.) are stateless
request/response — they have no mechanism to hold a connection open for
server-push events.

## What Upstash CAN Do

Upstash's REST API supports standard read/write commands (`GET`, `SET`, `LPUSH`,
`RPOP`, etc.) over HTTP. This is perfectly fine for the **API process**, which
only enqueues jobs (`RPUSH`) and stores metadata — all synchronous one-shot
operations.

## Deployment Recommendation

Run a **single native Redis / Valkey instance** (or managed cluster) shared by
both the API and the worker:

```
API process         ──redis://redis:6379──►  Redis / Valkey
Worker process      ──redis://redis:6379──►  (same instance)
```

Recommended options:

| Option | Notes |
|--------|-------|
| **Self-hosted Redis / Valkey** (Docker, k8s) | Simplest for dev; Valkey is the OSS Redis fork |
| **Redis Cloud (Standard plan)** | Managed native Redis; supports pub/sub + blocking ops |
| **Upstash with dedicated Redis** | Upstash's dedicated-cluster tier supports native TCP — check their docs; the serverless REST-only tier does NOT work |
| **AWS ElastiCache / MemoryDB** | Native Redis/Valkey; suitable for production |

## Encoded in Code

The constraint is encoded as `Config.RequiresNativeRedis()` in
`internal/config/config.go` (always returns `true`), so:

1. Operators can verify the requirement at startup.
2. Tests assert the constant is stable.
3. Future config-loading code can validate `REDIS_URL` points to a TCP
   endpoint (not an HTTP URL).

## Connection URL Hint

A valid `REDIS_URL` starts with `redis://` or `rediss://` (TLS).
If your URL starts with `https://` you are using an HTTP REST proxy and the
worker will fail on its first `SUBSCRIBE` or `BRPOP` call.

## See Also

- `.env.example` — `REDIS_URL` note
- `internal/config/config.go` — `Config.RequiresNativeRedis()`
- `internal/stdintransport/transport.go` — `StdinTransport` interface (pub/sub → Streams upgrade path)
- `.planning/research/STACK.md` — decision #2 (Redis client + stdin delivery)
