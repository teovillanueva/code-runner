// Client-safe soketi connection config. These NEXT_PUBLIC_* values are the
// PUBLIC app key + host only — never the APP_SECRET (which signs channel auth
// server-side). Reading them here is safe to ship to the browser.

export interface SoketiPublicConfig {
  appKey: string;
  host: string;
  port: number;
  useTLS: boolean;
}

export function soketiPublicConfig(): SoketiPublicConfig {
  return {
    appKey: process.env["NEXT_PUBLIC_SOKETI_APP_KEY"] ?? "code-runner-key",
    host: process.env["NEXT_PUBLIC_SOKETI_HOST"] ?? "localhost",
    port: Number(process.env["NEXT_PUBLIC_SOKETI_PORT"] ?? "6001"),
    useTLS: process.env["NEXT_PUBLIC_SOKETI_USE_TLS"] === "true",
  };
}
