// Package worker — heartbeat.go provides the ephemeral workerId generator and
// the heartbeat goroutine that keeps the worker's Redis heartbeat key alive.
//
// The heartbeat is part of the statelessness + horizontal-scale substrate
// (SCALE-01, SCALE-02): each worker has a unique ephemeral identity and
// advertises its liveness to the reaper (plan 05-02) via a Redis key with TTL.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"
)

// newWorkerID returns a cryptographically random ephemeral worker identity.
// It hex-encodes 16 random bytes, producing a 32-character lowercase hex
// string.  No external dependency is added — crypto/rand is stdlib.
func newWorkerID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is extremely rare; fall back to a time-seeded value
		// rather than panicking so the worker can still start.
		slog.Warn("worker: crypto/rand failed, using time-based fallback for workerID", "err", err)
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405000000000")))[:32]
	}
	return hex.EncodeToString(b)
}

// startHeartbeat launches a background goroutine that writes the worker's
// heartbeat key to Redis once immediately (so the key exists right at boot)
// and then on every HeartbeatInterval tick.  Each write refreshes the key's TTL
// so the key remains alive as long as the worker is running.
//
// The goroutine stops when ctx is cancelled.  Transient Redis errors are logged
// and tolerated — a missed beat does not crash the worker.
func (w *Worker) startHeartbeat(ctx context.Context) {
	// Write the first beat immediately so the key exists before the loop.
	if err := w.store.Heartbeat(ctx, w.workerID, w.heartbeatTTL); err != nil {
		slog.Warn("worker: initial heartbeat failed", "workerID", w.workerID, "err", err)
	}

	go func() {
		ticker := time.NewTicker(w.heartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.store.Heartbeat(ctx, w.workerID, w.heartbeatTTL); err != nil {
					// Tolerate transient errors — log and continue.
					slog.Warn("worker: heartbeat failed", "workerID", w.workerID, "err", err)
				}
			}
		}
	}()
}
