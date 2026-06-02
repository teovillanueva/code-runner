// apps/stub/src/index.ts
//
// Interactive E2E driver — plays the role of the upstream app.
//
// Flow:
//  1. POST /v1/execute — submit interactive Python program, capture {jobId, channel}
//  2. Connect pusher-js to soketi (wsHost/wsPort for self-hosted)
//  3. Subscribe to channel (private-run-<jobId>); auth via API's CHAN-02 helper
//  4. After subscription confirmed → POST /v1/jobs/:id/start
//  5. On seeing the input() prompt → POST /v1/jobs/:id/stdin {chunk:"World\n"}
//  6. POST /v1/jobs/:id/stdin/close (send EOF)
//  7. Await result event with exitCode 0; exit non-zero on timeout
//
// Channel auth (CHAN-01/CHAN-02 boundary):
//  Private channels require Pusher auth. In a real deployment this is done by
//  the upstream app. Here the stub calls the API's optional CHAN-02 helper
//  (POST /v1/channel-auth; requires ENABLE_CHANNEL_AUTH=true on the API).
//
// All parameters come from env vars so docker compose and scripts can drive it.

import Pusher from "pusher-js";
import { channelForJob, events } from "@code-runner/contract";

// ── Config from env ──────────────────────────────────────────────────────────

const API_BASE_URL = process.env["API_BASE_URL"] ?? "http://localhost:8080";
const EXECUTOR_API_TOKEN =
  process.env["EXECUTOR_API_TOKEN"] ?? "dev-insecure-token-change-me";
const SOKETI_APP_KEY = process.env["SOKETI_APP_KEY"] ?? "code-runner-key";
const SOKETI_APP_SECRET = process.env["SOKETI_APP_SECRET"] ?? "code-runner-secret";
const SOKETI_HOST = process.env["SOKETI_HOST"] ?? "localhost";
const SOKETI_PORT = parseInt(process.env["SOKETI_PORT"] ?? "6001", 10);
const SOKETI_USE_TLS = process.env["SOKETI_USE_TLS"] === "true";

// Timeout for the whole E2E (ms)
const E2E_TIMEOUT_MS = parseInt(process.env["E2E_TIMEOUT_MS"] ?? "60000", 10);

// The interactive Python program to run.
// It reads one line from stdin and prints "hello <input>"
const PYTHON_PROGRAM = `name = input("name? ")
print(f"hello {name}")
`;

// ── Channel auth (local HMAC signing) ────────────────────────────────────────
//
// Pusher private channel auth: HMAC-SHA256 over "<socket_id>:<channel_name>"
// signed with the app secret, returned as "<app_key>:<hmac>".
//
// Trust boundary (CHAN-01): in a real deployment the upstream app computes this
// using its own copy of the app secret. Here the stub has the secret from env
// (dev-only; never exposed in production) and signs locally — equivalent to
// the upstream app doing it.
//
// Note: pusher-js 8.x sends channel auth as application/x-www-form-urlencoded;
// using a custom handler here avoids the content-type mismatch with the API's
// JSON-only channel-auth helper.
import { createHmac } from "node:crypto";

function signChannel(socketId: string, channelName: string): string {
  const stringToSign = `${socketId}:${channelName}`;
  const hmac = createHmac("sha256", SOKETI_APP_SECRET)
    .update(stringToSign)
    .digest("hex");
  return `${SOKETI_APP_KEY}:${hmac}`;
}

// ── API helpers ──────────────────────────────────────────────────────────────

async function apiPost(path: string, body?: unknown): Promise<unknown> {
  const res = await fetch(`${API_BASE_URL}${path}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${EXECUTOR_API_TOKEN}`,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`POST ${path} failed: HTTP ${res.status}: ${text}`);
  }
  const contentType = res.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) {
    return res.json();
  }
  return res.text();
}

// ── Main E2E flow ────────────────────────────────────────────────────────────

