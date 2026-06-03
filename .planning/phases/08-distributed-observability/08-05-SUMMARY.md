---
phase: 08-distributed-observability
plan: 05
subsystem: observability
tags: [opentelemetry, otel-js, w3c-traceparent, sdk-node, docker-compose, jaeger, otel-collector, sampling, byo-stack, checkpoint]

# Dependency graph
requires:
  - phase: 08-distributed-observability
    plan: 01
    provides: JobSpec traceparent/tracestate wire carrier the caller context flows into
  - phase: 08-distributed-observability
    plan: 02
    provides: Worker phase span names (claim/sandbox.create/handshake.wait/compile/run/publish.result) shown in the BYO trace
  - phase: 08-distributed-observability
    plan: 03
    provides: API execute span + env-gated NodeSDK whose OTLP export target the compose profile wires
  - phase: 08-distributed-observability
    plan: 04
    provides: Worker domain metrics surfaced via the collector debug exporter
  - phase: 08-distributed-observability
    plan: 04b
    provides: API rejection metrics surfaced via the collector debug exporter
provides:
  - Node SDK caller->API traceparent propagation on /v1/execute (optional-peer @opentelemetry/api; no-op/no-error when OTel absent)
  - docker-compose observability profile (otel-collector 0.153.0 + jaeger all-in-one 1.62.0) inert by default, runnable via --profile observability
  - observability/otel-collector.yaml (OTLP recv -> jaeger traces + debug metrics/logs)
  - .env.example documented OTEL_* block (endpoint, protocol, service name, resource attrs, sampler+arg, kill-switches) all commented => true no-op
  - scripts/observability-e2e.sh + README BYO-stack quickstart + Redis/soketi self-instrumentation doc-only note
affects: [distributed-observability, milestone-headline-acceptance]

# Tech tracking
tech-stack:
  added:
    - "@opentelemetry/api ^1.9.0 (OPTIONAL peerDependency on packages/code-runner-sdk-node — zero weight for non-OTel callers)"
    - "otel/opentelemetry-collector-contrib:0.153.0 (compose, observability profile only)"
    - "jaegertracing/all-in-one:1.62.0 (compose, observability profile only)"
  patterns:
    - "SDK injects via dynamic import('@opentelemetry/api') in try/catch — OTel absent or no active span => silently unchanged behavior; injection gated to path === '/v1/execute' only"
    - "observability services mirror the existing `stub` profile block: profiles:[observability] + networks:[code-runner] => inert on default `docker compose up`, opt-in via --profile observability (D-09, not commented-out YAML)"
    - "api/worker get OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318 only under the profile; unset/empty on default profile (no-op)"
    - "sampler documented as OTEL_TRACES_SAMPLER=parentbased_traceidratio + OTEL_TRACES_SAMPLER_ARG=1.0 with a collector tail-sampling note (D-10)"

key-files:
  created:
    - packages/code-runner-sdk-node/test/trace.test.ts
    - observability/otel-collector.yaml
    - scripts/observability-e2e.sh
    - .planning/phases/08-distributed-observability/08-05-SUMMARY.md
  modified:
    - packages/code-runner-sdk-node/src/client.ts
    - packages/code-runner-sdk-node/package.json
    - docker-compose.yml
    - .env.example
    - README.md
    - CLAUDE.md
    - pnpm-lock.yaml

key-decisions:
  - "D-08: Jaeger all-in-one (jaegertracing/all-in-one:1.62.0, COLLECTOR_OTLP_ENABLED, UI 16686) as the example trace backend"
  - "D-09: shipped under a named compose --profile observability, inert on default up (NOT commented-out YAML)"
  - "D-10: sampling default OTEL_TRACES_SAMPLER=parentbased_traceidratio + OTEL_TRACES_SAMPLER_ARG=1.0 documented in .env.example with the collector tail-sampling note"

patterns-established:
  - "Optional-peer telemetry: dynamic import in try/catch keeps the SDK weightless for non-OTel consumers while extending the connected trace to the caller when OTel IS present"
  - "BYO observability stack ships as a profile (operator opt-in), not as default infra — matches the trust boundary (collector is operator-run egress)"

requirements-completed: [OBS-02, OBS-04, OBS-07, OBS-08]

