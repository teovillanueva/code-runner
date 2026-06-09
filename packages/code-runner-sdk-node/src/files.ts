// Ergonomic file-input helpers (FILES-08).
//
// Callers can hand the SDK either:
//   - a raw wire FileInput ({ name, content, encoding? }) — passed through as-is, or
//   - a text file ({ name, content: string }) — sent as utf8, or
//   - a binary file ({ name, data: Buffer | Uint8Array }) — the SDK base64-encodes
//     it and sets encoding:"base64" transparently, so callers never hand-roll
//     base64.
//
// The output is always a wire FileInput the gateway accepts unchanged.

import type { FileInput } from "@teovilla/code-runner-contract";

/** A text file: content is sent verbatim as utf8 (the wire default). */
export interface TextFileInput {
  name: string;
  content: string;
  /** Optional explicit encoding; omit for utf8. */
  encoding?: "utf8";
}

/** A binary file: raw bytes the SDK base64-encodes for the wire. */
export interface BinaryFileInput {
  name: string;
  data: Buffer | Uint8Array;
}

/**
 * Any shape the SDK accepts for a single input file: a raw wire FileInput, a
 * text file, or a binary (Buffer/Uint8Array) file.
 */
export type SdkFileInput = FileInput | TextFileInput | BinaryFileInput;

function isBinary(f: SdkFileInput): f is BinaryFileInput {
  return (
    "data" in f &&
    (Buffer.isBuffer((f as BinaryFileInput).data) ||
      (f as BinaryFileInput).data instanceof Uint8Array)
  );
}

/** Normalize one SDK file input into a wire FileInput. */
export function toFileInput(f: SdkFileInput): FileInput {
  if (isBinary(f)) {
    const buf = Buffer.isBuffer(f.data) ? f.data : Buffer.from(f.data);
    return { name: f.name, content: buf.toString("base64"), encoding: "base64" };
  }
  // Already a wire FileInput or a TextFileInput — both have { name, content }.
  const out: FileInput = { name: f.name, content: (f as FileInput).content };
  const enc = (f as FileInput).encoding;
  if (enc) out.encoding = enc;
  return out;
}

/** Normalize an array of SDK file inputs into wire FileInputs. */
export function toFileInputs(files: readonly SdkFileInput[]): FileInput[] {
  return files.map(toFileInput);
}
