---
phase: 08-distributed-observability
plan: 04b
type: execute
wave: 3
depends_on: ["08-02", "08-03"]
files_modified:
  - apps/api/src/metrics.ts
  - apps/api/src/admission.ts
  - apps/api/src/ratelimit.ts
  - apps/api/src/routes/control.ts
  - apps/api/src/routes/jobs.ts
  - apps/api/src/redis.ts
  - apps/api/test/metrics.test.ts
autonomous: true
requirements: [OBS-06, OBS-07]

must_haves:
  decisions_implemented: [D-02, D-05, D-06]
  truths:
    - "Decisions implemented: D-02 (finishes the console.*→pino migration for the remaining API request/job paths — control.ts / jobs.ts / redis.ts — so no console.* remains anywhere in apps/api/src), D-05 (counters for admission-429 + ratelimit rejections per the instrument-type mapping), D-06 (code_runner.* dotted namespace + unit metadata on the API rejection counters: code_runner.admission.rejected / code_runner.ratelimit.rejected)"
    - "API admission-429 and ratelimit-429 rejections increment counters with low-cardinality reason attributes"
    - "Remaining API request/job paths log via pino (no console.* left anywhere in apps/api/src)"
  artifacts:
    - path: "apps/api/src/metrics.ts"
      provides: "API meter + admission/ratelimit rejection counters"
      contains: "createCounter"
  key_links:
    - from: "apps/api/src/ratelimit.ts"
      to: "code_runner.ratelimit.rejected counter"
      via: "ratelimitRejections.add(1, {reason})"
      pattern: "ratelimitRejections|ratelimit.rejected"
    - from: "apps/api/src/admission.ts"
      to: "code_runner.admission.rejected counter"
      via: "admissionRejections.add(1)"
      pattern: "admissionRejections|admission.rejected"
---

<objective>
API-side breadth slice (split out of 08-04 for quality + parallelism): create the API meter and emit the admission-429 and ratelimit-429 rejection counters with low-cardinality `reason` attributes, and finish migrating the remaining API request/job paths off `console.*` to pino (control.ts / jobs.ts / redis.ts).

Purpose: Completes the TS half of OBS-06/OBS-07 operational breadth alongside the Go worker metrics (08-04). Both run in parallel (disjoint files: Go vs TS). Metric attributes stay low-cardinality (`reason`); `job_id` never goes on metrics (RESEARCH anti-pattern). The pino logger + AsyncLocalStorage `job_id` mixin already exist from 08-03 — this plan reuses `getLogger()`.

This plan was split from the original 08-04 (which exceeded the 15-file threshold). 08-04 now carries the Go-worker metrics only; 08-04b carries the API metrics + the console→pino finish. Total coverage is identical to the original 08-04 — no metric or migration dropped.

