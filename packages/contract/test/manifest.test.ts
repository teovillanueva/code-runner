// Tests for the shared manifest loader (packages/contract/src/manifest.ts).
// Run via: pnpm --filter @teovilla/code-runner-contract test
// Uses Node.js built-in test runner (node:test) — no external test framework.

import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { writeFile, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import {
  loadManifests,
  toLanguageInfo,
  resolveManifest,
} from "../src/manifest.ts";

const here = dirname(fileURLToPath(import.meta.url));
// The real languages/ directory is three levels up from packages/contract/test/
const repoRoot = join(here, "..", "..", "..");
const languagesDir = join(repoRoot, "languages");

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Creates a temporary directory with a single valid manifest for testing. */
async function makeTmpLanguagesDir(
  name: string,
  manifest: unknown,
): Promise<{ dir: string; cleanup: () => Promise<void> }> {
  const dir = join(tmpdir(), `cr-manifest-test-${Date.now()}`);
  const langDir = join(dir, name);
  await mkdir(langDir, { recursive: true });
  await writeFile(join(langDir, "manifest.json"), JSON.stringify(manifest));
  return {
    dir,
    cleanup: () => rm(dir, { recursive: true, force: true }),
  };
}

const validManifest = {
  language: "testlang",
  version: "1.0",
  aliases: ["tl", "testl"],
  image: "executor/testlang:1.0",
  entrypoint: "main.tl",
  compile: null,
  run: ["testlang", "main.tl"],
  interactive: true,
  defaultLimits: {
    wallTimeMs: 10000,
    idleMs: 5000,
    cpuMs: 5000,
    memoryMb: 64,
    pids: 32,
    outputKb: 256,
  },
};

// ---------------------------------------------------------------------------
// load-valid: real languages/ directory must yield the python manifest
// ---------------------------------------------------------------------------

describe("loadManifests — real languages dir", () => {
  it("loads the python manifest from the repo languages/ directory", async () => {
    const manifests = await loadManifests(languagesDir);
    assert.ok(manifests.length >= 1, "expected at least one manifest");
    const python = manifests.find((m) => m.language === "python");
    assert.ok(python, "expected a manifest with language 'python'");
    assert.strictEqual(python.version, "3.12");
    assert.ok(Array.isArray(python.aliases), "aliases must be an array");
    assert.ok(typeof python.interactive === "boolean");
  });
});

// ---------------------------------------------------------------------------
// load-malformed-throws
// ---------------------------------------------------------------------------

describe("loadManifests — malformed manifest", () => {
  it("throws with the file path when a manifest fails schema validation", async () => {
    const { dir, cleanup } = await makeTmpLanguagesDir("badlang", {
      language: "", // empty — violates ManifestSchema min(1)
      version: "1.0",
      image: "executor/badlang:1.0",
    });
    try {
      await assert.rejects(
        () => loadManifests(dir),
        (err: Error) => {
          assert.ok(
            err.message.includes("badlang"),
            `expected error message to name the offending file; got: ${err.message}`,
          );
          return true;
        },
      );
    } finally {
      await cleanup();
    }
  });
});

// ---------------------------------------------------------------------------
// toLanguageInfo shape
// ---------------------------------------------------------------------------

describe("toLanguageInfo", () => {
  it("maps a manifest array to LanguageInfo descriptors", async () => {
    const { dir, cleanup } = await makeTmpLanguagesDir("testlang", validManifest);
    try {
      const manifests = await loadManifests(dir);
      const infos = toLanguageInfo(manifests);
      assert.strictEqual(infos.length, 1);
      const info = infos[0]!;
      assert.strictEqual(info.language, "testlang");
      assert.strictEqual(info.version, "1.0");
      assert.deepStrictEqual(info.aliases, ["tl", "testl"]);
      assert.strictEqual(info.interactive, true);
      // LanguageInfo must NOT include image, entrypoint, run, compile, defaultLimits
      assert.ok(!("image" in info), "LanguageInfo should not expose image");
    } finally {
      await cleanup();
    }
  });
});

// ---------------------------------------------------------------------------
// resolveManifest — by name, by alias, missing
// ---------------------------------------------------------------------------

describe("resolveManifest — resolve by name", () => {
  it("returns the manifest matching the language name", async () => {
    const { dir, cleanup } = await makeTmpLanguagesDir("testlang", validManifest);
    try {
      const manifests = await loadManifests(dir);
      const resolved = resolveManifest(manifests, "testlang");
      assert.strictEqual(resolved.language, "testlang");
    } finally {
      await cleanup();
    }
  });
});

describe("resolveManifest — resolve by alias", () => {
  it("returns the manifest when looked up by alias", async () => {
    const { dir, cleanup } = await makeTmpLanguagesDir("testlang", validManifest);
    try {
      const manifests = await loadManifests(dir);
      const resolved = resolveManifest(manifests, "tl");
      assert.strictEqual(resolved.language, "testlang");
    } finally {
      await cleanup();
    }
  });
});

describe("resolveManifest — resolve by alias with version", () => {
  it("returns the manifest when looked up by alias and version", async () => {
    const { dir, cleanup } = await makeTmpLanguagesDir("testlang", validManifest);
    try {
      const manifests = await loadManifests(dir);
      const resolved = resolveManifest(manifests, "testl", "1.0");
      assert.strictEqual(resolved.version, "1.0");
    } finally {
      await cleanup();
    }
  });
});

describe("resolveManifest — unknown language throws", () => {
  it("throws a clear not-found error for an unknown language", async () => {
    const { dir, cleanup } = await makeTmpLanguagesDir("testlang", validManifest);
    try {
      const manifests = await loadManifests(dir);
      assert.throws(
        () => resolveManifest(manifests, "nonexistent"),
        (err: Error) => {
          assert.ok(
            err.message.includes("nonexistent"),
            `expected error to mention the language; got: ${err.message}`,
          );
          return true;
        },
      );
    } finally {
      await cleanup();
    }
  });
});
