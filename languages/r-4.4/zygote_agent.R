#!/usr/bin/env Rscript
# code-runner R zygote agent (Phase 11, AGENT / ZHARD) — P1.
#
# Thin R driver over the native helper libzygote_hard.so. R cannot call
# unshare()/mount()/prctl()/setresuid() or run a tight framed relay loop, so ALL
# of that lives in C (zygote_hard.c). R only:
#   1. pre-imports the manifest CRAN stack ONCE (design RULE #4 — CoW base), then
#   2. calls .Call("zyg_serve", port) which blocks forever, accepting jobs,
#      double-forking + hardening each session, and sourcing the entrypoint
#      in-process (CoW over the pre-loaded packages).
#
# Invoked ONLY when the worker launches the R image as a long-lived, PRIVILEGED
# pool container with an explicit command:
#   Rscript /opt/zygote/zygote_agent.R
# The image's normal run path (DockerSocketRunner -> `Rscript main.R`) is
# UNCHANGED.
#
# ============================ STATUS: WIP =====================================
# The Python agent (P0) is fully implemented + locally tested (11/11 checks).
# This R native path (P1) compiles but the embedded-R source() callback from a
# double-forked child has NOT been hardened + locally tested to the same bar.
# Per the design's risk valve, R may ship on the Docker tier instead (remove
# `preimport` from r-4.4/manifest.json in Phase 13). See
# .planning/decisions/ZYGOTE-R-STATUS.md before relying on this path.
# ============================================================================

# ---- locate + load the native helper ----------------------------------------
so_path <- Sys.getenv("ZYGOTE_HARD_SO", "/opt/zygote/libzygote_hard.so")
if (!file.exists(so_path)) {
  stop(sprintf("zygote native helper not found at %s (build it in the R image)", so_path))
}
dyn.load(so_path)

# ---- pre-import the manifest set (RULE #4) -----------------------------------
preimport <- Sys.getenv("ZYGOTE_PREIMPORT", "jsonlite,data.table,lpSolve,ggplot2")
mods <- trimws(strsplit(preimport, ",")[[1]])
mods <- mods[nzchar(mods)]
for (m in mods) {
  ok <- suppressWarnings(suppressMessages(
    tryCatch({ library(m, character.only = TRUE); TRUE },
             error = function(e) { message(sprintf("preimport %s failed: %s", m, conditionMessage(e))); FALSE })
  ))
}
message(sprintf("[zygote-agent-R] pre-imported: %s", paste(mods, collapse = ", ")))

# ---- serve (blocks forever) --------------------------------------------------
port <- as.integer(Sys.getenv("ZYGOTE_RELAY_PORT", "7000"))
message(sprintf("[zygote-agent-R] starting relay on port %d", port))
invisible(.Call("zyg_serve", port))
