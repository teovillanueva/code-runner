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
const SOKETI_HOST = process.env["SOKETI_HOST"] ?? "localhost";
const SOKETI_PORT = parseInt(process.env["SOKETI_PORT"] ?? "6001", 10);
const SOKETI_USE_TLS = process.env["SOKETI_USE_TLS"] === "true";
// The channel auth URL — the API's optional CHAN-02 helper
const CHANNEL_AUTH_URL =
  process.env["CHANNEL_AUTH_URL"] ?? `${API_BASE_URL}/v1/channel-auth`;

// Timeout for the whole E2E (ms)
const E2E_TIMEOUT_MS = parseInt(process.env["E2E_TIMEOUT_MS"] ?? "60000", 10);

// The interactive Python program to run.
// It reads one line from stdin and prints "hello <input>"
const PYTHON_PROGRAM = `name = input("name? ")
print(f"hello {name}")
`;

// ── Auth helper ──────────────────────────────────────────────────────────────

/**
 * Call the API's channel-auth helper (CHAN-02) to authorize a private-run-<id>
 * channel subscription.
 *
 * This is the correct trust boundary: the upstream app (this stub) handles
 * channel auth. The API's CHAN-02 helper just does the HMAC signing for us
 * since we share the app secret in this dev demo.
 */
async function authorizeChannel(
  socketId: string,
  channelName: string,
): Promise<string> {
  const res = await fetch(CHANNEL_AUTH_URL, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${EXECUTOR_API_TOKEN}`,
    },
    body: JSON.stringify({ socket_id: socketId, channel_name: channelName }),
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(
      `Channel auth failed: HTTP ${res.status} from ${CHANNEL_AUTH_URL}: ${body}`,
    );
  }
  const data = (await res.json()) as { auth: string };
  return data.auth;
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
  const pusher = new Pusher(SOKETI_APP_KEY, {
    wsHost: SOKETI_HOST,
    wsPort: SOKETI_PORT,
    wssPort: SOKETI_PORT,
    forceTLS: SOKETI_USE_TLS,
    disableStats: true,
    enabledTransports: ["ws"],
    // Channel auth: call the API's CHAN-02 helper to sign the subscription.
    channelAuthorization: {
      transport: "ajax",
      endpoint: CHANNEL_AUTH_URL,
      headers: {
        Authorization: `Bearer ${EXECUTOR_API_TOKEN}`,
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
      reject(new Error(`Subscription error on ${channel}: ${String(err)}`));
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

    // ── 5. Bind stage events ──────────────────────────────────────────────
    privateCh.bind(events.stage, (data: { stage: string }) => {
      console.log(`[stub] stage: ${data.stage}`);
    });

    // ── 6. Bind stdout events ─────────────────────────────────────────────
    privateCh.bind(
      events.stdout,
      async (data: { seq: number; chunk: string; truncated: boolean }) => {
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
      (data: { seq: number; chunk: string; truncated: boolean }) => {
        process.stderr.write(`[stub] stderr: ${data.chunk}`);
        if (!data.chunk.endsWith("\n")) process.stderr.write("\n");
      },
    );

    // ── 8. Bind result event ─────────────────────────────────────────────────
    privateCh.bind(
      events.result,
      (data: {
        exitCode: number;
        reason: string;
        truncated: boolean;
      }) => {
        clearTimeout(timer);
        pusher.disconnect();

        console.log(
          `[stub] result: exitCode=${data.exitCode} reason=${data.reason}`,
        );

        if (data.exitCode !== 0) {
          reject(
            new Error(
              `E2E FAIL: exitCode=${data.exitCode} reason=${data.reason}`,
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
