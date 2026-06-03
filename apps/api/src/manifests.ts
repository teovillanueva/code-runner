// Manifest singleton — loaded once at startup from LANGUAGES_DIR.
//
// Uses the shared loadManifests / resolveManifest / toLanguageInfo helpers
// from @teovilla/code-runner-contract. Zero language names are hardcoded here (API-07).

import {
  type Manifest,
  loadManifests,
  resolveManifest,
  toLanguageInfo,
  type LanguageInfo,
} from "@teovilla/code-runner-contract";
import { config } from "./config.ts";

let _manifests: Manifest[] | null = null;

/** Load manifests once. Subsequent calls return the cached result. */
export async function getManifests(): Promise<Manifest[]> {
  if (!_manifests) {
    _manifests = await loadManifests(config.languagesDir);
  }
  return _manifests;
}

/**
 * Resolve a language/version to a manifest.
 * Throws a clear error if not found (caller maps to 400).
 */
export { resolveManifest, toLanguageInfo };
export type { Manifest, LanguageInfo };

/** For test teardown — reset the manifest cache. */
export function resetManifests(): void {
  _manifests = null;
}
