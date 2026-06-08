# ZygoteRunner — R language status (Phase 11)

**Decision: ship R on the Docker tier for now. R does NOT run on the
ZygoteRunner in v1.1.** Python is the validated zygote language (P0); R was P1
behind an explicit risk valve in `ZYGOTE-PRODUCTION-DESIGN.md`, and that valve is
being exercised.

## What was done

- `languages/r-4.4/zygote_hard.c` — a native helper exposing the full Rule-#2
  hardening + framed relay protocol (HELLO/STDIN/STDIN_CLOSE/KILL inbound,
  STARTED/STDOUT/STDERR/CPU/EXIT outbound), double-fork for a private PID
  namespace, distinct UID, `no_new_privs`, `CLONE_NEWNET`, `CLONE_NEWNS` +
  rec-private `/` + private `/tmp` tmpfs + remounted `/proc`, per-child cgroup-v2
  leaf (`memory.max`/`pids.max`) with a `/proc/<pid>/stat` CPU fallback, fd-scrub,
  and a `select()` relay loop mirroring the proven Python agent.
- `languages/r-4.4/zygote_agent.R` — a thin driver: pre-imports the manifest CRAN
  stack (RULE #4) then `.Call("zyg_serve", port)`.
- **The `.so` COMPILES CLEANLY** in the R image (`R CMD SHLIB zygote_hard.c`,
  gcc 14, only after adding `<grp.h>` + `<R_ext/Parse.h>`). That de-risks a future
  Phase-12 revisit of R.

Both files carry a prominent `STATUS: WIP` header.

## Why R is NOT shipping on the zygote tier in v1.1

1. **Embedded-R eval after a double-fork is the exact fragility the design
   flagged.** `zyg_serve` runs inside an already-initialized `Rscript` runtime; the
   session child calls `R_ParseVector` + `R_tryEval(source(entrypoint))` AFTER
   `unshare(CLONE_NEWPID/NEWNET/NEWNS)` + `setresuid()`. R's runtime was not
   designed to keep evaluating in a forked child that has changed UID and entered
   fresh namespaces (signal handlers, the R allocator, the connection/IO layer,
   and `longjmp`-based error handling are all suspect post-fork). This needs
   careful validation that is not yet done.
2. **Job-file staging into the child's private tmpfs is not implemented.** The
   Python agent hands files to the child via `fork()` and materializes them into
   the child's own `/tmp/work` (total cross-child isolation, validated 11/11). The
   C helper currently sources a bare `entrypoint` without staging the HELLO
   `files[]` — incomplete.
3. **No local test to Python's bar.** The Python agent passes all 11 self-test
   checks (STARTED, stdout, stdin+EOF, EXIT codes, CPU frames, KILL, cross-child
   isolation) in a privileged Docker Desktop container. R has not been driven
   through the relay protocol end-to-end.
4. **R gets little CoW benefit relative to the risk.** Spike 006c measured R's
   per-language base; the density win exists but R's exam workloads are lower
   volume than Python's, so the cost/benefit of hardening the embedded-R fork
   path does not justify blocking the milestone on it.

## Recommended action (Phase 13)

- **Remove `preimport` from `languages/r-4.4/manifest.json`** so
  `manifest.ZygoteEligible(r)` returns false and the `TieredRunner` routes R to
  the existing `DockerSocketRunner`. R then STILL WORKS in prod exactly as today
  (per-job hardened container), just without the warm-pool CoW tier.
- Keep `zygote_hard.c` / `zygote_agent.R` in the tree as WIP for a future revisit
  (the `.so` already compiles).

## To revisit R on the zygote tier later

1. Implement HELLO `files[]` staging into the child's private `/tmp/work`.
2. Validate embedded-R `source()` from a double-forked, UID-dropped, namespaced
   child — or switch the child to `execve("Rscript", entrypoint)` (loses the CoW
   pre-import benefit, so only worth it if eval-after-fork proves unsafe).
3. Drive `zygote_agent.R` through the same relay self-test the Python agent passes
   (port `zygote_selftest.py` to target the R image).
