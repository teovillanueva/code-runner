#!/usr/bin/env bash
# scripts/e2e.sh — End-to-end interactive Python execute test
#
# Brings up the local dev stack, runs a full interactive execute round-trip
# through the real services, asserts the expected output, and tears down.
#
# Flow:
#   1. Ensure .env is present (copy .env.example if not)
#   2. Build the Python sandbox image on the host daemon (make python-image)
#   3. Bring up redis + soketi + api + worker (background)
#   4. Wait for API health endpoint to return 200
#   5. Run the stub (docker compose run --rm stub)
#   6. Assert the stub exited 0 and printed "hello World"
#   7. Tear down (docker compose down -v) — always, even on failure
#
# Usage:
#   ./scripts/e2e.sh
#   make e2e
#
# Prerequisites:
#   - Docker Desktop running (host daemon accessible)
#   - Ports 6001 (soketi) + 8080 (api) free
#   - executor/python:3.12 will be built automatically if missing
#
# Environment:
#   All config is read from .env (or .env.example defaults).
#   Override any var: VAR=val ./scripts/e2e.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

# ── Colours ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m' # No colour

pass() { echo -e "${GREEN}[PASS]${NC} $*"; }
fail() { echo -e "${RED}[FAIL]${NC} $*"; }
info() { echo -e "${YELLOW}[info]${NC} $*"; }

# ── Cleanup trap ──────────────────────────────────────────────────────────────
# Always tear down the stack on exit (success or failure).
cleanup() {
  local exit_code=$?
  info "tearing down stack..."
  docker compose down -v --remove-orphans 2>/dev/null || true
  if [ $exit_code -eq 0 ]; then
    pass "Stack torn down cleanly."
  else
    fail "E2E failed — stack torn down."
  fi
  exit $exit_code
}
trap cleanup EXIT

# ── 1. Ensure .env exists ─────────────────────────────────────────────────────
if [ ! -f "$PROJECT_ROOT/.env" ]; then
  info ".env not found — copying from .env.example"
  cp "$PROJECT_ROOT/.env.example" "$PROJECT_ROOT/.env"
fi

# Load .env into environment (skip comments and empty lines)
# shellcheck disable=SC1090
set -a
source <(grep -v '^#' "$PROJECT_ROOT/.env" | grep -v '^[[:space:]]*$') 2>/dev/null || true
set +a

API_PORT="${API_PORT:-8080}"
EXECUTOR_API_TOKEN="${EXECUTOR_API_TOKEN:-dev-insecure-token-change-me}"

# ── 2. Build the Python sandbox image ────────────────────────────────────────
info "building executor/python:3.12 on host daemon..."
docker build -t executor/python:3.12 "$PROJECT_ROOT/languages/python-3.12"
pass "executor/python:3.12 ready"

# ── 3. Bring up the core stack ────────────────────────────────────────────────
info "bringing up redis + soketi + api + worker..."
docker compose up -d --build redis soketi api worker

# ── 4. Wait for API health ────────────────────────────────────────────────────
info "waiting for API to be healthy (port ${API_PORT})..."
HEALTH_URL="http://localhost:${API_PORT}/health"
MAX_WAIT=120
WAITED=0

while true; do
  HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HEALTH_URL" 2>/dev/null || echo "000")
  if [ "$HTTP_STATUS" = "200" ]; then
    pass "API is healthy"
    break
  fi

  if [ "$WAITED" -ge "$MAX_WAIT" ]; then
    fail "API did not become healthy within ${MAX_WAIT}s"
    info "API logs:"
    docker compose logs api --tail=40
    info "Worker logs:"
    docker compose logs worker --tail=40
    exit 1
  fi

  sleep 2
  WAITED=$((WAITED + 2))
  echo -n "."
done

# Also wait for soketi to be ready (the worker needs it)
info "verifying soketi is reachable..."
SOKETI_PORT="${SOKETI_PORT:-6001}"
SOKETI_MAX_WAIT=30
SOKETI_WAITED=0
while true; do
  SOKETI_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:${SOKETI_PORT}/" 2>/dev/null || echo "000")
  if [ "$SOKETI_STATUS" = "200" ]; then
    pass "soketi is healthy"
    break
  fi
  if [ "$SOKETI_WAITED" -ge "$SOKETI_MAX_WAIT" ]; then
    fail "soketi did not become healthy within ${SOKETI_MAX_WAIT}s"
    docker compose logs soketi --tail=20
    exit 1
  fi
  sleep 2
  SOKETI_WAITED=$((SOKETI_WAITED + 2))
  echo -n "."
done

# ── 5. Run the stub ───────────────────────────────────────────────────────────
info "running stub (interactive E2E driver)..."
echo ""

# Capture output to both terminal and a variable for assertion
STUB_OUTPUT_FILE=$(mktemp)

# Run the stub with --no-TTY (batch mode for CI compatibility).
# Use --profile stub to enable the stub service, or build+run directly.
STUB_EXIT=0
docker compose run --rm \
  -e EXECUTOR_API_TOKEN="${EXECUTOR_API_TOKEN}" \
  -e API_BASE_URL="http://api:${API_PORT}" \
  -e SOKETI_APP_KEY="${SOKETI_APP_KEY:-code-runner-key}" \
  -e SOKETI_APP_SECRET="${SOKETI_APP_SECRET:-code-runner-secret}" \
  -e SOKETI_HOST="soketi" \
  -e SOKETI_PORT="6001" \
  -e SOKETI_USE_TLS="false" \
  -e CHANNEL_AUTH_URL="http://api:${API_PORT}/v1/channel-auth" \
  --profile stub \
  stub 2>&1 | tee "$STUB_OUTPUT_FILE" || STUB_EXIT=$?

echo ""

STUB_OUTPUT=$(cat "$STUB_OUTPUT_FILE")
rm -f "$STUB_OUTPUT_FILE"

# ── 6. Assert ─────────────────────────────────────────────────────────────────
PASS=true

if [ "$STUB_EXIT" -ne 0 ]; then
  fail "stub exited with code $STUB_EXIT"
  PASS=false
fi

if echo "$STUB_OUTPUT" | grep -qi "hello World"; then
  pass "Found 'hello World' in stub output"
else
  fail "'hello World' NOT found in stub output"
  PASS=false
fi

if echo "$STUB_OUTPUT" | grep -qi "exitCode=0"; then
  pass "exitCode=0 confirmed in result event"
else
  fail "exitCode=0 not seen in stub output"
  PASS=false
fi

echo ""
if [ "$PASS" = "true" ]; then
  pass "===== E2E PASS: interactive execute hello World round-trip succeeded ====="
  exit 0
else
  fail "===== E2E FAIL: see output above ====="
  info "API logs:"
  docker compose logs api --tail=40
  info "Worker logs:"
  docker compose logs worker --tail=60
  exit 1
fi
