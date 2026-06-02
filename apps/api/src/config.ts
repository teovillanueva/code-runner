// Config is read ONLY from environment variables. No endpoint ever returns
// a secret (CFG-01/CFG-02). The soketi secret is never written to Redis (CFG-03).

export interface Config {
  executorApiToken: string;
  redisUrl: string;
  apiPort: number;
  languagesDir: string;
  // soketi / pusher (read by the optional channel-auth helper only)
  soketiHost: string;
  soketiPort: number;
  soketiUseTls: boolean;
  soketiAppId: string;
  soketiAppKey: string;
  soketiAppSecret: string;
  // optional channel-auth feature flag
  enableChannelAuth: boolean;
}

function requireEnv(name: string): string {
  const val = process.env[name];
  if (!val) {
    throw new Error(
      `Missing required environment variable: ${name}. Check your .env file.`,
    );
  }
  return val;
}

function loadConfig(): Config {
  const executorApiToken = requireEnv("EXECUTOR_API_TOKEN");
  const redisUrl = requireEnv("REDIS_URL");

  return {
    executorApiToken,
    redisUrl,
    apiPort: parseInt(process.env["API_PORT"] ?? "8080", 10),
    languagesDir:
      process.env["LANGUAGES_DIR"] ??
      new URL("../../../languages", import.meta.url).pathname,
    soketiHost: process.env["SOKETI_HOST"] ?? "localhost",
    soketiPort: parseInt(process.env["SOKETI_PORT"] ?? "6001", 10),
    soketiUseTls: process.env["SOKETI_USE_TLS"] === "true",
    soketiAppId: process.env["SOKETI_APP_ID"] ?? "code-runner",
    soketiAppKey: process.env["SOKETI_APP_KEY"] ?? "code-runner-key",
    soketiAppSecret: process.env["SOKETI_APP_SECRET"] ?? "",
    enableChannelAuth: process.env["ENABLE_CHANNEL_AUTH"] === "true",
  };
}

// Singleton — loaded once at startup. Throws if EXECUTOR_API_TOKEN or REDIS_URL is missing.
export const config: Config = loadConfig();
