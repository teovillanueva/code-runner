// Observable-gauge test (OBS-06 / D-05). Asserts the worker's queue-depth and
// slots-used/max observable gauges via an in-memory ManualReader:
//
//   - slots.used / slots.max come from the in-memory semaphore (no Redis) and are
//     always observed,
//   - queue.depth observes LLEN jobs:queue when Redis is reachable, and SKIPS the
//     observation (no stale/forced zero) when the QueueDepth read errors
//     (Pitfall 5),
//   - no gauge carries a job_id attribute.
package worker_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/internal/worker"
)

// gaugeValue returns the observed value of the named Int64 observable gauge, a
// bool reporting whether ANY data point was observed, and asserts no job_id.
func gaugeValue(t *testing.T, rm metricdata.ResourceMetrics, name string) (int64, bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("expected Gauge[int64] for %s, got %T", name, m.Data)
			}
			for _, dp := range g.DataPoints {
				if _, bad := dp.Attributes.Value("job_id"); bad {
					t.Errorf("%s must NOT carry a job_id attribute", name)
				}
			}
			if len(g.DataPoints) == 0 {
				return 0, false
			}
			return g.DataPoints[len(g.DataPoints)-1].Value, true
		}
	}
	return 0, false
}

// TestSlotsGauge_FromSemaphore asserts slots.used/max are observed from the
// in-memory semaphore even with no store (queue.depth then simply skips).
func TestSlotsGauge_FromSemaphore(t *testing.T) {
	collect := installManualReader(t)

	pub, _ := newFakePublisher(t)
	inMem := newInMemoryControlTransport()
	cfg := worker.Config{MaxSandboxes: 5, WarmupMs: 500, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(nil, inMem, &scriptedRunner{}, pub, cfg)

	dereg, err := w.RegisterMetrics()
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	defer func() { _ = dereg() }()

	rm := collect()

	used, okUsed := gaugeValue(t, rm, "code_runner.slots.used")
	if !okUsed {
		t.Fatal("code_runner.slots.used was not observed")
	}
	if used != 0 {
		t.Errorf("idle worker: slots.used = %d; want 0", used)
	}

	max, okMax := gaugeValue(t, rm, "code_runner.slots.max")
	if !okMax {
		t.Fatal("code_runner.slots.max was not observed")
	}
	if max != 5 {
		t.Errorf("slots.max = %d; want 5 (MaxSandboxes)", max)
	}
}

// TestQueueDepthGauge_SkipsOnRedisError asserts that when the store's Redis is
// unreachable, the queue-depth callback SKIPS the observation (no data point /
// no stale zero) while slots.used/max are still observed (Pitfall 5).
func TestQueueDepthGauge_SkipsOnRedisError(t *testing.T) {
	collect := installManualReader(t)

	// A client pointed at a closed port: every QueueDepth (LLEN) errors fast.
	deadClient := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1", // nothing listens here
		DialTimeout: 100 * time.Millisecond,
		ReadTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
	})
	defer func() { _ = deadClient.Close() }()
	store := jobstore.New(deadClient)

	pub, _ := newFakePublisher(t)
	inMem := newInMemoryControlTransport()
	cfg := worker.Config{MaxSandboxes: 3, WarmupMs: 500, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(store, inMem, &scriptedRunner{}, pub, cfg)

	dereg, err := w.RegisterMetrics()
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	defer func() { _ = dereg() }()

	rm := collect() // must not panic / must not block beyond the short timeout

	// Slots still observed despite the Redis outage.
	if _, ok := gaugeValue(t, rm, "code_runner.slots.used"); !ok {
		t.Error("slots.used must still be observed when Redis is down")
	}
	// queue.depth observation skipped → no data point.
	if _, ok := gaugeValue(t, rm, "code_runner.queue.depth"); ok {
		t.Error("queue.depth must SKIP its observation when QueueDepth errors (no stale zero)")
	}
}

// TestQueueDepthGauge_ObservesLLEN asserts the callback observes the real LLEN
// value when Redis is reachable. Gated on a local Redis (skips if unreachable).
func TestQueueDepthGauge_ObservesLLEN(t *testing.T) {
	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://127.0.0.1:6379"
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Skipf("queue-depth gauge: cannot parse %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	defer func() { _ = cli.Close() }()

	pingCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := cli.Ping(pingCtx).Err(); err != nil {
		t.Skipf("queue-depth gauge: Redis unreachable at %q: %v", rawURL, err)
	}

	// Seed a known queue depth on a clean queue key.
	ctx := context.Background()
	_ = cli.Del(ctx, keys.JobQueue).Err()
	const want = 3
	for i := 0; i < want; i++ {
		if err := cli.LPush(ctx, keys.JobQueue, "job").Err(); err != nil {
			t.Fatalf("seed LPUSH: %v", err)
		}
	}
	t.Cleanup(func() { _ = cli.Del(context.Background(), keys.JobQueue).Err() })

	collect := installManualReader(t)
	store := jobstore.New(cli)

	pub, _ := newFakePublisher(t)
	inMem := newInMemoryControlTransport()
	cfg := worker.Config{MaxSandboxes: 4, WarmupMs: 500, ClaimTimeout: 100 * time.Millisecond}
	w := worker.NewWithTransport(store, inMem, &scriptedRunner{}, pub, cfg)

	dereg, err := w.RegisterMetrics()
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	defer func() { _ = dereg() }()

	rm := collect()
	depth, ok := gaugeValue(t, rm, "code_runner.queue.depth")
	if !ok {
		t.Fatal("code_runner.queue.depth was not observed against a reachable Redis")
	}
	if depth != want {
		t.Errorf("queue.depth = %d; want %d (seeded LLEN)", depth, want)
	}
}