async function run(): Promise<void> {
  console.log("[stub] starting interactive E2E");
  console.log(`[stub] API: ${API_BASE_URL}`);
  console.log(`[stub] soketi: ${SOKETI_HOST}:${SOKETI_PORT}`);

  // ── 1. Execute ──────────────────────────────────────────────────────────
  console.log("[stub] POST /v1/execute");
  const execResult = (await apiPost("/v1/execute", {
    language: "python",
    files: [{ name: "main.py", content: PYTHON_PROGRAM }],
  })) as { jobId: string; channel: string; status: string };

  const { jobId, channel } = execResult;
  const expectedChannel = channelForJob(jobId);

  console.log(`[stub] jobId=${jobId} channel=${channel}`);
  if (channel !== expectedChannel) {
    throw new Error(
      `Channel mismatch: got ${channel}, expected ${expectedChannel}`,
    );
  }

  // ── 2. Connect to soketi ─────────────────────────────────────────────────
  console.log("[stub] connecting to soketi");

  // pusher-js Node dist uses wsHost/wsPort for self-hosted soketi.
  // We use enabledTransports:['ws'] to skip SockJS fallback.
  // Note: pusher-js 8.x requires `cluster` even when wsHost is set (validation
  // check). We provide a dummy value; wsHost takes precedence in routing.
  //
  // Channel auth: pusher-js sends x-www-form-urlencoded but the API's CHAN-02
  // helper expects JSON, so we use a custom handler that signs locally with the
  // app secret (same pattern the upstream app uses in production).
  const pusher = new Pusher(SOKETI_APP_KEY, {
    cluster: "mt1",
    wsHost: SOKETI_HOST,
    wsPort: SOKETI_PORT,
    wssPort: SOKETI_PORT,
    forceTLS: SOKETI_USE_TLS,
    disableStats: true,
    enabledTransports: ["ws"],
    channelAuthorization: {
      transport: "ajax",
      endpoint: "http://localhost:1",   // unused — custom handler overrides
      customHandler: (
        params: { socketId: string; channelName: string },
        callback: (err: Error | null, data: { auth: string } | null) => void,
      ) => {
        try {
          const auth = signChannel(params.socketId, params.channelName);
          callback(null, { auth });
        } catch (err) {
          callback(err instanceof Error ? err : new Error(String(err)), null);
        }
      },
    },
  } as ConstructorParameters<typeof Pusher>[1]);

  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      pusher.disconnect();
      reject(
        new Error(
          `E2E timeout after ${E2E_TIMEOUT_MS}ms — did not see result event`,
        ),
      );
    }, E2E_TIMEOUT_MS);

    let sawHelloWorld = false;
    let startSent = false;
    let stdinSent = false;
    let closeSent = false;

    // ── 3. Subscribe to the private channel ────────────────────────────────
    const privateCh = pusher.subscribe(channel);

    privateCh.bind("pusher:subscription_error", (err: unknown) => {
      clearTimeout(timer);
      pusher.disconnect();
      const errStr = typeof err === "object" ? JSON.stringify(err) : String(err);
      reject(new Error(`Subscription error on ${channel}: ${errStr}`));
    });

    privateCh.bind(
      "pusher:subscription_succeeded",
      async (_data: unknown) => {
        console.log(`[stub] subscribed to ${channel}`);

        if (startSent) return;
        startSent = true;

        // ── 4. POST /start ─────────────────────────────────────────────────
        // The start-handshake: only send /start after subscription is confirmed.
        // This ensures the worker parks until we're ready to receive output.
        console.log("[stub] POST /v1/jobs/:id/start");
        try {
          await apiPost(`/v1/jobs/${jobId}/start`);
          console.log("[stub] start sent");
        } catch (err) {
          clearTimeout(timer);
          pusher.disconnect();
          reject(err);
        }
      },
    );

    // Helper: soketi delivers event data as a JSON string; parse it.
    function parseEventData<T>(raw: unknown): T {
      if (typeof raw === "string") {
        try { return JSON.parse(raw) as T; } catch { /* fall through */ }
      }
      return raw as T;
    }

    // ── 5. Bind stage events ──────────────────────────────────────────────
    privateCh.bind(events.stage, (raw: unknown) => {
      const data = parseEventData<{ phase: string }>(raw);
      console.log(`[stub] stage: ${data.phase}`);
    });

    // ── 6. Bind stdout events ─────────────────────────────────────────────
    privateCh.bind(
      events.stdout,
      async (raw: unknown) => {
        const data = parseEventData<{ seq: number; chunk: string; truncated: boolean }>(raw);
        process.stdout.write(`[stub] stdout: ${data.chunk}`);
        if (!data.chunk.endsWith("\n")) process.stdout.write("\n");

        // Detect the input() prompt — send stdin once we see it
        if (!stdinSent && data.chunk.includes("name?")) {
          stdinSent = true;
          console.log("[stub] detected prompt, sending stdin: World");
          try {
            await apiPost(`/v1/jobs/${jobId}/stdin`, { chunk: "World\n" });
            console.log("[stub] stdin sent");
          } catch (err) {
            clearTimeout(timer);
            pusher.disconnect();
            reject(err);
            return;
          }

          // ── 7. POST /stdin/close (EOF) ──────────────────────────────────
          if (!closeSent) {
            closeSent = true;
            try {
              await apiPost(`/v1/jobs/${jobId}/stdin/close`);
              console.log("[stub] stdin/close sent");
            } catch (err) {
              // Non-fatal if the session already closed
              console.warn("[stub] stdin/close warning:", err);
            }
          }
        }

        // Check for the expected output
        if (data.chunk.includes("hello World") || data.chunk.includes("hello world")) {
          sawHelloWorld = true;
          console.log("[stub] FOUND: hello World in stdout");
        }
      },
    );

    // ── Bind stderr events ──────────────────────────────────────────────────
    privateCh.bind(
      events.stderr,
      (raw: unknown) => {
        const data = parseEventData<{ seq: number; chunk: string; truncated: boolean }>(raw);
        process.stderr.write(`[stub] stderr: ${data.chunk}`);
        if (!data.chunk.endsWith("\n")) process.stderr.write("\n");
      },
    );

    // ── 8. Bind result event ─────────────────────────────────────────────────
    // The ResultEvent wire format: {exitCode, idleTimedOut, timedOut, signal, truncated, durationMs}
    privateCh.bind(
      events.result,
      (raw: unknown) => {
        const data = parseEventData<{
          exitCode: number | null;
          idleTimedOut: boolean;
          timedOut: boolean;
          signal: string | null;
          truncated: boolean;
          durationMs: number;
        }>(raw);
        clearTimeout(timer);
        pusher.disconnect();

        const reason = data.timedOut
          ? "wall-timeout"
          : data.idleTimedOut
            ? "idle-timeout"
            : data.signal
              ? `signal:${data.signal}`
              : "exit";

        console.log(
          `[stub] result: exitCode=${data.exitCode} reason=${reason} durationMs=${data.durationMs}`,
        );

        if (data.exitCode !== 0 || data.idleTimedOut || data.timedOut) {
          reject(
            new Error(
              `E2E FAIL: exitCode=${data.exitCode} reason=${reason}`,
            ),
          );
          return;
        }

        if (!sawHelloWorld) {
          reject(
            new Error(
              'E2E FAIL: did not see "hello World" in streamed stdout',
            ),
          );
          return;
        }

        console.log(
          "[stub] E2E PASS: hello World received + exitCode 0 + clean result",
        );
        resolve();
      },
    );

    // Connection errors
    pusher.connection.bind("error", (err: unknown) => {
      clearTimeout(timer);
      reject(new Error(`Pusher connection error: ${String(err)}`));
    });
  });
}

// ── Entry ────────────────────────────────────────────────────────────────────

run()
  .then(() => {
    console.log("[stub] exit 0 (PASS)");
    process.exit(0);
  })
  .catch((err) => {
    console.error("[stub] exit 1 (FAIL):", err.message ?? err);
    process.exit(1);
  });
