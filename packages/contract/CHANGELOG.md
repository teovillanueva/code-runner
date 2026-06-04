# @teovilla/code-runner-contract

## 0.3.0

### Minor Changes

- 81cac83: Separate compile-stage output from run output (Piston-style), with a real-time build log.
  - **contract**: add a `CompileResult` type and an optional `RunResult.compile` field (stdout/stderr/output/exitCode/durationMs), so a compiled-language run keeps its build logs distinct from the program's stdout/stderr. Add a `compile_output` soketi event (`events.compileOutput`) carrying the live, interleaved build log emitted during the `compiling` stage.
  - **react**: `useCodeRunnerJob` now returns `compileOutput` — the live build log reassembled from `compile_output` events, separate from `stdout`/`stderr` — so consumers can render a dedicated real-time build panel.

## 0.2.0

### Minor Changes

- 8eca3ae: Phase 9 — pullable run output & artifacts.
  - **contract**: add artifact wire types and the artifact event/`RunResult` surface to the shared schema.
  - **sdk-node**: add `CodeRunnerClient.getOutput(id)` returning a typed `RunResult` (Redis-backed pull of a finished run).
  - **react**: `useCodeRunnerJob` now exposes `artifacts[]` from soketi artifact events.
