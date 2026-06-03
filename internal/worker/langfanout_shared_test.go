//go:build langfanout

// Package worker_test — shared helpers for all langfanout integration tests.
//
// These declarations mirror the identically-named helpers in integration_test.go
// (guarded by the worker_integration build tag) so that the sqlite, rust, and r
// langfanout files can reference them without pulling in the full worker_integration
// harness.  Keep this file in sync with integration_test.go.
package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// ─────────────────────────────────────────────────────────────────────────────
// Shared event types and triggerer (used by langfanout_sqlite_test.go)
// ─────────────────────────────────────────────────────────────────────────────

type integrationEvent struct {
	channel string
	event   string
	data    json.RawMessage
}

type integrationTriggerer struct {
	mu     sync.Mutex
	events []integrationEvent
	notify chan struct{}
}

func newIntegrationTriggerer() *integrationTriggerer {
	return &integrationTriggerer{notify: make(chan struct{}, 128)}
}

func (it *integrationTriggerer) Trigger(channel, event string, data interface{}) error {
	raw, _ := json.Marshal(data)
	it.mu.Lock()
	it.events = append(it.events, integrationEvent{channel: channel, event: event, data: raw})
	it.mu.Unlock()
	select {
	case it.notify <- struct{}{}:
	default:
	}
	return nil
}

// waitFor blocks until predicate(events) returns true or timeout fires.
func (it *integrationTriggerer) waitFor(timeout time.Duration, pred func([]integrationEvent) bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		it.mu.Lock()
		snapshot := make([]integrationEvent, len(it.events))
		copy(snapshot, it.events)
		it.mu.Unlock()
		if pred(snapshot) {
			return true
		}
		select {
		case <-it.notify:
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

func (it *integrationTriggerer) allEvents() []integrationEvent {
	it.mu.Lock()
	defer it.mu.Unlock()
	out := make([]integrationEvent, len(it.events))
	copy(out, it.events)
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// integrationSeccompProfilePath — shared by sqlite + other langfanout tests
// ─────────────────────────────────────────────────────────────────────────────

func integrationSeccompProfilePath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(filepath.Join(projectRoot, "profiles", "seccomp", "runner.json"))
	if err != nil {
		return ""
	}
	return abs
}

// ─────────────────────────────────────────────────────────────────────────────
// assertNoContainerLeak — shared helper used by sqlite and other langfanout tests
// ─────────────────────────────────────────────────────────────────────────────

func assertNoContainerLeak(t *testing.T, cli *client.Client, jobID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := filters.NewArgs()
	f.Add("label", fmt.Sprintf("code-runner.jobId=%s", jobID))
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		t.Errorf("assertNoContainerLeak: ContainerList: %v", err)
		return
	}
	if len(containers) > 0 {
		ids := make([]string, len(containers))
		for i, c := range containers {
			ids[i] = c.ID[:12]
		}
		t.Errorf("LEAK: %d container(s) with jobId=%s still alive: %v", len(containers), jobID, ids)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// publishStdinRaw — shared by sqlite and other langfanout tests
// ─────────────────────────────────────────────────────────────────────────────

func publishStdinRaw(t *testing.T, ctx context.Context, rc *redis.Client, jobID, chunk string) {
	t.Helper()
	msg, err := json.Marshal(wire.StdinMessage{Chunk: chunk})
	require.NoError(t, err, "marshal StdinMessage")
	require.NoError(t, rc.Publish(ctx, fmt.Sprintf("stdin:%s", jobID), msg).Err(), "Publish stdin")
}
