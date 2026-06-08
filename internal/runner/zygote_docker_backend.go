// Package runner — Docker-backed pool backend for the ZygoteRunner.
//
// dockerPoolBackend launches the warm parent as a long-lived, PRIVILEGED,
// network-attached container running the language's zygote agent, via the same
// moby SDK used by DockerSocketRunner. Unlike a per-job sandbox container the
// pool container is:
//
//   - Privileged (Privileged:true) + host cgroup namespace (CgroupnsMode:"host")
//     so the agent can rebuild per-child isolation (UID/ns/cgroup) inside it.
//     This is only acceptable on Fly where the Firecracker microVM is the host
//     boundary; ZygoteRunner is gated OFF by default. (Design "Fly-only security
//     posture".)
//   - Network-attached (default bridge) so the worker can reach the agent's
//     relay port on the container's Docker-network IP (children still get an
//     empty netns, so user code has no network).
//   - Long-lived: it runs the agent accept loop, serving one hardened child per
//     job over the relay protocol. It is NOT removed between jobs (only on idle
//     reap or dead-parent respawn).
package runner

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"

	"github.com/teovillanueva/code-runner/internal/config"
)

// agentPath is where the zygote agent is baked into every zygote-eligible
// language image (see the language Dockerfile's "Zygote agent" stage).
const agentPath = "/opt/zygote/zygote_agent.py"

// poolReadyTimeout bounds how long launchParent waits for the agent's relay port
// to become reachable after the container starts.
const poolReadyTimeout = 30 * time.Second

// poolReadyProbeInterval is the dial-probe cadence while waiting for the agent.
const poolReadyProbeInterval = 200 * time.Millisecond

// dockerPoolBackend implements poolBackend over the moby Docker SDK.
type dockerPoolBackend struct {
	cli *client.Client
	cfg config.Config
}

// launchParent creates + starts the privileged pool container and waits for its
// agent relay port to become reachable, returning the container ID and the
// container's Docker-network IP.
func (b *dockerPoolBackend) launchParent(ctx context.Context, key poolKey, image string, relayPort int) (string, string, error) {
	memBytes := int64(b.cfg.ZygotePoolMemoryMb) * 1024 * 1024
	if memBytes <= 0 {
		memBytes = 1024 * 1024 * 1024
	}

	// The agent is launched explicitly (the image's default run path is
	// unaffected). Per the agent, argv[1] (a comma list) overrides the preimport
	// set; we leave it to the manifest default baked into the image env, so the
	// command is just `python /opt/zygote/zygote_agent.py`.
	cmd := strslice.StrSlice{"python", agentPath}

	containerCfg := &container.Config{
		Image: image,
		Cmd:   cmd,
		Env: []string{
			fmt.Sprintf("ZYGOTE_RELAY_PORT=%d", relayPort),
			fmt.Sprintf("ZYGOTE_UID_BASE=%d", b.cfg.ZygoteUIDBase),
		},
		Labels: map[string]string{
			"code-runner.zygotePool": key.String(),
			"code-runner.role":       "zygote-pool",
		},
	}

	// Privileged + host cgroup ns so the agent can build per-child cgroup leaves
	// and namespaces (design RULE #2 + Fly-only posture). Memory cap applies to
	// the whole pool container; per-CHILD memory.max is set by the agent from
	// each job's HELLO.
	hostCfg := &container.HostConfig{
		Privileged:   true,
		CgroupnsMode: container.CgroupnsModeHost,
		Resources: container.Resources{
			Memory:     memBytes,
			MemorySwap: memBytes, // no swap
		},
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	resp, err := b.cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	if err != nil {
		return "", "", fmt.Errorf("ContainerCreate pool: %w", err)
	}
	containerID := resp.ID

	if err := b.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		_ = b.cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
		return "", "", fmt.Errorf("ContainerStart pool: %w", err)
	}

	// Inspect to get the container's network IP.
	agentIP, err := b.inspectIP(ctx, containerID)
	if err != nil {
		_ = b.cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
		return "", "", err
	}

	// Health-check: wait for the relay port to accept connections.
	if err := b.waitRelayReady(ctx, agentIP, relayPort); err != nil {
		_ = b.cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
		return "", "", fmt.Errorf("pool %s agent not ready: %w", key, err)
	}

	return containerID, agentIP, nil
}

// inspectIP returns the first non-empty IPv4 address across the container's
// networks. The pool container is attached to the default bridge unless the
// daemon places it elsewhere; we take whichever network exposes an IP.
func (b *dockerPoolBackend) inspectIP(ctx context.Context, containerID string) (string, error) {
	info, err := b.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("ContainerInspect pool: %w", err)
	}
	if info.NetworkSettings != nil {
		if info.NetworkSettings.IPAddress != "" {
			return info.NetworkSettings.IPAddress, nil
		}
		for _, n := range info.NetworkSettings.Networks {
			if n != nil && n.IPAddress != "" {
				return n.IPAddress, nil
			}
		}
	}
	return "", fmt.Errorf("pool container %s has no network IP", containerID[:12])
}

// waitRelayReady polls the agent relay port until it accepts a TCP connection or
// the timeout/context elapses.
func (b *dockerPoolBackend) waitRelayReady(ctx context.Context, ip string, port int) error {
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	deadline := time.Now().Add(poolReadyTimeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, poolReadyProbeInterval)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("relay %s not reachable within %s: %w", addr, poolReadyTimeout, err)
		}
		time.Sleep(poolReadyProbeInterval)
	}
}

// healthy probes the parent's agent with a short dial. A failure means the
// container/agent died → the pool manager respawns it (POOL-04).
func (b *dockerPoolBackend) healthy(_ context.Context, p *poolParent) bool {
	conn, err := net.DialTimeout("tcp", p.addr(), 1*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// removeParent force-removes the pool container. Not-found is tolerated.
func (b *dockerPoolBackend) removeParent(ctx context.Context, p *poolParent) error {
	err := b.cli.ContainerRemove(ctx, p.containerID, container.RemoveOptions{Force: true})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("ContainerRemove pool %s: %w", p.key, err)
	}
	return nil
}
