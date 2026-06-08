// Package runner — zygote warm parent pool manager (POOL-01..04).
//
// One warm parent (= one long-lived, privileged, network-attached pool
// container running the language's zygote agent) per (language, version). The
// pool manager:
//
//   - POOL-01/02: lazily get-or-creates the parent on first job for a key and
//     keeps it warm; routes each job's dial to the right parent's agent IP:port.
//   - POOL-03: an idle reaper tears down a parent after ZygotePoolIdleMs with no
//     jobs, reclaiming RAM.
//   - POOL-04: dead-parent detection — if dial/health fails, the dead parent is
//     removed and respawned on the next request; in-flight jobs that lose their
//     conn surface a clean error (relay reader → EXIT err) and the worker slot
//     is released, no leak.
//
// The Docker control plane is abstracted behind poolBackend so the routing /
// get-or-create / reap / respawn logic can be unit-tested with a fake (no
// Docker). The real backend (dockerPoolBackend) launches the privileged pool
// container via the same moby SDK used by DockerSocketRunner.
package runner

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/teovillanueva/code-runner/internal/config"
)

// poolKey identifies one warm parent: CoW sharing only happens within the same
// image, so a parent is per (language, version) (RULE #3).
type poolKey struct {
	language string
	version  string
}

func (k poolKey) String() string { return k.language + ":" + k.version }

// poolParent is one launched warm parent and its agent endpoint.
type poolParent struct {
	key         poolKey
	containerID string
	agentIP     string
	relayPort   int

	// lastUsed is updated on every successful dial; the reaper uses it.
	mu       sync.Mutex
	lastUsed time.Time
	inflight int // number of live jobs currently routed to this parent
}

func (p *poolParent) addr() string {
	return net.JoinHostPort(p.agentIP, fmt.Sprintf("%d", p.relayPort))
}

// poolBackend abstracts the Docker control plane so the pool manager is
// unit-testable without Docker. The real implementation launches/inspects/
// removes privileged pool containers via the moby SDK; the test fake records
// calls in-memory.
type poolBackend interface {
	// launchParent starts a long-lived, privileged, network-attached pool
	// container for key running the language agent, waits until its relay port is
	// reachable, and returns its container ID + agent IP. image is the language
	// image (e.g. executor/python:3.12). relayPort is the port the agent listens
	// on inside the container.
	launchParent(ctx context.Context, key poolKey, image string, relayPort int) (containerID, agentIP string, err error)

	// healthy reports whether the parent's agent is still reachable (a cheap dial
	// probe). A false result triggers respawn (POOL-04).
	healthy(ctx context.Context, p *poolParent) bool

	// removeParent force-removes the pool container (idle reap or dead-parent
	// teardown). Not-found is not an error.
	removeParent(ctx context.Context, p *poolParent) error
}

// poolManager owns the set of warm parents keyed by (language, version).
type poolManager struct {
	cfg     config.Config
	backend poolBackend
	dialer  func(string) (net.Conn, error)

	mu      sync.Mutex
	parents map[poolKey]*poolParent

	reaperStop chan struct{}
	reaperOnce sync.Once
	closeOnce  sync.Once
}

// newPoolManager constructs the manager and starts the idle reaper.
func newPoolManager(cfg config.Config, backend poolBackend, dialer func(string) (net.Conn, error)) *poolManager {
	pm := &poolManager{
		cfg:        cfg,
		backend:    backend,
		dialer:     dialer,
		parents:    make(map[poolKey]*poolParent),
		reaperStop: make(chan struct{}),
	}
	pm.startReaper()
	return pm
}

// dial routes a job to the warm parent for key (creating it if absent or
// respawning it if dead), dials the agent, and returns the relay connection plus
// a release func to call when the job ends (decrements the parent's in-flight
// count so the reaper can later collect an idle parent). On any error the
// in-flight reservation is rolled back so no slot leaks.
func (pm *poolManager) dial(ctx context.Context, key poolKey, image string) (*relayConn, func(), error) {
	parent, err := pm.getOrCreate(ctx, key, image)
	if err != nil {
		return nil, nil, err
	}

	// Reserve an in-flight slot on this parent.
	parent.mu.Lock()
	parent.inflight++
	parent.lastUsed = time.Now()
	parent.mu.Unlock()

	rollback := func() {
		parent.mu.Lock()
		parent.inflight--
		parent.lastUsed = time.Now()
		parent.mu.Unlock()
	}

	rc, derr := dialRelay(pm.dialer, parent.addr())
	if derr != nil {
		// Dead-parent detection (POOL-04): the dial failed — remove + drop the
		// parent so the next request respawns it. In-flight reservation rolled
		// back so the slot is released cleanly.
		rollback()
		pm.dropParent(ctx, key, parent)
		return nil, nil, fmt.Errorf("zygote pool %s: %w", key, derr)
	}

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(rollback)
	}
	return rc, release, nil
}

