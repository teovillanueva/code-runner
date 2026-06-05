# Spike Conventions

Patterns established across spike sessions. New spikes follow these unless the
question requires otherwise.

## Stack

- **Measurement target = the real prod VM shape**, not a local approximation. For
  worker capacity questions, measure on a **throwaway Fly Machine** matching the
  prod `[[vm]]` block (perf-2x / 4096 MB / gru), in the **edalef** org (where the
  code-runner apps + billing live — `personal` org can't create >1 GB volumes).
- The throwaway runs `docker:27-dind` + an ext4 volume at `/var/lib/docker`
  (overlay2 can't run on the Firecracker overlayfs rootfs — same constraint as
  the prod worker), env `DOCKER_TLS_CERTDIR=` for socket-only dockerd.
- Sandbox launch config mirrors prod exactly: `--read-only --network none
  --user 65534:65534 --memory 128m --memory-swap 128m --pids-limit 64 --cap-drop
  ALL --security-opt no-new-privileges`, image built from the real
  `languages/<lang>/Dockerfile`.

## Structure

- Shared measurement code lives in `_harness/` (POSIX `sh` for the dind/busybox
  box); each spike README references the relevant config rather than duplicating.
- Harnesses emit a JSON blob between `===CR_RESULTS_BEGIN===`/`===_END===` markers
  so results survive log noise and are greppable.

## Patterns

- **Transfer files to the box via stdin tar**, not sftp:
  `tar czf - … | flyctl ssh console -a <app> -C "/bin/sh -c 'tar xzf - -C /work'"`.
  Push a single script via `cat local | flyctl ssh … -C "cat > /work/x"`.
- **Run long jobs detached** (`nohup … >log 2>&1 &`) and poll the remote log with
  a local `until grep -q <terminal-marker>` loop as a background Bash task — Fly
  SSH sessions are not durable enough to hold a 15–20 min foreground run.
- **Always destroy the throwaway app when done** (`flyctl apps destroy <app>
  --yes`) — it takes the machine + volume with it. Stop the meter.
- **Measure the honest case, then the best case.** Use unique per-sandbox data
  (random buffers) for the conservative number; only then test identical-data /
  ASLR-off / CoW best cases, and label which is which.

## Gotchas (learned the hard way)

- busybox `date` has **no `%N`** → `apk add coreutils` for nanosecond startup
  timing. busybox `ps -o rss` is unreliable for daemon RSS → use `/proc/<pid>/status`.
- docker 27 **BuildKit** failed transiently building the science-stack image on
  the dind box; the **classic builder** (`DOCKER_BUILDKIT=0`) was reliable.
- A 128 MB cap OOMs if the workload imports the *whole* baked stack at once; the
  faithful active workload (≈ prod's 110 MB) is numpy + pandas + matplotlib + a
  ~40 MB buffer. scipy/sklearn/statsmodels/seaborn currently **crash** the prod
  image anyway (see `FINDINGS-prod-image-numpy-testing.md`).