# Metrics
duration: 9min
completed: 2026-06-03
checkpoint_status: pending-human-verify
---

# Phase 8 Plan 05: Final Integration — SDK Caller Propagation + BYO Jaeger Stack Summary

**Closes the connected-trace story end-to-end: the Node SDK now propagates an active caller OTel context into `/v1/execute` (optional-peer, no-op when OTel absent), and ships an inert-by-default `observability` compose profile (otel-collector + Jaeger) with documented `OTEL_*` configuration, an e2e script, and BYO-stack README docs — so a self-hoster flips one flag and sees their single connected trace spanning API and worker. Tasks 1 & 2 (auto) are complete and committed; Task 3 (the live Jaeger human-verify) is deferred to the operator's own confirmation.**

## Performance

- **Duration:** ~9 min (auto Tasks 1 & 2; Task 3 is human-verify, not timed)
- **Completed:** 2026-06-03 (auto tasks)
- **Tasks:** 2/3 auto-complete; 1 human-verify checkpoint outstanding
- **Files:** 4 created, 6 modified

## Accomplishments

### Task 1 — SDK Node caller->API traceparent propagation (optional peer) — commit `72a719f`
- `packages/code-runner-sdk-node/src/client.ts` — in `request()`, after header assembly, when `path === "/v1/execute"` it does `const api = await import("@opentelemetry/api")` in a try/catch and calls `api.propagation.inject(api.context.active(), headers)`. Any throw (OTel absent, no active span) is swallowed → unchanged behavior. Injection happens ONLY on `/v1/execute`, never on stdin/kill/status paths.
- `packages/code-runner-sdk-node/package.json` — `@opentelemetry/api ^1.9.0` wired as an **optional** `peerDependency` (`peerDependenciesMeta.optional: true`) so non-OTel callers pull zero OTel weight.
- `packages/code-runner-sdk-node/test/trace.test.ts` (new, `node --test`) — asserts: active caller span ⇒ `/v1/execute` request carries a `traceparent` matching the caller trace_id; no active span ⇒ request still succeeds without the header; optional-peer-absent ⇒ no-op/no-error; injection gated to `/v1/execute`. **15/15 pass**, typecheck clean.

### Task 2 — Compose observability profile + collector + docs + e2e — commit `4988679`
- `docker-compose.yml` — `otel-collector` (`otel/opentelemetry-collector-contrib:0.153.0`) + `jaeger` (`jaegertracing/all-in-one:1.62.0`, `COLLECTOR_OTLP_ENABLED=true`, UI `16686`) added under `profiles: [observability]` on the `code-runner` network, mirroring the existing `stub` block. api/worker set `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318` + `OTEL_SERVICE_NAME` only under the profile (empty/unset on default → no-op).
- `observability/otel-collector.yaml` (new) — OTLP receiver (http 4318 + grpc 4317), `batch` processor, exporters `otlp/jaeger` (→ `jaeger:4317`, tls.insecure) + `debug` (detailed), three pipelines (traces → jaeger+debug, metrics → debug, logs → debug), with a tail-sampling note comment (D-10).
- `.env.example` — documented `OTEL_*` block: endpoint, protocol (`http/protobuf`), service name, resource attrs, sampler `parentbased_traceidratio` + arg `1.0`, kill-switches (`OTEL_SDK_DISABLED`, `OTEL_TRACES_EXPORTER=none`) — 9 distinct OTEL_ vars, all commented ⇒ true no-op.
- `scripts/observability-e2e.sh` (new, `chmod +x`) — brings up `--profile observability`, runs one execute end-to-end, prints the Jaeger UI URL + a collector-debug check proving one trace spans both services.
- `README.md` — BYO-OTel-stack quickstart (the `--profile observability` flag, the `OTEL_*` vars, lowering the sampler ratio in prod) + a doc-only note on adding `redis_exporter` / soketi metrics (SPEC out-of-scope: no exporter shipped).
- `CLAUDE.md` — Environment notes updated for the new compose profile.
- Verify (static, no `up`): `docker compose config` (no profile) renders only `api redis soketi worker` (observability absent — inert); `docker compose --profile observability config` renders both services on the `code-runner` network. No admin/metrics HTTP port added anywhere (OBS-05 stays dropped).

