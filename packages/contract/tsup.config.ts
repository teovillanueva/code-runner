import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  // The contract is the published source of truth for the SDKs' types, so it
  // ships its own bundled .d.ts. zod stays external (a real runtime dependency);
  // the generated gen/ts/*.ts modules are bundled in.
  dts: true,
  clean: true,
  platform: "node",
  target: "node22",
  sourcemap: true,
});
