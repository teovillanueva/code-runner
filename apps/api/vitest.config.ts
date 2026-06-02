import { defineConfig } from "vitest/config";
import { resolve } from "path";

export default defineConfig({
  test: {
    globals: true,
    environment: "node",
    testTimeout: 30000,
    hookTimeout: 30000,
    pool: "forks",
    poolOptions: {
      forks: {
        singleFork: true,
      },
    },
    // Set env vars BEFORE test files are loaded so config.ts singleton reads correct values
    env: {
      EXECUTOR_API_TOKEN: process.env["EXECUTOR_API_TOKEN"] ?? "test-default-token",
      REDIS_URL: process.env["REDIS_URL"] ?? process.env["TEST_REDIS_URL"] ?? "redis://localhost:6380",
      LANGUAGES_DIR: process.env["LANGUAGES_DIR"] ?? resolve(__dirname, "../../languages"),
      ENABLE_CHANNEL_AUTH: "false",
    },
  },
});
