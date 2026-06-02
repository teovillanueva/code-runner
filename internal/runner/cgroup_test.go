package runner

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCgroupUsageNanosToMs checks the pure conversion helper.
// On cgroup-v2 hosts the Docker stats endpoint derives TotalUsage from
// cpu.stat usage_usec (multiplied by 1000 to get nanoseconds).
func TestCgroupUsageNanosToMs(t *testing.T) {
	tests := []struct {
		name     string
		nanos    uint64
		wantMs   int
	}{
		{
			name:   "zero usage",
			nanos:  0,
			wantMs: 0,
		},
		{
			name:   "exactly 1ms worth of CPU",
			nanos:  1_000_000,
			wantMs: 1,
		},
		{
			name:   "500ms",
			nanos:  500_000_000,
			wantMs: 500,
		},
		{
			name:   "1 second = 1000ms",
			nanos:  1_000_000_000,
			wantMs: 1000,
		},
		{
			name:   "2.5 seconds (truncated to int)",
			nanos:  2_500_000_000,
			wantMs: 2500,
		},
		{
			name:   "partial ms rounds down",
			nanos:  1_500_999, // 1.500999ms → 1ms
			wantMs: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := usageNanosToMs(tc.nanos)
			assert.Equal(t, tc.wantMs, got)
		})
	}
}

// TestCgroupStatsToMs verifies extractCPUMs pulls the cumulative usage from a
// populated StatsResponse struct without requiring Docker.
func TestCgroupStatsToMs(t *testing.T) {
	stats := container.StatsResponse{
		CPUStats: container.CPUStats{
			CPUUsage: container.CPUUsage{
				// 250ms of cumulative CPU usage in nanoseconds
				TotalUsage: 250_000_000,
			},
		},
	}

	got := extractCPUMs(stats)
	require.Equal(t, 250, got)
}

// TestCgroupStatsToMs_Zero verifies zero usage maps to 0ms.
func TestCgroupStatsToMs_Zero(t *testing.T) {
	var stats container.StatsResponse
	got := extractCPUMs(stats)
	assert.Equal(t, 0, got)
}

// TestNewContainerCPUReader_Signature verifies that newContainerCPUReader returns
// the func signature consumed by the session CPU poller. This test does NOT
// dial Docker — it just instantiates the constructor and asserts the returned
// function is non-nil (compile-time shape check + nil guard).
func TestNewContainerCPUReader_Signature(t *testing.T) {
	// A nil docker client is intentionally passed; the returned func is NOT
	// invoked (that would require Docker). We only verify the constructor
	// returns a non-nil function with the correct shape.
	fn := newContainerCPUReader(nil, "fake-container-id")
	require.NotNil(t, fn, "newContainerCPUReader must return a non-nil func")
}
