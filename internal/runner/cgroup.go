package runner

import (
	"context"
	"encoding/json"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// usageNanosToMs converts a cumulative CPU usage value in nanoseconds (as
// reported by the Docker stats API's CPUStats.CPUUsage.TotalUsage) to
// cumulative CPU milliseconds.
//
// On a cgroup-v2 host the kernel accumulates cpu.stat usage_usec; the Docker
// daemon multiplies that by 1000 before writing TotalUsage, giving nanoseconds.
// This function is intentionally pure so it can be asserted in unit tests
// without any Docker dependency.
func usageNanosToMs(nanos uint64) int {
	return int(nanos / 1_000_000)
}

// extractCPUMs pulls the cumulative CPU milliseconds from a StatsResponse.
// This is a pure helper intended for unit tests (no Docker required).
func extractCPUMs(stats container.StatsResponse) int {
	return usageNanosToMs(stats.CPUStats.CPUUsage.TotalUsage)
}

// CPUUsageFunc is the function signature consumed by the session CPU poller.
// It matches session.CPUUsageFunc exactly so it can be passed to session.Run
// without type conversion:
//
//	func(ctx context.Context) (cpuMs int, err error)
//
// We define it here rather than importing internal/session to avoid an import
// cycle (session already imports internal/runner).
type CPUUsageFunc = func(ctx context.Context) (cpuMs int, err error)

// newContainerCPUReader returns a CPUUsageFunc that reads the cumulative CPU
// usage for the given container via a single-shot Docker stats query.
//
// The function performs a one-shot stats read (not a streaming poll) so it is
// safe to call repeatedly on a ticker. If the Docker client is nil (e.g. in
// unit tests that verify the constructor shape), the function is still returned
// but will panic if called — nil clients are intentional for constructor-shape
// unit tests that never invoke the returned function.
func newContainerCPUReader(cli *client.Client, containerID string) CPUUsageFunc {
	return func(ctx context.Context) (int, error) {
		resp, err := cli.ContainerStatsOneShot(ctx, containerID)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close() //nolint:errcheck

		var stats container.StatsResponse
		if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
			return 0, err
		}
		return extractCPUMs(stats), nil
	}
}
