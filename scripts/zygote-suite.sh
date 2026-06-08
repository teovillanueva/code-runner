#!/usr/bin/env bash
# scripts/zygote-suite.sh — Phase 14 ZygoteRunner safety/abuse/isolation/density
# verification suite (ZTEST-01..04 + ZOBS-01..02).
#
# This is the gate the orchestrator runs on a LINUX / dind host (Fly) BEFORE the
# zygote tier is trusted in production. It runs:
#
#   1. The Docker-FREE pool-observability metrics unit tests (always; ZOBS-01..02).
#   2. The privileged integration suite (ZTEST-01..04): abuse parity (fork bomb,
#      OOM, infinite loop, idle, EOF, giant output), cross-child isolation,
#      density (CoW one-parent-for-N), and the no-leak sweep — driven through the
#      ZygoteRunner + REAL privileged python pool agent under the internal/session
#      three-clock supervisor.
#
# WHY LINUX/DIND: the integration suite needs the worker process to reach the
# pool container's Docker-network (bridge) IP. On macOS Docker Desktop that IP is
# not routable from a host process, so those tests SKIP cleanly there (they still
# COMPILE and the metrics unit tests still PASS). On Fly the worker runs INSIDE
# dind, so the bridge IP is reachable and every assertion runs for real.
#
# REQUIRED ENVIRONMENT (Linux/Fly):
#   - Docker daemon reachable (DOCKER_HOST or /var/run/docker.sock).
#   - cgroup v2 host (for per-child memory.max / pids.max / cpu.stat).
#   - The pool container runs --privileged --cgroupns=host: the host must permit
#     it (Fly Firecracker microVM is the real boundary — see
#     ZYGOTE-PRODUCTION-DESIGN.md "Fly-only security posture").
#   - executor/python:3.12 image built locally (the agent is baked at
#     /opt/zygote/zygote_agent.py). Built automatically below if missing.
#   - Go toolchain (1.26.x) on PATH.
#   - Redis is NOT required: this suite drives the ZygoteRunner directly, not the
#     full Redis→worker path (that is internal/worker/abuse_test.go).
#
# USAGE:
#   bash scripts/zygote-suite.sh          # full suite
#   bash scripts/zygote-suite.sh --unit   # metrics unit tests only (no Docker)
#
# EXIT: non-zero on any failure. SKIPs (host-unroutable) do NOT fail the run.
set -euo pipefail

cd "$(dirname "$0")/.."

UNIT_ONLY=0
if [[ "${1:-}" == "--unit" ]]; then
  UNIT_ONLY=1
fi

echo "==> [1/3] Pool observability metrics unit tests (Docker-free, ZOBS-01..02)"
go test ./internal/runner/... \
  -run 'ZygoteForkDurationHistogram|ZygoteParentReapCounter|ZygoteParentRespawnCounter|SandboxTerminalCounter|WarmParentGauge' \
  -v

if [[ "$UNIT_ONLY" == "1" ]]; then
  echo "==> --unit: skipping privileged integration suite."
  exit 0
fi

echo "==> [2/3] Ensuring executor/python:3.12 image is present"
if ! docker image inspect executor/python:3.12 >/dev/null 2>&1; then
  echo "    building executor/python:3.12 ..."
  docker build -t executor/python:3.12 languages/python-3.12
fi

echo "==> [3/3] Privileged zygote integration suite (ZTEST-01..04)"
echo "    (host-unroutable env SKIPs cleanly; Linux/dind runs all assertions)"
go test -tags=docker -timeout 900s ./internal/runner/... -run Zygote -v

echo "==> zygote-suite: done."
