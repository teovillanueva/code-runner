// Presigner for content-addressed blob PUT URLs (Phase 16, BLOB-02).
//
// The API PRESIGNS PUT URLs — pure LOCAL crypto, NO S3 network call, NO byte
// proxying. We use the `minio` JS client to match the Go worker's minio-go and
// the running compose MinIO (same signing algorithm / endpoint semantics).
// Presigning against the PUBLIC endpoint binds the signed host to the address
// the SDK actually PUTs to (SigV4 signs the host).
//
// Object key: blobs/cas/<hash> where <hash> is the full "sha256:<64hex>" ref —
// identical to the Go BlobStore key and the Redis blob:* keys (one token end to
// end).

import { Client as MinioClient } from "minio";
import { config } from "./config.ts";

/** Key prefix every blob object lives under, mirroring the Go BlobStore. */
export const BLOB_KEY_PREFIX = "blobs/cas/";

/** The S3 object key for a blob ref ("sha256:<64hex>" -> "blobs/cas/sha256:<hex>"). */
export function blobObjectKey(hash: string): string {
  return `${BLOB_KEY_PREFIX}${hash}`;
}

/**
 * Parse an endpoint URL ("http://host:9000", "https://s3.example.com", or a bare
 * "host:9000") into the { endPoint, port, useSSL } shape the minio JS client
 * wants — mirroring the Go NewS3Store's scheme-strip + Secure-from-scheme logic.
 * minio.Client wants a bare host (no scheme), an explicit port, and useSSL.
 */
export function parseEndpoint(raw: string): {
  endPoint: string;
  port: number;
  useSSL: boolean;
} {
  const lower = raw.toLowerCase();
  const useSSL = lower.startsWith("https://");
  let stripped = raw
    .replace(/^https:\/\//i, "")
    .replace(/^http:\/\//i, "")
    .replace(/\/+$/, "");
  // Drop any path component — we only sign object keys, never an endpoint path.
  const slash = stripped.indexOf("/");
  if (slash !== -1) stripped = stripped.slice(0, slash);

  let endPoint = stripped;
  let port: number;
  const colon = stripped.lastIndexOf(":");
  if (colon !== -1) {
    const maybePort = stripped.slice(colon + 1);
    if (/^\d+$/.test(maybePort)) {
      endPoint = stripped.slice(0, colon);
      port = parseInt(maybePort, 10);
    } else {
      port = useSSL ? 443 : 80;
    }
  } else {
    port = useSSL ? 443 : 80;
  }
  return { endPoint, port, useSSL };
}

let _client: MinioClient | null = null;

/**
 * Lazily build the presign-only minio client from blob config. The client is
 * NEVER used to connect — minio.presignedPutObject is local crypto. Callers must
 * ensure config.blobStoreConfigured is true before calling presignBlobPut.
 */
function getPresignClient(): MinioClient {
  if (_client) return _client;
  const { endPoint, port, useSSL } = parseEndpoint(config.blobS3Endpoint);
  _client = new MinioClient({
    endPoint,
    port,
    useSSL,
    accessKey: config.blobS3AccessKeyId,
    secretKey: config.blobS3SecretAccessKey,
    region: config.blobS3Region,
  });
  return _client;
}

/**
 * Presign a PUT URL for a blob's bytes. Pure local crypto — no network call.
 * `ttlSeconds` is the upload window the URL stays valid for.
 */
export function presignBlobPut(hash: string, ttlSeconds: number): Promise<string> {
  return getPresignClient().presignedPutObject(
    config.blobS3Bucket,
    blobObjectKey(hash),
    ttlSeconds,
  );
}

/** Test seam: reset the memoized client (so a test can swap config). */
export function resetPresignClient(): void {
  _client = null;
}
