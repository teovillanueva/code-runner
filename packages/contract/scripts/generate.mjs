// Generates TS types + zod validators + Go structs from the single-source-of-truth
// JSON Schema (schema/wire.schema.json). Run via `pnpm contract`.
//
// Outputs (all generated, never hand-edited):
//   gen/ts/types.ts      — TS interfaces (json-schema-to-typescript)
//   gen/ts/schemas.ts     — zod validators (json-schema-to-zod)
//   gen/go/wire/wire.gen.go — Go structs (go-jsonschema)
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { compile } from "json-schema-to-typescript";
import { jsonSchemaToZod } from "json-schema-to-zod";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "..");
const schemaPath = resolve(root, "schema/wire.schema.json");

const banner =
  "// Code generated from schema/wire.schema.json by `pnpm contract`. DO NOT EDIT.\n";

const schema = JSON.parse(await readFile(schemaPath, "utf8"));
const defs = schema.$defs ?? {};
const defNames = Object.keys(defs);

// Recursively inline every internal `#/$defs/X` $ref. The contract has no
// cycles, so a plain recursive expansion is sufficient and lets json-schema-to-zod
// (which otherwise emits z.any() for unresolved refs) produce fully-typed validators.
function deref(node) {
  if (Array.isArray(node)) return node.map(deref);
  if (node && typeof node === "object") {
    if (typeof node.$ref === "string" && node.$ref.startsWith("#/$defs/")) {
      const target = defs[node.$ref.slice("#/$defs/".length)];
      if (!target) throw new Error(`unresolved $ref: ${node.$ref}`);
      return deref(target);
    }
    return Object.fromEntries(Object.entries(node).map(([k, v]) => [k, deref(v)]));
  }
  return node;
}

await mkdir(resolve(root, "gen/ts"), { recursive: true });
await mkdir(resolve(root, "gen/go/wire"), { recursive: true });

// ── TS types ──────────────────────────────────────────────────────────────
// Build a wrapper whose properties reference every $def so the compiler emits
// each named type exactly once.
const wrapper = {
  $schema: schema.$schema,
  $id: schema.$id,
  title: "CodeRunnerWireRoot",
  type: "object",
  additionalProperties: false,
  $defs: defs,
  properties: Object.fromEntries(
    defNames.map((n) => [n, { $ref: `#/$defs/${n}` }]),
  ),
  required: [],
};

let ts = await compile(wrapper, "CodeRunnerWireRoot", {
  bannerComment: "",
  additionalProperties: false,
  declareExternallyReferenced: true,
  enableConstEnums: false,
  unknownAny: false,
  style: { singleQuote: false },
});
// Drop the synthetic root interface (keep only the named domain types).
ts = ts.replace(
  /export interface CodeRunnerWireRoot \{[\s\S]*?\n\}\n?/m,
  "",
);
await writeFile(resolve(root, "gen/ts/types.ts"), banner + "\n" + ts.trimStart());

// ── zod validators ──────────────────────────────────────────────────────────
// One self-contained validator per $def (referenced defs are inlined). Names
// follow <Title>Schema, e.g. ExecuteRequestSchema.
const zodParts = [banner, 'import { z } from "zod";', ""];
for (const name of defNames) {
  const selfContained = {
    $schema: schema.$schema,
    ...deref(defs[name]),
  };
  const code = jsonSchemaToZod(selfContained, { module: "none", name: undefined });
  zodParts.push(`export const ${name}Schema = ${code};`);
  zodParts.push("");
}
await writeFile(resolve(root, "gen/ts/schemas.ts"), zodParts.join("\n"));

// ── Go structs ────────────────────────────────────────────────────────────
const goOut = resolve(root, "gen/go/wire/wire.gen.go");
const gopath = (spawnSync("go", ["env", "GOPATH"], { encoding: "utf8" }).stdout || "").trim();
const candidates = [
  "go-jsonschema",
  gopath ? resolve(gopath, "bin/go-jsonschema") : null,
].filter(Boolean);

let goOk = false;
for (const bin of candidates) {
  const res = spawnSync(
    bin,
    ["-p", "wire", "--only-models", "-t", "--tags", "json", "-o", goOut, schemaPath],
    { encoding: "utf8" },
  );
  if (res.status === 0) {
    goOk = true;
    break;
  }
  if (res.error && res.error.code === "ENOENT") continue;
  // ran but failed → surface the error
  if (res.stderr) process.stderr.write(res.stderr);
}
if (!goOk) {
  console.error(
    "✗ go-jsonschema not found. Install with:\n" +
      "    go install github.com/omissis/go-jsonschema/cmd/go-jsonschema@latest\n" +
      "  (Go structs were NOT regenerated.)",
  );
  process.exitCode = 1;
} else {
  // Prepend the DO-NOT-EDIT banner if the generator didn't.
  const cur = await readFile(goOut, "utf8");
  if (!cur.includes("DO NOT EDIT")) {
    await writeFile(goOut, banner.replace("//", "// ") + cur);
  }
  console.log("✓ generated gen/go/wire/wire.gen.go");
}

console.log("✓ generated gen/ts/types.ts, gen/ts/schemas.ts");
