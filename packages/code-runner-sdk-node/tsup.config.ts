import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: true,
  clean: true,
  platform: "node",
  target: "node22",
  sourcemap: true,
  // @teovilla/code-runner-contract is a published dependency: its types are
  // referenced (not inlined) in our .d.ts and resolved by consumers from npm.
});
