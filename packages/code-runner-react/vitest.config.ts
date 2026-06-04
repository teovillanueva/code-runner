import { defineConfig } from "vitest/config";

export default defineConfig({
  // No @vitejs/plugin-react needed: the suite uses the automatic JSX runtime via
  // esbuild, which is enough for renderHook + a fake pusher-js.
  esbuild: {
    jsx: "automatic",
  },
  test: {
    globals: true,
    environment: "jsdom",
    include: ["test/**/*.test.{ts,tsx}"],
  },
});
