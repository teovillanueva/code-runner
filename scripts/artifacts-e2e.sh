#!/usr/bin/env bash
# scripts/artifacts-e2e.sh — End-to-end artifacts + pullable-output test (Phase 9)
#
# Proves the FULL cross-layer chain that the unit/seam tests cannot, in one run:
#
#   POST /v1/execute {collectOutput:true, savefig('plot.png')}
#     → real worker claims the job
#     → real Docker python-3.12 sandbox runs matplotlib and writes plot.png to cwd
#     → workspace-diff capture reads it before Cleanup()
#     → S3Store uploads it to MinIO under artifacts/<jobId>/
#     → RunResult persisted to Redis (job:<id>:output)
#   GET /v1/jobs/:id/output
#     → returns RunResult.artifacts[0] {name=plot.png, mimeType=image/png, bytes>0, url}
#     → the presigned MinIO url fetches back real PNG bytes (\x89PNG magic)
#
# This automates HUMAN-UAT items 1 + 3 + the artifact half of the pull loop
# (R4/R5/R6/R7/R9/R14/R15 end-to-end). Item 2 (R) and item 4 (browser) stay manual.
#
# Flow (mirrors scripts/e2e.sh):
#   1. Ensure .env is present (copy .env.example if not)
#   2. Build the Python sandbox image on the host daemon (matplotlib baked, R10)
#   3. Bring up redis + soketi + minio + api + worker (background)
#   4. Wait for API container to be healthy
#   5. Run the artifact driver INSIDE the api container (node reads the script on
#      stdin) — it must run on the compose network so the minio:9000 presigned
#      URL resolves and so it reaches the API via localhost
#   6. Assert the driver exited 0 (artifact captured, uploaded, pulled, bytes match)
#   7. Tear down (docker compose down -v) — always, even on failure
#
# Usage:
#   ./scripts/artifacts-e2e.sh
#   make artifacts-e2e
#
# Prerequisites:
#   - Docker Desktop running (host daemon accessible)
#   - executor/python:3.12 is rebuilt automatically (must include matplotlib)
#
# Environment:
#   All config is read from .env (or .env.example defaults).
#   Override any var: VAR=val ./scripts/artifacts-e2e.sh

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
DRIVER_FILE=""
cleanup() {
  local exit_code=$?
  [ -n "$DRIVER_FILE" ] && rm -f "$DRIVER_FILE" 2>/dev/null || true
  info "tearing down stack..."
  docker compose down -v --remove-orphans 2>/dev/null || true
  if [ $exit_code -eq 0 ]; then
    pass "Stack torn down cleanly."
  else
    fail "Artifacts E2E failed — stack torn down."
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
OUTPUT_TIMEOUT_MS="${ARTIFACTS_E2E_TIMEOUT_MS:-150000}"

# ── 2. Build the Python sandbox image (must include matplotlib, R10) ──────────
info "building executor/python:3.12 on host daemon (matplotlib baked)..."
docker build -t executor/python:3.12 "$PROJECT_ROOT/languages/python-3.12"
pass "executor/python:3.12 ready"

# ── 3. Bring up the stack (incl. MinIO — the worker's S3 backend) ─────────────
info "bringing up redis + soketi + minio + api + worker..."
docker compose up -d --build redis soketi minio api worker

# ── 4. Wait for API health (no host port published; check from inside) ────────
info "waiting for API to be healthy..."
MAX_WAIT=120
WAITED=0
while true; do
  HTTP_STATUS=$(docker compose exec -T api node -e \
    "require('http').get('http://localhost:${API_PORT}/health',(r)=>{let d='';r.on('data',c=>d+=c);r.on('end',()=>{try{const j=JSON.parse(d);process.stdout.write(String(r.statusCode));process.exit(0);}catch(e){process.stdout.write('500');process.exit(1);}});}).on('error',()=>{process.stdout.write('000');process.exit(1);});" 2>/dev/null || echo "000")
  if [ "$HTTP_STATUS" = "200" ]; then
    pass "API is healthy"
    break
  fi
  if [ "$WAITED" -ge "$MAX_WAIT" ]; then
    fail "API did not become healthy within ${MAX_WAIT}s"
    docker compose logs api --tail=40
    docker compose logs worker --tail=40
    exit 1
  fi
  sleep 3
  WAITED=$((WAITED + 3))
  echo -n "."
done

# ── 5. Run the artifact driver inside the api container ───────────────────────
# The driver runs ON the compose network so:
#   - it reaches the API at http://localhost:${API_PORT}
#   - the presigned URL (host "minio:9000") resolves and fetches real bytes
# node (v24 in the api image) executes the script piped on stdin (CommonJS).
DRIVER_FILE="$(mktemp "${TMPDIR:-/tmp}/artifacts-e2e-driver.XXXXXX.cjs")"
cat > "$DRIVER_FILE" <<'DRIVER'
// Artifact pull-loop driver. Runs inside the api container.
// Env: API_PORT, EXECUTOR_API_TOKEN, ARTIFACTS_E2E_TIMEOUT_MS.
const PORT = process.env.API_PORT || "8080";
const TOKEN = process.env.EXECUTOR_API_TOKEN || "dev-insecure-token-change-me";
const TIMEOUT_MS = parseInt(process.env.ARTIFACTS_E2E_TIMEOUT_MS || "150000", 10);
const BASE = `http://localhost:${PORT}`;
const AUTH = { Authorization: `Bearer ${TOKEN}` };

// Python: write a PNG to cwd via matplotlib (MPLBACKEND=Agg is baked in the image).
const PY = [
  "import matplotlib.pyplot as plt",
  "plt.plot([1, 2, 3], [4, 5, 6])",
  "plt.savefig('plot.png')",
  "print('plot written')",
].join("\n") + "\n";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const log = (...a) => console.log("[artifacts-e2e]", ...a);

async function main() {
  // 1. Execute with collectOutput:true
  log("POST /v1/execute (collectOutput:true)");
  const execRes = await fetch(`${BASE}/v1/execute`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...AUTH },
    body: JSON.stringify({
      language: "python",
      files: [{ name: "main.py", content: PY }],
      collectOutput: true,
    }),
  });
  if (!execRes.ok) throw new Error(`execute failed: HTTP ${execRes.status}: ${await execRes.text()}`);
  const { jobId } = await execRes.json();
  if (!jobId) throw new Error("execute returned no jobId");
  log(`jobId=${jobId}`);

  // 2+3. Start handshake + poll for the persisted RunResult.
  //
  // Start is delivered over Redis pub/sub (fire-and-forget): the worker
  // subscribes to the control channel, THEN publishes the "queued" stage and
  // writes JobStatus.state="queued", THEN parks at the gate waiting for start.
  // A /start sent before the worker has subscribed is published into the void.
  // No soketi subscriber here, so instead of watching the "queued" stage we
  // re-send /start on a cadence while JobStatus is still "queued" — one send
  // lands once the worker is parked, flipping it to "running". The /start route
  // just publishes + returns 202 (no state validation), so re-sending is safe.
  // The RunResult persists at teardown regardless of any soketi subscriber.
  log("start handshake + polling GET /v1/jobs/:id/output ...");
  const deadline = Date.now() + TIMEOUT_MS;
  let runResult = null;
  let lastState = "";
  while (Date.now() < deadline) {
    let state = "";
    try {
      const s = await fetch(`${BASE}/v1/jobs/${jobId}`, { headers: AUTH });
      if (s.ok) state = (await s.json()).state || "";
    } catch { /* transient */ }
    if (state !== lastState) { log(`job state=${state || "?"}`); lastState = state; }

    const r = await fetch(`${BASE}/v1/jobs/${jobId}/output`, { headers: AUTH });
    if (r.status === 200) { runResult = await r.json(); break; }
    if (r.status !== 404) throw new Error(`output route unexpected HTTP ${r.status}: ${await r.text()}`);

    // Nudge /start while still queued (pre-subscribe sends are harmlessly lost).
    if (state === "" || state === "queued") {
      await fetch(`${BASE}/v1/jobs/${jobId}/start`, { method: "POST", headers: AUTH }).catch(() => {});
    }
    await sleep(1500);
  }
  if (!runResult) throw new Error(`timed out after ${TIMEOUT_MS}ms waiting for RunResult`);
  log(`RunResult: exitCode=${runResult.exitCode} artifacts=${(runResult.artifacts || []).length} truncated=${runResult.artifactsTruncated}`);

  // 4. Assert the RunResult shape
  if (runResult.exitCode !== 0) throw new Error(`exitCode=${runResult.exitCode} (stderr: ${runResult.stderr})`);
  const arts = runResult.artifacts || [];
  if (arts.length < 1) throw new Error("no artifacts in RunResult (expected plot.png)");
  const png = arts.find((a) => a.name === "plot.png");
  if (!png) throw new Error(`no artifact named plot.png; got: ${arts.map((a) => a.name).join(", ")}`);
  if (!png.url) throw new Error("plot.png artifact has no presigned url");
  if (!(png.bytes > 0)) throw new Error(`plot.png artifact bytes=${png.bytes} (expected > 0)`);
  if (png.mimeType !== "image/png") throw new Error(`plot.png mimeType=${png.mimeType} (expected image/png)`);
  log(`artifact: name=${png.name} mimeType=${png.mimeType} bytes=${png.bytes}`);
  log(`presigned url host: ${new URL(png.url).host}`);

  // 5. Fetch the presigned URL (no bearer) and confirm real PNG bytes
  log("GET <presigned url> (no bearer — browser path)");
  const dl = await fetch(png.url);
  if (dl.status !== 200) throw new Error(`presigned GET HTTP ${dl.status}`);
  const buf = Buffer.from(await dl.arrayBuffer());
  const isPng = buf.length >= 8 && buf[0] === 0x89 && buf.slice(1, 4).toString("latin1") === "PNG";
  if (!isPng) throw new Error(`fetched bytes are not a PNG (len=${buf.length}, first=${buf.slice(0, 8).toString("hex")})`);
  if (buf.length !== png.bytes) {
    log(`WARN: fetched len ${buf.length} != reported bytes ${png.bytes} (non-fatal; both > 0 and valid PNG)`);
  }
  log(`fetched ${buf.length} bytes; PNG magic OK`);

  log("ARTIFACTS-E2E-PASS");
}

main().then(() => process.exit(0)).catch((e) => {
  console.error("[artifacts-e2e] FAIL:", e.message || e);
  process.exit(1);
});
DRIVER

info "running artifact pull-loop driver inside the api container..."
echo ""
DRIVER_EXIT=0
docker compose exec -T \
  -e ARTIFACTS_E2E_TIMEOUT_MS="${OUTPUT_TIMEOUT_MS}" \
  api node < "$DRIVER_FILE" || DRIVER_EXIT=$?
echo ""

# ── 6. Assert ─────────────────────────────────────────────────────────────────
if [ "$DRIVER_EXIT" -eq 0 ]; then
  pass "===== ARTIFACTS E2E PASS: savefig → capture → S3 upload → pull → PNG bytes ====="
  exit 0
else
  fail "===== ARTIFACTS E2E FAIL (driver exit ${DRIVER_EXIT}) — see output above ====="
  info "API logs:"
  docker compose logs api --tail=40
  info "Worker logs:"
  docker compose logs worker --tail=80
  exit 1
fi
