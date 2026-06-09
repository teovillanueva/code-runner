# Phase 16 — Deferred items (out of scope for plan 16-01)

These were observed during 16-01 execution but are NOT caused by CAS changes and
were left untouched (SCOPE BOUNDARY: only auto-fix issues directly caused by the
current task's changes).

## stdintransport pub/sub round-trip tests fail against a fresh redis:7 container

- Tests: `TestRedisTransport_RoundTrip`, `TestRedisTransport_CloseStopsDelivery`
  in `internal/stdintransport/redis_test.go`.
- Symptom: the Subscribe handler is not invoked within 3s when run against a
  throwaway `redis:7` container on `localhost:6379` (`TEST_REDIS_URL` default).
- Scope: `internal/stdintransport` is entirely UNTOUCHED by the 16-01 commits
  (`git diff --name-only 5ff8a76 HEAD` lists no stdintransport file). The failure
  reproduces independently of CAS work — it is an environment/pubsub-timing issue
  with the local Redis image, not a regression from Phase 16.
- Action: NOT fixed in 16-01. Revisit separately (likely a go-redis v9 RESP3
  push-message init timing vs. the redis image, or a test that needs a longer
  subscribe-settle wait). The rest of `go test ./...` is green.