### Task 3 — Human-verify the connected trace in Jaeger — OUTSTANDING (operator confirmation)
Per the orchestrator's decision (user opted to verify in the Jaeger UI themselves), this blocking `human-verify` checkpoint is not auto-approved. All code/config to perform it is committed and statically validated; the live single-trace visual check is the operator's to run. See **Human Verification** below.

## Task Commits

1. **Task 1: SDK Node caller→API traceparent propagation (optional peer)** — `72a719f` (feat)
2. **Task 2: Compose observability profile + collector + .env + e2e + README/CLAUDE** — `4988679` (feat)

## Human Verification (outstanding — Task 3)

1. Copy `.env.example` to `.env`; uncomment `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318` (+ the service-name/sampler lines).
2. `docker compose --profile observability up --build` (or `bash scripts/observability-e2e.sh`).
3. Trigger one interactive execute against the gateway.
4. Open Jaeger at http://localhost:16686 → service `code-runner-api` → latest trace → confirm it contains BOTH the API `execute` span AND the worker phase spans (`claim`, `sandbox.create`, `handshake.wait`, `compile`, `run`, `publish.result`) under ONE trace_id.
5. `docker compose --profile observability logs otel-collector` → confirm the `debug` exporter prints received metrics (`code_runner.jobs.terminal`, `code_runner.queue.depth`, …) + trace-correlated logs.
6. Stop the stack; plain `docker compose up` with `OTEL_*` unset ⇒ a full execute still completes with zero telemetry and no new port opened.

**Resume signal:** "approved" if one connected trace (API + worker, shared trace_id) is visible in Jaeger and the no-op-when-unset run is clean; otherwise describe what's missing.

## Decisions Made

Implemented D-08, D-09, D-10 exactly as specified. SDK telemetry kept as an optional peer (RESEARCH A5) so non-OTel callers are weightless.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — test tooling] `@opentelemetry/core@2.7.1` added as an SDK devDependency**
- **Found during:** Task 1 — to register a W3C propagator and assert genuine `traceparent` byte-format in the test, `@opentelemetry/core` was needed (already in the lockfile via apps/api).
- **Fix:** Added it as a **devDependency** only; the runtime peer (`@opentelemetry/api`) stays optional. No runtime weight added.
- **Committed in:** `72a719f`.

**2. [Rule 1 — version pinning] Image tags pinned by Docker Hub existence check**
- Context7/ctx7 were unavailable, so the collector (`0.153.0`) and Jaeger (`1.62.0`) tags were confirmed-existing via Docker Hub (HTTP 200) rather than via docs. Both are current, non-EOL.

---

**Total deviations:** 2 auto-fixed (test tooling + tag verification method). No runtime dependency weight added to the SDK; no admin/metrics port; behavior strictly additive.

## Out-of-Scope (Deferred)

- **Live Jaeger trace verification (Task 3)** — intentionally left to the operator per the orchestrator decision. Tracked as the phase's outstanding human-verification item (HUMAN-UAT).
- **Redis/soketi self-instrumentation** — documentation-only per 08-SPEC; README explains how a self-hoster adds `redis_exporter` / soketi metrics. No exporter shipped.

## Known Stubs

None. The SDK injection, compose profile, collector config, and e2e script are all real; the no-op-when-OTEL-unset behavior is the designed OBS-01 contract, not a stub.

## Threat Flags

- T-08-14 (info disclosure via SDK header) — mitigated: only non-secret W3C `traceparent`/`tracestate` injected, only on `/v1/execute`, optional-peer try/catch.
- T-08-15 (collector/Jaeger egress) — mitigated: OTLP push only to operator-configured endpoint; inert by default; trust boundary unchanged.
- T-08-16 (malicious caller traceparent) — mitigated: API/worker W3C extract fails closed to a fresh trace on malformed input (08-01/08-02); SDK injects only the caller's own active context.

## Self-Check: PASSED (auto tasks)

Tasks 1 & 2 verified: SDK 15/15 tests + typecheck green; `docker compose config` inert / `--profile observability config` includes both services; `.env.example` documents 9 OTEL_* vars; collector yaml + executable e2e script present; no admin/metrics port. Task 3 (live Jaeger trace) awaits operator confirmation.
