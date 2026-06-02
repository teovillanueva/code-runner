// ioredis client — non-blocking commands only (LPUSH, PUBLISH, SET, GET, INCRBY).
// The API never SUBSCRIBEs; that is the worker's responsibility.

import Redis from "ioredis";
import { config } from "./config.ts";

let _redis: Redis | null = null;

export function getRedis(): Redis {
  if (!_redis) {
    _redis = new Redis(config.redisUrl, {
      lazyConnect: false,
      maxRetriesPerRequest: 3,
      enableReadyCheck: true,
    });

    _redis.on("error", (err: Error) => {
      // Log but do not crash — ioredis will auto-reconnect.
      console.error("[redis] connection error:", err.message);
    });
  }
  return _redis;
}

/** For test teardown — disconnect the redis client. */
export async function disconnectRedis(): Promise<void> {
  if (_redis) {
    await _redis.quit();
    _redis = null;
  }
}
