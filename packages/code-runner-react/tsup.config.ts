import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: true,
  clean: true,
  platform: "browser",
  sourcemap: true,
  // Peer deps stay external (the consumer supplies them).
  // @teovilla/code-runner-contract is a published dependency: only its types are
  // used (erased at runtime) and referenced from the emitted .d.ts.
  external: ["react", "react/jsx-runtime", "pusher-js"],
  esbuildOptions(o) {
    o.jsx = "automatic";
  },
});
