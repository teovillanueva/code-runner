// Shared TypeScript manifest loader for the code-runner service.
//
// Reads language manifests from a directory of the form <dir>/*/manifest.json,
// validates each with the generated ManifestSchema, and provides helpers for
// the Hono API to list and resolve languages — with zero hardcoded identifiers.
//
// The generated schema/types MUST NOT be hand-edited. Edit schema/wire.schema.json
// and run `pnpm contract` to regenerate.

import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import type { Manifest, LanguageInfo } from "../gen/ts/types.ts";
import { ManifestSchema } from "../gen/ts/schemas.ts";

export type { Manifest, LanguageInfo };

/**
 * Reads every <dir>/&#42;/manifest.json, validates with the generated ManifestSchema,
 * and returns the parsed manifests in directory-scan order.
 *
 * @throws if any manifest fails schema validation (error names the offending file).
 */
export async function loadManifests(dir: string): Promise<Manifest[]> {
  let entries: string[];
  try {
    entries = await readdir(dir);
  } catch (err) {
    throw new Error(
      `loadManifests: cannot read directory "${dir}": ${(err as Error).message}`,
    );
  }

  const manifests: Manifest[] = [];

  for (const entry of entries) {
    const manifestPath = join(dir, entry, "manifest.json");
    let raw: string;
    try {
      raw = await readFile(manifestPath, "utf8");
    } catch {
      // Not every top-level entry has a manifest.json — skip silently.
      continue;
    }

    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch (err) {
      throw new Error(
        `loadManifests: JSON parse error in "${manifestPath}": ${(err as Error).message}`,
      );
    }

    const result = ManifestSchema.safeParse(parsed);
    if (!result.success) {
      throw new Error(
        `loadManifests: manifest validation failed for "${manifestPath}": ${result.error.message}`,
      );
    }

    // Cast is safe: ManifestSchema validates the min(1) constraint on `run` at
    // runtime, but zod infers `string[]` rather than the tuple type in types.ts.
    manifests.push(result.data as Manifest);
  }

  return manifests;
}

/**
 * Maps an array of manifests to the public LanguageInfo descriptors returned
 * by GET /v1/languages. No language name is hardcoded here.
 */
export function toLanguageInfo(manifests: Manifest[]): LanguageInfo[] {
  return manifests.map((m) => ({
    language: m.language,
    version: m.version,
    aliases: m.aliases,
    interactive: m.interactive,
  }));
}

/**
 * Returns the manifest matching the given language name-or-alias and optional
 * version. Throws a clear not-found error if no match is found.
 *
 * @param manifests  The array returned by loadManifests.
 * @param language   Language name (e.g. python) or alias (e.g. py3).
 * @param version    Optional explicit version (e.g. "3.12"). If omitted the
 *                   first match by name-or-alias is returned.
 */
export function resolveManifest(
  manifests: Manifest[],
  language: string,
  version?: string,
): Manifest {
  const candidates = manifests.filter(
    (m) => m.language === language || m.aliases.includes(language),
  );

  if (candidates.length === 0) {
    throw new Error(`resolveManifest: language "${language}" not found`);
  }

  if (version !== undefined) {
    const match = candidates.find((m) => m.version === version);
    if (!match) {
      throw new Error(
        `resolveManifest: language "${language}" version "${version}" not found`,
      );
    }
    return match;
  }

  return candidates[0]!;
}
