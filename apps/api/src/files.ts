// File-input validation for POST /v1/execute (FILES-06/07).
//
// Mirrors the Go worker's runner.SanitizeWorkspacePath so the API rejects bad
// paths up-front with a clear 400, but the worker remains the source of truth
// (it re-sanitizes regardless — host-escape-only threat model, never trust the
// path). Also computes the total DECODED byte size so an over-cap request is
// rejected with 413 before anything is enqueued.

import type { FileInput } from "@teovilla/code-runner-contract";

export type FileValidationError =
  | { kind: "path"; name: string; message: string }
  | { kind: "base64"; name: string; message: string }
  | { kind: "ref"; name: string; message: string };

export interface FileValidationResult {
  totalBytes: number;
  error?: FileValidationError;
}

/**
 * Sanitize a wire file name the same way the worker does: reject empty,
 * absolute, and any ".." traversal segment; preserve legitimate subdirs.
 * Returns the safe relative path or an error message. Wire paths are always
 * forward-slash.
 */
export function sanitizeWorkspacePath(
  name: string,
): { ok: true; rel: string } | { ok: false; message: string } {
  if (name === "") return { ok: false, message: "file name is empty" };
  // Absolute (POSIX or any leading slash).
  if (name.startsWith("/")) {
    return {
      ok: false,
      message: `file name "${name}" is absolute; only relative workspace paths are allowed`,
    };
  }
  const segments = name.split("/");
  for (const seg of segments) {
    if (seg === "..") {
      return {
        ok: false,
        message: `file name "${name}" contains a ".." traversal segment`,
      };
    }
  }
  // Collapse "." and empty segments while preserving order.
  const cleaned = segments.filter((s) => s !== "" && s !== ".").join("/");
  if (cleaned === "") {
    return {
      ok: false,
      message: `file name "${name}" does not resolve to a file inside the workspace`,
    };
  }
  return { ok: true, rel: cleaned };
}

// Strict base64 (no whitespace, valid padding). Node's Buffer.from is lenient,
// so we validate the shape explicitly before trusting a decode.
const BASE64_RE = /^[A-Za-z0-9+/]*={0,2}$/;

// A content-addressed blob ref: "sha256:" + 64 lowercase hex. Mirrors the wire
// schema's FileInput.ref pattern and the worker's validateFileInput.
const REF_RE = /^sha256:[a-f0-9]{64}$/;

/**
 * Validate every input file and sum the DECODED byte size. Returns the first
 * error encountered (path or base64). For utf8 files the decoded size is the
 * UTF-8 byte length of the content; for base64 it is the decoded byte length.
 */
export function validateFiles(files: FileInput[]): FileValidationResult {
  let totalBytes = 0;
  for (const f of files) {
    const path = sanitizeWorkspacePath(f.name);
    if (!path.ok) {
      return { totalBytes, error: { kind: "path", name: f.name, message: path.message } };
    }

    // content/ref XOR (BLOB, Phase 16). The generated FileInputSchema can't
    // express XOR (both fields are optional), so enforce it here AND in the
    // worker (never trust one side). A ref file carries no inline bytes — skip
    // base64/size accounting for it; the worker streams + sha256-verifies it.
    const hasContent = f.content !== undefined;
    const hasRef = f.ref !== undefined;
    if (hasContent && hasRef) {
      return {
        totalBytes,
        error: {
          kind: "ref",
          name: f.name,
          message: `file "${f.name}": exactly one of "content" or "ref" is allowed, not both`,
        },
      };
    }
    if (!hasContent && !hasRef) {
      return {
        totalBytes,
        error: {
          kind: "ref",
          name: f.name,
          message: `file "${f.name}": exactly one of "content" or "ref" is required`,
        },
      };
    }
    if (hasRef) {
      if (!REF_RE.test(f.ref!)) {
        return {
          totalBytes,
          error: {
            kind: "ref",
            name: f.name,
            message: `file "${f.name}": ref must be "sha256:<64 hex>", got "${f.ref}"`,
          },
        };
      }
      // Ref bytes are not counted toward MAX_FILES_BYTES (the inline-input cap):
      // CAS is exactly the path for large/shared files that exceed it.
      continue;
    }

    const encoding = f.encoding ?? "utf8";
    if (encoding === "base64") {
      const content = f.content ?? "";
      if (!BASE64_RE.test(content) || content.length % 4 !== 0) {
        return {
          totalBytes,
          error: {
            kind: "base64",
            name: f.name,
            message: `file "${f.name}": content is not valid base64`,
          },
        };
      }
      totalBytes += Buffer.from(content, "base64").length;
    } else {
      totalBytes += Buffer.byteLength(f.content ?? "", "utf8");
    }
  }
  return { totalBytes };
}
