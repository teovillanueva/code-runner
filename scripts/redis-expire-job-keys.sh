#!/usr/bin/env sh
# Give a TTL to legacy job:* keys written before JOB_TTL existed. They never
# expired, which is how Redis filled up on 2026-09-05 (~339k keys) and, under
# --maxmemory-policy noeviction, started rejecting every write.
#
# Streams SCAN output straight into `redis-cli --pipe`: nothing is buffered on
# the client and nothing is deleted. EXPIRE is O(1) per key and allocates no
# memory, so this is safe to run on a Redis that is already near maxmemory.
# Keys that already carry a TTL (job:*:output) just get it reset to TTL_SECONDS.
#
# Usage:  scripts/redis-expire-job-keys.sh [TTL_SECONDS] [REDIS_URL]
#   On Fly, from inside the Redis Machine (`fly ssh console -a code-runner-redis`),
#   paste this script or run the one-liner it wraps:
#     redis-cli --scan --pattern 'job:*' | awk '{print "EXPIRE",$0,3600}' | redis-cli --pipe
set -eu
TTL="${1:-3600}"
URL="${2:-${REDIS_URL:-redis://127.0.0.1:6379}}"
before=$(redis-cli -u "$URL" DBSIZE)
redis-cli -u "$URL" --scan --pattern 'job:*' \
  | awk -v ttl="$TTL" '{ printf "EXPIRE %s %s\n", $0, ttl }' \
  | redis-cli -u "$URL" --pipe
echo "dbsize before: $before, now: $(redis-cli -u "$URL" DBSIZE); the job:* keys expire within ${TTL}s"