// getOrCreate returns the warm parent for key, launching it on first use
// (POOL-01/02) or respawning it if the existing one is unhealthy (POOL-04).
func (pm *poolManager) getOrCreate(ctx context.Context, key poolKey, image string) (*poolParent, error) {
	pm.mu.Lock()
	parent, ok := pm.parents[key]
	pm.mu.Unlock()

	if ok {
		if pm.backend.healthy(ctx, parent) {
			return parent, nil
		}
		// Dead parent: tear it down and fall through to respawn.
		pm.dropParent(ctx, key, parent)
	}

	// Launch a fresh parent. Two concurrent first-jobs could both reach here;
	// the lock below makes launch single-winner per key.
	pm.mu.Lock()
	defer pm.mu.Unlock()
	// Re-check under lock (another goroutine may have launched it).
	if existing, ok := pm.parents[key]; ok {
		return existing, nil
	}

	containerID, agentIP, err := pm.backend.launchParent(ctx, key, image, pm.cfg.ZygoteRelayPort)
	if err != nil {
		return nil, fmt.Errorf("zygote pool %s: launch parent: %w", key, err)
	}
	parent = &poolParent{
		key:         key,
		containerID: containerID,
		agentIP:     agentIP,
		relayPort:   pm.cfg.ZygoteRelayPort,
		lastUsed:    time.Now(),
	}
	pm.parents[key] = parent
	return parent, nil
}

// dropParent removes parent from the map (if it is still the current one for
// key) and force-removes the container. Safe to call concurrently; only the
// owner of the current map entry triggers the backend removal.
func (pm *poolManager) dropParent(ctx context.Context, key poolKey, parent *poolParent) {
	pm.mu.Lock()
	cur, ok := pm.parents[key]
	if ok && cur == parent {
		delete(pm.parents, key)
	}
	pm.mu.Unlock()
	// Best-effort container teardown (idempotent / not-found tolerant).
	_ = pm.backend.removeParent(ctx, parent)
}

// startReaper launches the idle-reaper goroutine (POOL-03).
func (pm *poolManager) startReaper() {
	pm.reaperOnce.Do(func() {
		idle := time.Duration(pm.cfg.ZygotePoolIdleMs) * time.Millisecond
		if idle <= 0 {
			return // reaping disabled
		}
		// Tick at a fraction of the idle window (bounded) so reaping is timely
		// without busy-polling.
		tick := idle / 4
		if tick < time.Second {
			tick = time.Second
		}
		go func() {
			t := time.NewTicker(tick)
			defer t.Stop()
			for {
				select {
				case <-pm.reaperStop:
					return
				case <-t.C:
					pm.reapIdle(idle)
				}
			}
		}()
	})
}

// reapIdle tears down parents that have had no in-flight jobs and have been idle
// longer than idle.
func (pm *poolManager) reapIdle(idle time.Duration) {
	now := time.Now()
	pm.mu.Lock()
	var toReap []*poolParent
	for key, parent := range pm.parents {
		parent.mu.Lock()
		idleEnough := parent.inflight == 0 && now.Sub(parent.lastUsed) >= idle
		parent.mu.Unlock()
		if idleEnough {
			toReap = append(toReap, parent)
			delete(pm.parents, key)
		}
	}
	pm.mu.Unlock()

	for _, parent := range toReap {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = pm.backend.removeParent(ctx, parent)
		cancel()
	}
}

// close stops the reaper and tears down all warm parents. Intended for worker
// shutdown. Idempotent.
func (pm *poolManager) close() error {
	pm.closeOnce.Do(func() {
		close(pm.reaperStop)
	})
	pm.mu.Lock()
	parents := make([]*poolParent, 0, len(pm.parents))
	for key, parent := range pm.parents {
		parents = append(parents, parent)
		delete(pm.parents, key)
	}
	pm.mu.Unlock()

	for _, parent := range parents {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = pm.backend.removeParent(ctx, parent)
		cancel()
	}
	return nil
}
