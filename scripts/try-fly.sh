#!/usr/bin/env bash
# Self-contained demo against the deployed code-runner gateway on Fly.
# Runs an INTERACTIVE python job and streams the live output from soketi.
# Requires: node (>=18) and curl. No repo checkout needed.
set -euo pipefail

API="${API:-https://code-runner-api.fly.dev}"
# Your EXECUTOR_API_TOKEN — pass it via env, never hard-code a secret:
#   TOKEN=xxxx bash try-fly.sh
TOKEN="${TOKEN:?Set TOKEN env var to your EXECUTOR_API_TOKEN}"
SOKETI_HOST="${SOKETI_HOST:-code-runner-soketi.fly.dev}"
SOKETI_KEY="${SOKETI_KEY:-code-runner-key}"

echo "→ API: $API"
echo "→ languages:"
curl -s -H "Authorization: Bearer $TOKEN" "$API/v1/languages" | sed 's/},{/}\n  {/g'; echo

# Temp node project with a tiny WebSocket client (raw Pusher protocol).
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
cd "$WORK"
echo "→ installing ws (temp)…"
npm init -y >/dev/null 2>&1
npm i ws >/dev/null 2>&1

API="$API" TOKEN="$TOKEN" SOKETI_HOST="$SOKETI_HOST" SOKETI_KEY="$SOKETI_KEY" node --input-type=module - <<'NODE'
import WebSocket from 'ws';
const { API, TOKEN, SOKETI_HOST, SOKETI_KEY } = process.env;
const H = { 'Authorization': `Bearer ${TOKEN}`, 'Content-Type': 'application/json' };
const post = (p, b) => fetch(`${API}${p}`, { method: 'POST', headers: H, body: b ? JSON.stringify(b) : undefined });
const parse = d => (typeof d === 'string' ? JSON.parse(d) : d);

// 1) enqueue an interactive job (reads a name, greets it)
const program = 'name = input("name? "); print(f"hello {name}")';
const ex = await (await fetch(`${API}/v1/execute`, { method: 'POST', headers: H,
  body: JSON.stringify({ language: 'python', files: [{ name: 'main.py', content: program }] }) })).json();
const { jobId, channel } = ex;
console.log('→ jobId:', jobId, '| channel:', channel);

// 2) open the soketi WebSocket (Pusher protocol)
const ws = new WebSocket(`wss://${SOKETI_HOST}/app/${SOKETI_KEY}?protocol=7&client=demo&version=1.0`);
let sentStdin = false;

ws.on('message', async (buf) => {
  const msg = JSON.parse(buf.toString());
  const data = msg.data ? parse(msg.data) : {};
  switch (msg.event) {
    case 'pusher:connection_established': {
      // 3) authorize the private channel via the API helper (bearer only), then subscribe
      const auth = await (await post('/v1/channel-auth', { socket_id: data.socket_id, channel_name: channel })).json();
      ws.send(JSON.stringify({ event: 'pusher:subscribe', data: { channel, auth: auth.auth } }));
      break;
    }
    case 'pusher_internal:subscription_succeeded':
      console.log('→ subscribed; starting process');
      await post(`/v1/jobs/${jobId}/start`);          // 4) start AFTER subscribing (handshake)
      break;
    case 'stage':  console.log('   stage :', data.phase); break;
    case 'stderr': process.stdout.write('   stderr: ' + data.chunk); break;
    case 'stdout':
      process.stdout.write('   stdout: ' + data.chunk);
      if (!sentStdin) {                                // 5) saw the prompt → send stdin + EOF
        sentStdin = true;
        await post(`/v1/jobs/${jobId}/stdin`, { chunk: 'World\n' });
        await post(`/v1/jobs/${jobId}/stdin/close`);
      }
      break;
    case 'result':
      console.log('→ result:', JSON.stringify(data));
      ws.close();
      process.exit(data.exitCode === 0 ? 0 : 1);
  }
});
ws.on('error', e => { console.error('ws error:', e.message); process.exit(2); });
setTimeout(() => { console.error('timeout (60s)'); process.exit(2); }, 60000);
NODE
