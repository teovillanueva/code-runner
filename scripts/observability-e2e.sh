#!/usr/bin/env bash
# scripts/observability-e2e.sh — End-to-end CONNECTED-TRACE proof.
#
# Brings up the dev stack WITH the example observability backend (otel-collector
# + Jaeger, the `observability` compose profile), runs one full interactive
# execute round-trip through the real services, then shows you how to confirm a
# single connected trace — the API `execute` span AND the worker phase spans
# (claim, sandbox.create, handshake.wait, compile, run, publish.result) sharing
# ONE trace_id — in the Jaeger UI, plus the collector `debug` exporter showing
# the worker's metrics + trace-correlated logs.
#
# Flow:
#   1. Ensure .env exists and OTEL_EXPORTER_OTLP_ENDPOINT is enabled.
#   2. Build the Python sandbox image on the host daemon (make python-image).
#   3. Bring up redis + soketi + api + worker + otel-collector + jaeger.
#   4. Wait for the API to be healthy.
#   5. Run the stub (one interactive execute round-trip).
#   6. Print the Jaeger UI URL + tail the collector debug output (metrics/logs).
#   7. Leave the stack UP so you can inspect Jaeger; print the teardown command.
#
# Usage:   bash scripts/observability-e2e.sh
#          KEEP_UP=0 bash scripts/observability-e2e.sh   # tear down at the end
#
# Prerequisites: Docker Desktop running; ports 8080, 6001, 16686 free.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
pass() { echo -e "${GREEN}[PASS]${NC} $*"; }
fail() { echo -e "${RED}[FAIL]${NC} $*"; }
info() { echo -e "${YELLOW}[info]${NC} $*"; }

KEEP_UP="${KEEP_UP:-1}"
COMPOSE=(docker compose --profile observability)
JAEGER_UI="http://localhost:16686"

cleanup_on_fail() {
  local code=$?
  if [ $code -ne 0 ]; then
    fail "observability E2E failed — tearing the stack down."
    "${COMPOSE[@]}" logs api --tail=30 || true
    "${COMPOSE[@]}" logs worker --tail=40 || true
    "${COMPOSE[@]}" logs otel-collector --tail=40 || true
    "${COMPOSE[@]}" down -v --remove-orphans 2>/dev/null || true
  fi
  exit $code
}
trap cleanup_on_fail EXIT

# ── 1. Ensure .env with OTEL enabled ──────────────────────────────────────────
if [ ! -f "$PROJECT_ROOT/.env" ]; then
  info ".env not found — copying from .env.example"
  cp "$PROJECT_ROOT/.env.example" "$PROJECT_ROOT/.env"
fi

# Make sure the OTLP endpoint is set (the ON switch) so the api+worker export.
if ! grep -Eq '^OTEL_EXPORTER_OTLP_ENDPOINT=' "$PROJECT_ROOT/.env"; then
  info "enabling OTEL_EXPORTER_OTLP_ENDPOINT in .env (points at the example collector)"
  printf '\nOTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318\n' >> "$PROJECT_ROOT/.env"
fi

set -a
# shellcheck disable=SC1090
source <(grep -v '^#' "$PROJECT_ROOT/.env" | grep -v '^[[:space:]]*$') 2>/dev/null || true
set +a
API_PORT="${API_PORT:-8080}"
EXECUTOR_API_TOKEN="${EXECUTOR_API_TOKEN:-dev-insecure-token-change-me}"

# ── 2. Build the Python sandbox image ────────────────────────────────────────
info "building executor/python:3.12 on host daemon..."
docker build -t executor/python:3.12 "$PROJECT_ROOT/languages/python-3.12" >/dev/null
pass "executor/python:3.12 ready"

# ── 3. Bring up the stack + observability backend ─────────────────────────────
info "bringing up redis + soketi + api + worker + otel-collector + jaeger..."
"${COMPOSE[@]}" up -d --build redis soketi api worker otel-collector jaeger

# ── 4. Wait for API health ────────────────────────────────────────────────────
info "waiting for API to be healthy..."
MAX_WAIT=120; WAITED=0
while true; do
  HTTP_STATUS=$("${COMPOSE[@]}" exec -T api node -e \
    "require('http').get('http://localhost:${API_PORT}/health',(r)=>{let d='';r.on('data',c=>d+=c);r.on('end',()=>{try{const j=JSON.parse(d);process.stdout.write(String(r.statusCode));process.exit(0);}catch(e){process.stdout.write('500');process.exit(1);}});}).on('error',()=>{process.stdout.write('000');process.exit(1);});" 2>/dev/null || echo "000")
  [ "$HTTP_STATUS" = "200" ] && { pass "API is healthy"; break; }
  [ "$WAITED" -ge "$MAX_WAIT" ] && { fail "API did not become healthy in ${MAX_WAIT}s"; exit 1; }
  sleep 3; WAITED=$((WAITED + 3)); echo -n "."
done

# ── 5. Run one interactive execute round-trip via the stub ───────────────────
info "running stub (one interactive execute → emits the connected trace)..."
echo ""
"${COMPOSE[@]}" build stub >/dev/null 2>&1
STUB_EXIT=0
"${COMPOSE[@]}" run --rm \
  -e EXECUTOR_API_TOKEN="${EXECUTOR_API_TOKEN}" \
  -e API_BASE_URL="http://api:${API_PORT}" \
  -e SOKETI_APP_KEY="${SOKETI_APP_KEY:-code-runner-key}" \
  -e SOKETI_APP_SECRET="${SOKETI_APP_SECRET:-code-runner-secret}" \
  -e SOKETI_HOST="soketi" -e SOKETI_PORT="6001" -e SOKETI_USE_TLS="false" \
  -e CHANNEL_AUTH_URL="http://api:${API_PORT}/v1/channel-auth" \
  stub || STUB_EXIT=$?
echo ""
[ "$STUB_EXIT" -eq 0 ] && pass "interactive execute round-trip completed" \
  || { fail "stub exited $STUB_EXIT"; exit 1; }

# ── 6. Show the collector debug output (metrics + trace-correlated logs) ──────
info "collector debug output (look for traces, code_runner.* metrics, log records):"
"${COMPOSE[@]}" logs otel-collector --tail=60 2>/dev/null \
  | grep -Ei 'trace_id|span|code_runner|metric|LogRecord|ScopeSpans' | head -40 || true

# ── 7. Point at Jaeger + leave the stack up to inspect ────────────────────────
echo ""
pass "Connected-trace stack is up."
cat <<EOF

  ┌─ VERIFY THE ONE CONNECTED TRACE ──────────────────────────────────────────
  │ 1. Open Jaeger:   ${JAEGER_UI}
  │ 2. Service:       code-runner-api   → Find Traces → open the latest trace.
  │ 3. Confirm ONE trace_id contains BOTH:
  │      • the API   'execute' span, and
  │      • the worker phase spans: claim, sandbox.create, handshake.wait,
  │        compile, run, publish.result   (worker linked to the execute span).
  │ 4. The collector logs above should show code_runner.* metrics + log records.
  └────────────────────────────────────────────────────────────────────────────

EOF

if [ "$KEEP_UP" = "1" ]; then
  trap - EXIT
  info "stack left running so you can inspect Jaeger at ${JAEGER_UI}"
  info "tear down with:  docker compose --profile observability down -v"
else
  info "KEEP_UP=0 — tearing the stack down."
  trap - EXIT
  "${COMPOSE[@]}" down -v --remove-orphans 2>/dev/null || true
  pass "stack torn down."
fi
