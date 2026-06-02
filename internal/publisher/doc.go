// Package publisher is the soketi output publisher for the code-runner worker.
//
// It wraps pusher-http-go/v5 to trigger the four contract events
// (stage, stdout, stderr, result) on the private-run-<jobId> channel.
//
// # Trust Boundary
//
// This package is output-only: the worker triggers events; nothing trusted
// enters through soketi. All soketi credentials (host, port, TLS flag,
// app ID/key/secret) are supplied via [config.Config] — they are read from
// environment variables by the caller and passed in at construction time.
// This package never reads env vars directly and never logs credentials.
//
// # Event Sizing
//
// soketi enforces a ~10 KB per-event payload limit. Stdout and stderr chunks
// that would exceed this limit are split into multiple sequential events, each
// within the budget, so no output is silently dropped. Every output event
// carries a monotonically increasing sequence number per job so the client
// can detect drops or reordering (see PITFALLS pitfall 8).
//
// # Concurrency
//
// A single [Publisher] is safe for concurrent use from multiple goroutines.
// Sequence counters are guarded by a mutex.
package publisher
