---
spike: 002
name: crun-vs-runc
type: comparison
validates: "Given the prod sandbox config, when launched via crun vs runc, then per-container RAM and/or cold-start latency differ measurably"
verdict: VALIDATED
related: [001, 005]
tags: [density, runtime, crun, runc]
---

# Spike 002: crun vs runc

## What This Validates

Given the prod sandbox launch config, when the OCI runtime is swapped runc → crun
(`HostConfig.Runtime`, registered via `/etc/docker/daemon.json` + SIGHUP reload),
then per-container RAM and/or startup latency improve measurably.

## How to Run

`_harness/density-harness.sh` — configs `baseline-runc` vs `crun`, plus the
`startup_ms` micro-benchmark (8 trivial `docker run`s per runtime).

## Results — crun WINS startup, density TIE

| Metric | runc 1.2.4 | crun 1.20 | Δ |
|---|---|---|---|
| Density ceiling | 30 | 30 | **tie** |
| Per-container RAM | 116.8 MB | 116.7 MB | tie (noise) |
| **Cold start** | 256 ms | **207 ms** | **−19% (−49 ms)** |

crun does **not** change density — per-container overhead is negligible against
the 110 MB Python footprint, so both hit the same RAM ceiling. Its win is
**cold-start latency** (−19%) and lower per-op CPU, which matters for short-job
throughput and burst scale-up, not for how many live sandboxes fit.

## Investigation Trail

- crun installs cleanly on the docker:dind Alpine base (`apk add crun`) and
  registers via daemon.json `runtimes` + `kill -HUP dockerd` (no restart).
- Ran crun under both KSM-on and KSM-off (spike 001) — no density divergence from
  runc in any config.

## Signal for the build

Adopt crun as the default OCI runtime — it's a one-line `HostConfig.Runtime`
change behind the existing `Runner` interface, free on the dind base, and buys
−19% cold start. Just don't sell it as a density lever; it isn't one.
