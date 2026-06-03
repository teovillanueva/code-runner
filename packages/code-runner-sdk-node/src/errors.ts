// Typed error hierarchy thrown by CodeRunnerClient. Each subclass maps to a
// specific HTTP status returned by the Hono gateway so callers can branch on
// `instanceof` instead of inspecting status codes.

export class CodeRunnerError extends Error {
  readonly status?: number;
  readonly body?: unknown;

  constructor(message: string, status?: number, body?: unknown) {
    super(message);
    this.name = "CodeRunnerError";
    this.status = status;
    this.body = body;
    // Restore prototype chain when compiled to older targets.
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/** 401 — bearer token missing or invalid. */
export class UnauthorizedError extends CodeRunnerError {
  constructor(message = "Unauthorized", body?: unknown) {
    super(message, 401, body);
    this.name = "UnauthorizedError";
  }
}

/** 404 — job (or other resource) not found. */
export class NotFoundError extends CodeRunnerError {
  constructor(message = "Not found", body?: unknown) {
    super(message, 404, body);
    this.name = "NotFoundError";
  }
}

/** 400 — request failed validation. */
export class ValidationError extends CodeRunnerError {
  constructor(message = "Validation failed", body?: unknown) {
    super(message, 400, body);
    this.name = "ValidationError";
  }
}

/** 429 on /v1/execute — no free sandbox slots (queue full). */
export class CapacityError extends CodeRunnerError {
  readonly retryAfterMs?: number;

  constructor(message = "At capacity", retryAfterMs?: number, body?: unknown) {
    super(message, 429, body);
    this.name = "CapacityError";
    this.retryAfterMs = retryAfterMs;
  }
}

/** 429 on /v1/jobs/:id/stdin — stdin rate or pending-byte cap exceeded. */
export class RateLimitError extends CodeRunnerError {
  readonly retryAfterMs?: number;
  readonly capBytes?: number;

  constructor(
    message = "Rate limited",
    opts: { retryAfterMs?: number; capBytes?: number } = {},
    body?: unknown,
  ) {
    super(message, 429, body);
    this.name = "RateLimitError";
    this.retryAfterMs = opts.retryAfterMs;
    this.capBytes = opts.capBytes;
  }
}
