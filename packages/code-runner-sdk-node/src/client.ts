// CodeRunnerClient — a typed HTTP client over the code-runner Hono gateway.
//
// Server-side ONLY: it carries the EXECUTOR_API_TOKEN as a bearer credential
// and must never run in a browser. All routes (except /health) require the
// bearer.

import type {
  ExecuteRequest,
  ExecuteResponse,
  JobStatus,
  LanguageInfo,
  RunResult,
} from "@teovilla/code-runner-contract";
import {
  CapacityError,
  CodeRunnerError,
  NotFoundError,
  RateLimitError,
  UnauthorizedError,
  ValidationError,
} from "./errors.ts";

export type FetchLike = (
  input: string,
  init?: RequestInit,
) => Promise<Response>;

export interface CodeRunnerClientOptions {
  /** Base URL of the gateway, e.g. "http://localhost:8080". No trailing slash required. */
  baseUrl: string;
  /** Bearer token (EXECUTOR_API_TOKEN). Server-side only. */
  token: string;
  /** Optional fetch implementation; defaults to globalThis.fetch. */
  fetch?: FetchLike;
}

interface RateLimitBody {
  error?: string;
  retryAfterMs?: number;
  capBytes?: number;
}

export class CodeRunnerClient {
  private readonly baseUrl: string;
  private readonly token: string;
  private readonly fetchImpl: FetchLike;