Output: new `apps/api/src/metrics.ts` + rejection-counter wiring in `admission.ts`/`ratelimit.ts`; remaining `console.*`→pino in `control.ts`/`jobs.ts`/`redis.ts`; vitest metric assertions.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/PROJECT.md
@.planning/ROADMAP.md
@.planning/STATE.md
@.planning/phases/08-distributed-observability/08-SPEC.md
@.planning/phases/08-distributed-observability/08-CONTEXT.md
@.planning/phases/08-distributed-observability/08-RESEARCH.md
@.planning/phases/08-distributed-observability/08-PATTERNS.md
@.planning/phases/08-distributed-observability/08-03-PLAN.md
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: API meter + admission/ratelimit rejection counters; migrate remaining console.* to pino</name>
  <files>apps/api/src/metrics.ts, apps/api/src/admission.ts, apps/api/src/ratelimit.ts, apps/api/src/routes/control.ts, apps/api/src/routes/jobs.ts, apps/api/src/redis.ts, apps/api/test/metrics.test.ts</files>
  <read_first>
    - 08-RESEARCH.md §"Pattern 5" JS meter snippet inside §Architecture (the `metrics.getMeter("code-runner-api")` + `createCounter` shape) and anti-patterns (low-cardinality attrs only).
    - 08-PATTERNS.md §"apps/api/src/metrics.ts (new)" (lines 80-92) — module shape from `admission.ts:41` (small named exports, no class); `meter.createCounter("code_runner.admission.rejected", {unit:"{request}"})` + `code_runner.ratelimit.rejected`.
    - 08-PATTERNS.md §"control.ts, jobs.ts, redis.ts (modified)" (lines 137-143) — `redis.ts:19-20` `console.error`→pino; `ratelimit.ts` 429 branches (lines 60-68 frame-rate, 108-118 byte-cap) → `ratelimitRejections.add(1,{reason})`; control.ts console.* → pino (stdin/kill spans discretionary D-11 — skip, output is metrics).
    - 08-PATTERNS.md §"execute.ts" admission branch note (line 115) — admission-429 branch calls `admissionRejections.add(1)`; that branch lives in execute.ts (08-03 left it) — prefer incrementing inside `admission.ts`'s helper so execute.ts stays unchanged (08-03 owns execute.ts; this plan must NOT edit execute.ts — keep file ownership clean).
    - apps/api/src/admission.ts (atCapacity/admissionError helpers ~lines 29-49), apps/api/src/ratelimit.ts (the two 429 branches), apps/api/src/routes/control.ts, apps/api/src/routes/jobs.ts, apps/api/src/redis.ts (console.error line ~19-20) — actual sites.
    - apps/api/src/logger.ts (from 08-03) — `getLogger()` singleton + `jobContext` AsyncLocalStorage to reuse for the console→pino migration.
    - 08-RESEARCH.md §"Validation Architecture" Test Tooling Notes (lines 582-586) — JS metrics via `InMemoryMetricExporter` or manual `PeriodicExportingMetricReader` flush.
  </read_first>
  <behavior>
    - `code_runner.admission.rejected` (counter, unit `{request}`) increments when a request is rejected by the queue-depth admission gate (429).
    - `code_runner.ratelimit.rejected` (counter) increments on each ratelimit rejection with a low-cardinality `reason` attribute (`frame_rate` vs `byte_cap`).
    - All API request/job paths (control.ts, jobs.ts, redis.ts) log via pino — no `console.log`/`console.error` remains anywhere in `apps/api/src`.
    - Counters carry only low-cardinality attributes (`reason`); never `job_id`.
  </behavior>
  <action>
    Create `apps/api/src/metrics.ts` mirroring the `admission.ts` small-named-export shape: a module-level `meter = metrics.getMeter("code-runner-api")` and exported counters `admissionRejections` (`code_runner.admission.rejected`, unit `{request}`) and `ratelimitRejections` (`code_runner.ratelimit.rejected`, unit `{request}`). Wire `admissionRejections.add(1)` at the admission-429 site (increment INSIDE the `admission.ts` helper so callers/execute.ts stay unchanged — do NOT edit execute.ts, which is 08-03's file) and `ratelimitRejections.add(1,{reason})` at the two `ratelimit.ts` 429 branches with `reason` = `frame_rate`/`byte_cap`. Replace remaining `console.*` in `redis.ts`, `control.ts`, and `jobs.ts` with `getLogger()` (from logger.ts, 08-03). Do NOT add stdin/kill spans (D-11 — output is metrics; skip). Write `apps/api/test/metrics.test.ts` (vitest) using an InMemory metric reader/exporter to assert the admission and ratelimit counters increment with the expected `reason` attribute. Never log secrets or user code/stdin; keep metric attributes low-cardinality (no `job_id`).
  </action>
  <verify>
    <automated>pnpm --filter @code-runner/api typecheck && pnpm --filter @code-runner/api test metrics</automated>
  </verify>
  <acceptance_criteria>
    - `pnpm --filter @code-runner/api test metrics` asserts `code_runner.admission.rejected` increments on an admission rejection and `code_runner.ratelimit.rejected{reason=...}` increments on a ratelimit rejection.
    - `grep -rn 'console\.\(log\|error\)' apps/api/src/` returns NO matches (all request/job paths on pino).
    - `grep -n 'createCounter' apps/api/src/metrics.ts` matches both counters; no `job_id` attribute on any counter.
    - execute.ts is NOT modified by this plan (08-03 owns it); admission increment lives in `admission.ts`.
    - `pnpm --filter @code-runner/api typecheck` passes.
  </acceptance_criteria>
  <done>API emits admission + ratelimit rejection counters; the entire apps/api/src is migrated off console.* to pino; counters are low-cardinality; execute.ts untouched (clean ownership with 08-03).</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| API → OTLP Collector | Metric egress (push only). No inbound surface; OBS-05 admin port dropped. |
| Caller → API (rate-limit / admission gates) | Rejection paths increment counters; no new behavior, telemetry is additive. |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-08-10b | Denial of Service (backend) | high-cardinality metric attributes | mitigate | API counters use low-cardinality attributes (`reason` only); Task asserts no `job_id`/user-string on metric attributes. |
| T-08-12 | Information Disclosure | pino logs in control/jobs/redis paths | mitigate | Migrate to pino with allow-listed fields; never log `EXECUTOR_API_TOKEN`/`SOKETI_APP_SECRET`/stdin/user code. |
| T-08-13b | Elevation / Tampering | no new listener | accept | No admin/metrics HTTP port opened (OBS-05 dropped); metric export is outbound OTLP push only. |
</threat_model>

<verification>
- `pnpm --filter @code-runner/api typecheck && pnpm --filter @code-runner/api test metrics` pass.
- InMemory assertions confirm the admission + ratelimit counters with low-cardinality `reason` attributes.
- No `console.*` anywhere in `apps/api/src`; execute.ts untouched (08-03 ownership).
</verification>

<success_criteria>
- API emits admission + ratelimit rejection counters via OTLP push (low-cardinality; `job_id` never on metrics).
- API is fully migrated off console.* to pino across all request/job paths.
- File ownership clean: this plan does not touch execute.ts (08-03) or any Go file (08-04).
</success_criteria>

<output>
Create `.planning/phases/08-distributed-observability/08-04b-SUMMARY.md` when done.
</output>
