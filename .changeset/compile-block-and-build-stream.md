---
"@teovilla/code-runner-contract": minor
"@teovilla/code-runner-react": minor
---

Separate compile-stage output from run output (Piston-style), with a real-time build log.

- **contract**: add a `CompileResult` type and an optional `RunResult.compile` field (stdout/stderr/output/exitCode/durationMs), so a compiled-language run keeps its build logs distinct from the program's stdout/stderr. Add a `compile_output` soketi event (`events.compileOutput`) carrying the live, interleaved build log emitted during the `compiling` stage.
- **react**: `useCodeRunnerJob` now returns `compileOutput` — the live build log reassembled from `compile_output` events, separate from `stdout`/`stderr` — so consumers can render a dedicated real-time build panel.