  constructor(options: CodeRunnerClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, "");
    this.token = options.token;
    const f = options.fetch ?? (globalThis.fetch as FetchLike | undefined);
    if (!f) {
      throw new Error(
        "No fetch implementation available; pass `fetch` in CodeRunnerClientOptions",
      );
    }
    this.fetchImpl = f;
  }

  /** GET /v1/languages — list available languages. */
  listLanguages(): Promise<LanguageInfo[]> {
    return this.request<LanguageInfo[]>("GET", "/v1/languages");
  }

  /** POST /v1/execute — enqueue a job. 429 -> CapacityError, 400 -> ValidationError. */
  execute(req: ExecuteRequest): Promise<ExecuteResponse> {
    return this.request<ExecuteResponse>("POST", "/v1/execute", req);
  }

  /** GET /v1/jobs/:id — fetch job status. 404 -> NotFoundError. */
  getJob(id: string): Promise<JobStatus> {
    return this.request<JobStatus>("GET", `/v1/jobs/${encodeURIComponent(id)}`);
  }

  /**
   * GET /v1/jobs/:id/status — fetch the live JobStatus (alias of getJob).
   * Intended for reconciling client state after a late soketi subscription:
   * pull the authoritative state instead of waiting for an event that may have
   * already fired. 404 -> NotFoundError.
   */
  getStatus(id: string): Promise<JobStatus> {
    return this.request<JobStatus>(
      "GET",
      `/v1/jobs/${encodeURIComponent(id)}/status`,
    );
  }

  /** GET /v1/jobs/:id/output — fetch the persisted run output. 404 -> NotFoundError. */
  getOutput(id: string): Promise<RunResult> {
    return this.request<RunResult>(
      "GET",
      `/v1/jobs/${encodeURIComponent(id)}/output`,
    );
  }

  /** POST /v1/jobs/:id/start — start a queued job. */
  async start(id: string): Promise<void> {
    await this.request<unknown>(
      "POST",
      `/v1/jobs/${encodeURIComponent(id)}/start`,
    );
  }

  /** POST /v1/jobs/:id/stdin — write a chunk to the process stdin. 429 -> RateLimitError. */
  async sendStdin(id: string, chunk: string): Promise<void> {
    await this.request<unknown>(
      "POST",
      `/v1/jobs/${encodeURIComponent(id)}/stdin`,
      { chunk },
    );
  }

  /** POST /v1/jobs/:id/stdin/close — close the stdin stream. */
  async closeStdin(id: string): Promise<void> {
    await this.request<unknown>(
      "POST",
      `/v1/jobs/${encodeURIComponent(id)}/stdin/close`,
    );
  }

  /** POST /v1/jobs/:id/kill — kill the sandbox. */
  async kill(id: string): Promise<void> {
    await this.request<unknown>(
      "POST",
      `/v1/jobs/${encodeURIComponent(id)}/kill`,
    );
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
  ): Promise<T> {
    const isStdin = path.endsWith("/stdin");
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.token}`,
    };
    const init: RequestInit = { method, headers };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }

    // Distributed tracing (OBS-02-ext): if the caller has an active OTel
    // context, inject the W3C `traceparent`/`tracestate` headers so the
    // execution shows up as one connected trace spanning caller → API → worker.
    // Injected ONLY on /v1/execute — the single request that enqueues a job, and
    // the one the API/worker extract from. `@opentelemetry/api` is an OPTIONAL
    // peer: if it is not installed (or there is no active span) this is a silent
    // no-op and the request is byte-for-byte unchanged.
    if (path === "/v1/execute") {
      await injectTraceparent(headers);
    }

    const res = await this.fetchImpl(`${this.baseUrl}${path}`, init);

    if (!res.ok) {
      await this.throwForStatus(res, isStdin);
    }

    // 2xx. Some endpoints return {ok:true}; callers ignore the body for those.
    if (res.status === 204) {
      return undefined as T;
    }
    const text = await res.text();
    if (!text) {
      return undefined as T;
    }
    try {
      return JSON.parse(text) as T;
    } catch {
      return undefined as T;
    }
  }

  private async throwForStatus(res: Response, isStdin: boolean): Promise<never> {
    let parsed: unknown;
    let text = "";
    try {
      text = await res.text();
      parsed = text ? JSON.parse(text) : undefined;
    } catch {
      parsed = text || undefined;
    }
    const errMessage =
      (parsed && typeof parsed === "object" && "error" in parsed
        ? String((parsed as { error?: unknown }).error)
        : undefined) ?? `Request failed with status ${res.status}`;

    switch (res.status) {
      case 401:
        throw new UnauthorizedError(errMessage, parsed);
      case 404:
        throw new NotFoundError(errMessage, parsed);
      case 400:
        throw new ValidationError(errMessage, parsed);
      case 429: {
        const b = (parsed as RateLimitBody | undefined) ?? {};
        if (isStdin) {
          throw new RateLimitError(
            errMessage,
            { retryAfterMs: b.retryAfterMs, capBytes: b.capBytes },
            parsed,
          );
        }
        throw new CapacityError(errMessage, b.retryAfterMs, parsed);
      }
      default:
        throw new CodeRunnerError(errMessage, res.status, parsed);
    }
  }
}

/**
 * The slice of the `@opentelemetry/api` surface we use: inject the active trace
 * context into a carrier as W3C `traceparent`/`tracestate` headers.
 */
interface OTelApi {
  propagation: {
    inject(context: unknown, carrier: Record<string, string>): void;
  };
  context: {
    active(): unknown;
  };
}

/**
 * Optionally inject W3C trace-context headers from the caller's active OTel
 * span into `headers`, in place.
 *
 * `@opentelemetry/api` is an OPTIONAL peer dependency: callers that do not use
 * OpenTelemetry never install it, so the dynamic `import()` rejects and we
 * silently no-op — the request is unchanged. When OTel IS present but no span
 * is active, `propagation.inject` writes nothing meaningful and the request
 * still succeeds. Either way this never throws (T-08-14: only the non-secret
 * `traceparent`/`tracestate` headers are added, and only on /v1/execute).
 *
 * A `globalThis.__OTEL_API__` override is honored first as a test seam (and as
 * an escape hatch for bundlers that cannot resolve the optional peer); it lets
 * the suite assert injection without depending on module resolution.
 */
async function injectTraceparent(headers: Record<string, string>): Promise<void> {
  try {
    const override = (globalThis as { __OTEL_API__?: OTelApi }).__OTEL_API__;
    const api: OTelApi = override ?? ((await import("@opentelemetry/api")) as unknown as OTelApi);
    api.propagation.inject(api.context.active(), headers);
  } catch {
    // OTel absent (optional peer not installed) or no active span → unchanged
    // behavior (OBS-02-ext). Never propagate a tracing error to the caller.
  }
}
