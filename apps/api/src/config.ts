// Config is read ONLY from environment variables. No endpoint ever returns
// a secret (CFG-01/CFG-02). The soketi secret is never written to Redis (CFG-03).

export interface Config {
  executorApiToken: string;
  redisUrl: string;
  apiPort: number;
  languagesDir: string;
  // soketi / pusher (read by the optional channel-auth helper only)
  soketiHost: string;
  soketiPort: number;
  soketiUseTls: boolean;
  soketiAppId: string;
  soketiAppKey: string;
  soketiAppSecret: string;
  // optional channel-auth feature flag
  enableChannelAuth: boolean;
  // Job-admission backpressure (SCALE-03): max depth of the job queue.
  // POST /v1/execute returns 429 when LLEN(jobs:queue) >= maxQueueDepth.
  // Set via MAX_QUEUE_DEPTH env var. Default: 256.
  maxQueueDepth: number;
  // Multi-file input size cap (FILES-06): max TOTAL decoded bytes across all
  // input files in a single /v1/execute request. POST returns 413 when the sum
  // of decoded file bytes exceeds this. Set via MAX_FILES_BYTES. Default: 8 MiB.
  maxFilesBytes: number;

  // ── Content-addressed blob store presign (Phase 16, BLOB-02/03/04) ─────────
  // The API PRESIGNS PUT URLs for blob uploads — pure LOCAL crypto, NO S3
  // network call, NO byte proxying. Existence is checked against REDIS
  // (EXISTS blob:meta:<hash>), never S3. The worker is the authoritative
  // sha256 verifier on pull.
  //
  // When `blobStoreConfigured` is false (no endpoint/bucket/creds), the
  // /v1/blobs/* routes return 501 — `docker compose up` stays a no-op when
  // MinIO isn't wired, mirroring telemetry-off-by-default.
  blobStoreConfigured: boolean;
  // PUBLIC endpoint the SDK can reach (presign against this so the signed host
  // == the host the client PUTs to). Falls back through the artifact public
  // endpoint and the connect endpoint.
  blobS3Endpoint: string;
  blobS3Bucket: string;
  blobS3AccessKeyId: string;
  blobS3SecretAccessKey: string;
  blobS3Region: string;
  blobS3UseSsl: boolean;
  // Idle liveness TTL recorded on blob:meta:<hash> (seconds). Mirrors the
  // worker default (BLOB_IDLE_TTL, 24h) so the touch/finalize TTL matches.
  blobIdleTtlSeconds: number;
  // Presigned-PUT upload window (seconds). Short — the SDK PUTs immediately.
  blobUploadUrlTtlSeconds: number;
}

function requireEnv(name: string): string {
  const val = process.env[name];
  if (!val) {
    throw new Error(
      `Missing required environment variable: ${name}. Check your .env file.`,
    );
  }
  return val;
}

function loadConfig(): Config {
  const executorApiToken = requireEnv("EXECUTOR_API_TOKEN");
  const redisUrl = requireEnv("REDIS_URL");

  // Blob presign config. We presign against the PUBLIC endpoint so the SDK can
  // reach the store: BLOB_S3_PUBLIC_ENDPOINT, then the artifact public endpoint
  // (ARTIFACT_S3_PUBLIC_ENDPOINT), then the connect endpoint (AWS_ENDPOINT_URL_S3
  // / ARTIFACT_S3_ENDPOINT). Bucket defaults to the artifact bucket (BUCKET_NAME).
  // Credentials/region reuse the AWS_* / ARTIFACT_S3_* names. Presigning is local
  // crypto only — these never trigger an S3 network call.
  const blobS3Endpoint =
    process.env["BLOB_S3_PUBLIC_ENDPOINT"] ??
    process.env["ARTIFACT_S3_PUBLIC_ENDPOINT"] ??
    process.env["AWS_ENDPOINT_URL_S3"] ??
    process.env["ARTIFACT_S3_ENDPOINT"] ??
    "";
  const blobS3Bucket =
    process.env["BLOB_S3_BUCKET"] ??
    process.env["ARTIFACT_S3_BUCKET"] ??
    process.env["BUCKET_NAME"] ??
    "";
  const blobS3AccessKeyId =
    process.env["ARTIFACT_S3_ACCESS_KEY_ID"] ??
    process.env["AWS_ACCESS_KEY_ID"] ??
    "";
  const blobS3SecretAccessKey =
    process.env["ARTIFACT_S3_SECRET_ACCESS_KEY"] ??
    process.env["AWS_SECRET_ACCESS_KEY"] ??
    "";
  const blobS3Region =
    process.env["ARTIFACT_S3_REGION"] ??
    process.env["AWS_REGION"] ??
    "us-east-1";
  const blobS3UseSsl = blobS3Endpoint.toLowerCase().startsWith("https://");
  // Configured iff we have an endpoint, a bucket, and credentials to sign with.
  const blobStoreConfigured =
    blobS3Endpoint !== "" &&
    blobS3Bucket !== "" &&
    blobS3AccessKeyId !== "" &&
    blobS3SecretAccessKey !== "";

  return {
    executorApiToken,
    redisUrl,
    apiPort: parseInt(process.env["API_PORT"] ?? "8080", 10),
    languagesDir:
      process.env["LANGUAGES_DIR"] ??
      new URL("../../../languages", import.meta.url).pathname,
    soketiHost: process.env["SOKETI_HOST"] ?? "localhost",
    soketiPort: parseInt(process.env["SOKETI_PORT"] ?? "6001", 10),
    soketiUseTls: process.env["SOKETI_USE_TLS"] === "true",
    soketiAppId: process.env["SOKETI_APP_ID"] ?? "code-runner",
    soketiAppKey: process.env["SOKETI_APP_KEY"] ?? "code-runner-key",
    soketiAppSecret: process.env["SOKETI_APP_SECRET"] ?? "",
    enableChannelAuth: process.env["ENABLE_CHANNEL_AUTH"] === "true",
    maxQueueDepth: parseInt(process.env["MAX_QUEUE_DEPTH"] ?? "256", 10),
    maxFilesBytes: parseInt(
      process.env["MAX_FILES_BYTES"] ?? String(8 * 1024 * 1024),
      10,
    ),
    blobStoreConfigured,
    blobS3Endpoint,
    blobS3Bucket,
    blobS3AccessKeyId,
    blobS3SecretAccessKey,
    blobS3Region,
    blobS3UseSsl,
    blobIdleTtlSeconds: parseInt(
      process.env["BLOB_IDLE_TTL"] ?? String(24 * 60 * 60),
      10,
    ),
    blobUploadUrlTtlSeconds: parseInt(
      process.env["BLOB_UPLOAD_URL_TTL"] ?? String(15 * 60),
      10,
    ),
  };
}

// Singleton — loaded once at startup. Throws if EXECUTOR_API_TOKEN or REDIS_URL is missing.
export const config: Config = loadConfig();
