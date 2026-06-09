// CodeRunnerClient — a typed HTTP client over the code-runner Hono gateway.
//
// Server-side ONLY: it carries the EXECUTOR_API_TOKEN as a bearer credential
// and must never run in a browser. All routes (except /health) require the
// bearer.

import { createHash } from "node:crypto";
import type {
  BlobCheckRequest,
  BlobCheckResponse,
  BlobFinalizeRequest,
  BlobFinalizeResponse,
  ExecuteRequest,
  ExecuteResponse,
  FileInput,
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
import {
  toFileInputs,
  type SdkFileInput,
  type BinaryFileInput,
} from "./files.ts";

/** Default per-file inline threshold: binary buffers larger than this are routed
 *  to the content-addressed blob store instead of inline base64. 256 KiB. */
export const DEFAULT_INLINE_THRESHOLD_BYTES = 256 * 1024;

/** Options for {@link CodeRunnerClient.blobs}.upload. */
export interface BlobUploadOptions {
  /** Idle-TTL hint (seconds) — informational; the server owns the actual TTL. */
  ttlSeconds?: number;
}

/** The blobs sub-namespace surface (content-addressed uploads). */
export interface BlobsApi {
  /**
   * Upload a buffer to the content-addressed blob store and return its ref.
   * Computes sha256 locally, asks the API which bytes are missing, PUTs only the
   * missing bytes to the presigned URL (client→store direct), finalizes, and
   * returns `{ ref: "sha256:<hex>" }`. An already-present blob skips the PUT.
   */
  upload(
    buffer: Buffer | Uint8Array,
    opts?: BlobUploadOptions,
  ): Promise<{ ref: string }>;
  /** Low-level passthrough: POST /v1/blobs/check. */
  check(hashes: readonly string[]): Promise<BlobCheckResponse>;
}

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
  /**
   * Per-file size threshold (bytes) above which a binary file is transparently
   * routed to the content-addressed blob store instead of inline base64 in
   * {@link CodeRunnerClient.executeFiles}. Defaults to
   * {@link DEFAULT_INLINE_THRESHOLD_BYTES} (256 KiB). Transparent CAS routing
   * requires the blob store to be configured server-side.
   */
  inlineThresholdBytes?: number;
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
  private readonly inlineThresholdBytes: number;

  /** Content-addressed blob store sub-namespace (upload + low-level check). */
  public readonly blobs: BlobsApi;

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
    this.inlineThresholdBytes =
      options.inlineThresholdBytes ?? DEFAULT_INLINE_THRESHOLD_BYTES;
    this.blobs = {
      upload: (buffer, opts) => this.uploadBlob(buffer, opts),
      check: (hashes) => this.blobsCheck(hashes),
    };
  }

  /** GET /v1/languages — list available languages. */
  listLanguages(): Promise<LanguageInfo[]> {
    return this.request<LanguageInfo[]>("GET", "/v1/languages");
  }

  /** POST /v1/execute — enqueue a job. 429 -> CapacityError, 400 -> ValidationError. */
  execute(req: ExecuteRequest): Promise<ExecuteResponse> {
    return this.request<ExecuteResponse>("POST", "/v1/execute", req);
  }

  /**
   * POST /v1/execute with ergonomic file inputs (FILES-08). `files` may mix raw
   * wire FileInputs, text files ({name, content}), and binary files
   * ({name, data: Buffer | Uint8Array}). Binary files are base64-encoded and
   * tagged encoding:"base64" transparently. Identical to {@link execute} once
   * the files are normalized.
   */
  async executeFiles(
    req: Omit<ExecuteRequest, "files"> & { files: readonly SdkFileInput[] },
  ): Promise<ExecuteResponse> {
    const files = await this.normalizeFilesWithCas(req.files);
    const normalized: ExecuteRequest = {
      ...req,
      files: files as ExecuteRequest["files"],
    };
    return this.execute(normalized);
  }

  // ── Content-addressed blobs (Phase 16, BLOB-10/11) ─────────────────────────

  /** POST /v1/blobs/check — low-level existence/presign passthrough. */
  private blobsCheck(hashes: readonly string[]): Promise<BlobCheckResponse> {
    const body: BlobCheckRequest = { hashes: [...hashes] };
    return this.request<BlobCheckResponse>("POST", "/v1/blobs/check", body);
  }

  /** POST /v1/blobs/finalize — record liveness for just-uploaded blobs. */
  private blobsFinalize(
    hashes: readonly string[],
  ): Promise<BlobFinalizeResponse> {
    const body: BlobFinalizeRequest = { hashes: [...hashes] };
    return this.request<BlobFinalizeResponse>(
      "POST",
      "/v1/blobs/finalize",
      body,
    );
  }

  /**
   * Upload a buffer to the CAS store and return its ref. sha256 -> check -> PUT
   * missing bytes to the presigned URL -> finalize -> { ref }. Skips the PUT
   * when the blob is already present. Bytes go client→store direct (the
   * presigned URL points at the store, not the gateway).
   */
  private async uploadBlob(
    buffer: Buffer | Uint8Array,
    _opts?: BlobUploadOptions,
  ): Promise<{ ref: string }> {
    const buf = Buffer.isBuffer(buffer) ? buffer : Buffer.from(buffer);
    const ref = sha256Ref(buf);
    const checked = await this.blobsCheck([ref]);

    const missing = checked.missing.find((m) => m.hash === ref);
    if (missing) {
      // PUT the raw bytes to the presigned URL (client→store direct).
      const res = await this.fetchImpl(missing.uploadUrl, {
        method: "PUT",
        // RequestInit.body accepts a Uint8Array view; Buffer is one.
        body: new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength),
      } as RequestInit);
      if (!res.ok) {
        throw new CodeRunnerError(
          `Blob upload failed for ${ref}: PUT returned ${res.status}`,
          res.status,
        );
      }
      // Record liveness so the worker can resolve the ref.
      await this.blobsFinalize([ref]);
    }
    // Already present (checked.present includes ref) -> skip PUT/finalize: its
    // liveness TTL was already refreshed by the /check touch.
    return { ref };
  }

  /**
   * Normalize SDK file inputs, transparently routing oversized binary files to
   * CAS. A `{ name, data: Buffer }` file whose size exceeds inlineThresholdBytes
   * is uploaded and replaced with `{ name, ref }`; smaller binaries and text
   * files take the Phase-15 inline path. Raw `{ name, ref }` and raw FileInputs
   * pass through unchanged.
   */
  private async normalizeFilesWithCas(
    files: readonly SdkFileInput[],
  ): Promise<FileInput[]> {
    const out: FileInput[] = [];
    for (const f of files) {
      if (isLargeBinary(f, this.inlineThresholdBytes)) {
        const { ref } = await this.uploadBlob((f as BinaryFileInput).data);
        out.push({ name: f.name, ref });
      } else {
        out.push(toFileInputs([f])[0]!);
      }
    }
    return out;
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

/** Compute the content-addressed ref ("sha256:<64hex>") of a buffer. */
function sha256Ref(buf: Buffer): string {
  return "sha256:" + createHash("sha256").update(buf).digest("hex");
}

/**
 * Is this SDK file input a binary buffer larger than `threshold`? Only
 * `{ name, data: Buffer|Uint8Array }` shapes qualify for CAS routing; text files
 * and raw FileInputs (incl. those already carrying `ref`) are left untouched.
 */
function isLargeBinary(f: SdkFileInput, threshold: number): boolean {
  const data = (f as BinaryFileInput).data;
  const isBin =
    "data" in f && (Buffer.isBuffer(data) || data instanceof Uint8Array);
  if (!isBin) return false;
  return data.byteLength > threshold;
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
