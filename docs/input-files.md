# Multi-file input (text, binary, subdirectories)

`POST /v1/execute` accepts one or more input files in `files[]`. Every file is
materialized under the sandbox workspace (`/workspace`) before the entrypoint
runs. Files support **text and binary content** and **subdirectory paths**, all
inline in the request body — no external fetch, no bucket, nothing to provision.

## The `FileInput` shape

```jsonc
{
  "name": "data/input.csv",   // relative path under /workspace; "/" makes subdirs
  "content": "a,b\n1,2\n",     // interpreted per `encoding`
  "encoding": "utf8"           // "utf8" (default) | "base64"; optional
}
```

| Field | Required | Notes |
| --- | --- | --- |
| `name` | yes | Relative path written into `/workspace`. May contain `/` for subdirectories (e.g. `data/input.csv`, `assets/img/logo.png`). **Absolute paths (`/etc/passwd`) and `..` traversal are rejected.** |
| `content` | yes | The file body. UTF-8 text by default; base64-encoded bytes when `encoding:"base64"`. |
| `encoding` | no | `"utf8"` (default — back-compatible with every existing text caller) or `"base64"` for arbitrary bytes (xlsx, parquet, images, zip, …). |

### Backward compatibility

A request with **no `encoding`** and **flat filenames** behaves exactly as it
always has: the content is written verbatim as UTF-8 text at `/workspace/<name>`.
`encoding` is optional and defaults to `utf8`, so no existing caller changes.

## Subdirectories

`name` may contain `/`. The worker creates the parent directories and writes the
file at the relative path under `/workspace`. For example:

```jsonc
"files": [
  { "name": "main.py", "content": "import pandas as pd; print(pd.read_csv('data/input.csv').shape)" },
  { "name": "data/input.csv", "content": "a,b\n1,2\n3,4\n" }
]
```

materializes `/workspace/main.py` and `/workspace/data/input.csv`, and the
program reads the CSV with the relative path it expects.

## Binary files (base64)

Set `encoding:"base64"` and send the bytes base64-encoded. The worker decodes
them and writes raw bytes:

```jsonc
"files": [
  { "name": "report.py", "content": "import openpyxl; wb = openpyxl.load_workbook('fixtures/sheet.xlsx'); print(wb.active.max_row)" },
  { "name": "fixtures/sheet.xlsx", "content": "UEsDBBQABgAIAAAAIQ…", "encoding": "base64" }
]
```

### Node SDK: pass a `Buffer`, skip the base64

`@teovilla/code-runner-sdk-node` encodes binary inputs for you. Pass `data` as a
`Buffer`/`Uint8Array` and the SDK sets `encoding:"base64"` transparently; text
files stay as `{ name, content }`:

```ts
import { readFileSync } from "node:fs";
import { CodeRunnerClient } from "@teovilla/code-runner-sdk-node";

const client = new CodeRunnerClient({ baseUrl, token });

await client.executeFiles({
  language: "python",
  files: [
    { name: "report.py", content: "…" },                 // text → utf8
    { name: "fixtures/sheet.xlsx", data: readFileSync("./sheet.xlsx") }, // binary → base64
  ],
});
```

`executeFiles` accepts a mix of raw wire `FileInput`s, text files, and binary
files. The lower-level `client.execute(req)` still takes raw `FileInput`s
unchanged if you prefer to encode yourself (or use `toFileInputs`).

## Size cap (`MAX_FILES_BYTES`)

The API sums the **decoded** size of all input files in a request and rejects it
with **413** when the total exceeds `MAX_FILES_BYTES` (default **8 MiB**). For a
`utf8` file the decoded size is its UTF-8 byte length; for a `base64` file it is
the decoded byte length (not the larger base64 string). Raise the cap for larger
binary inputs:

```bash
MAX_FILES_BYTES=33554432   # 32 MiB
```

## Validation & safety

The API rejects bad requests up front:

| Result | Cause |
| --- | --- |
| `400` | `content` is not valid base64 (when `encoding:"base64"`). |
| `400` | `name` is absolute (`/…`) or contains a `..` traversal segment. |
| `400` | A file has **both** `content` and `ref`, **neither**, or a malformed `ref`. |
| `413` | Total decoded bytes exceed `MAX_FILES_BYTES`. |

The **worker re-sanitizes every path regardless of the API** (host-escape-only
threat model — the worker never trusts the path). Path cleaning is anchored at
the workspace root, so a name can never escape `/workspace`. Subdirectories are
preserved; absolute paths and `..` traversal are refused.

## Captured artifacts vs. input files

When `collectOutput:true`, the worker captures files the program **wrote** to the
workspace as pullable artifacts (`GET /v1/jobs/:id/output`). Input files are
excluded from capture by their full relative path, so an input at
`data/input.csv` is never echoed back as an artifact — only genuinely new
outputs (e.g. `out/plot.png`) are.

## Large & shared files → content-addressed blobs

Inline files are best for small inputs. For **large** files or files reused
across **many** runs, the content-addressed blob store (CAS) uploads each file
**once** (by `sha256`) and lets jobs reference it by hash — skipping re-upload and
sidestepping `MAX_FILES_BYTES`. The Node SDK routes oversized binary buffers there
transparently. See **[blobs.md](./blobs.md)**.
