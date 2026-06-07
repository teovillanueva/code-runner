#!/usr/bin/env Rscript
# R zygote / CoW fork probe (spike 006c, multi-language base cost).
#
# Mirrors zygote.py for R: load the baked CRAN stack ONCE in a parent, then fork
# one child per "session" via parallel::mcparallel (which uses fork() -> CoW). Each
# child allocates a UNIQUE ~40 MB vector and blocks. Reports the parent's resident
# base (the per-language import footprint paid once) and the marginal per child.
suppressMessages({
  library(parallel)
  library(jsonlite)
  library(data.table)
  library(lpSolve)
  library(ggplot2)
})

readmeta <- function(key) {
  ln <- grep(key, readLines("/proc/meminfo"), value = TRUE)
  as.integer(sub("[^0-9]*([0-9]+).*", "\\1", ln))
}
memavail <- function() readmeta("MemAvailable")
rss_kb <- function() {
  ln <- grep("VmRSS", readLines("/proc/self/status"), value = TRUE)
  as.integer(sub("[^0-9]*([0-9]+).*", "\\1", ln))
}

safety <- as.integer(Sys.getenv("SAFETY_KB", "220000"))
cap <- as.integer(Sys.getenv("HARD_CAP", "260"))

base_rss <- rss_kb()
cat(sprintf("R_BASE_RSS_KB=%d\n", base_rss), file = stderr())
base_av <- memavail()

n <- 0
kids <- list()
while (n < cap) {
  if (memavail() < safety) break
  p <- parallel::mcparallel({
    x <- runif(5e6)  # ~40 MB unique
    x[1] <- 1
    Sys.sleep(3600)
  })
  kids[[length(kids) + 1]] <- p
  n <- n + 1
  Sys.sleep(2)
}
after <- memavail()
used <- base_av - after
per <- if (n > 0) used %/% n else 0
cat(sprintf(
  "ZYGOTE_R ceiling=%d used_kb=%d marginal_per_child_kb=%d base_rss_kb=%d base_av_kb=%d\n",
  n, used, per, base_rss, base_av), file = stderr())
Sys.sleep(15)
