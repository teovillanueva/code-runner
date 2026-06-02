//go:build reaper_integration

// Package reaper_test contains integration tests for the dead-worker reaper.
//
// Prerequisites:
//
//	docker run -d -p 6381:6379 redis:7
//	executor/python:3.12 image built (docker build -t executor/python:3.12 languages/python-3.12/)
//
// Run:
//
//	go test -tags=reaper_integration -timeout 180s ./internal/reaper/... -run Reaper -v
//
// or via the Makefile target:
//
//	make reaper-test
package reaper_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/jobstore"
	"github.com/teovillanueva/code-runner/internal/keys"
	"github.com/teovillanueva/code-runner/internal/reaper"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test infrastructure helpers
// ─────────────────────────────────────────────────────────────────────────────

// dialTestRedis returns a live *redis.Client or skips the test.
func dialTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("TEST_REDIS_URL")
	if rawURL == "" {
		rawURL = "redis://localhost:6381"
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Skipf("reaper_integration: cannot parse TEST_REDIS_URL %q: %v", rawURL, err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("reaper_integration: Redis unreachable at %q: %v — run: docker run -d -p 6381:6379 redis:7", rawURL, err)
	}
	return cli
}

// requireDockerAndImage creates a Docker client, pings the daemon, and verifies
// the executor/python:3.12 image is present.  Skips the test if any gate fails.
func requireDockerAndImage(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("reaper_integration: cannot create Docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close() //nolint:errcheck
		t.Skipf("reaper_integration: Docker daemon unreachable: %v", err)
	}
	// Verify the Python image is present (ImageInspect returns an error if absent).
	_, inspectErr := cli.ImageInspect(context.Background(), "executor/python:3.12")
	if inspectErr != nil {
		cli.Close() //nolint:errcheck
		t.Skip("reaper_integration: executor/python:3.12 image not found — build it: cd languages/python-3.12 && docker build -t executor/python:3.12 .")
	}
	return cli
}

// createTestContainer creates a labelled sandbox container with an anonymous
// /workspace volume, mirroring the shape that runner/docker.go produces.
// The container runs `sleep 60` to stay alive until reaped or cleaned up.
// Returns the container ID and the anonymous volume name.
func createTestContainer(t *testing.T, ctx context.Context, dockerCli *client.Client, jobID string) (containerID string, volumeName string) {
	t.Helper()

	resp, err := dockerCli.ContainerCreate(
		ctx,
		&container.Config{
			Image: "executor/python:3.12",
			Cmd:   []string{"sleep", "60"},
			Labels: map[string]string{
				"code-runner.jobId": jobID,
			},
		},
		&container.HostConfig{
			NetworkMode: "none",
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeVolume,
					Target: "/workspace",
					// Empty Source → Docker creates an anonymous volume.
					VolumeOptions: &mount.VolumeOptions{
						Labels: map[string]string{
							"code-runner.jobId": jobID,
						},
					},
				},
			},
		},
		nil,
		nil,
		"",
	)
	require.NoError(t, err, "ContainerCreate for job %s", jobID)
	containerID = resp.ID

	// Start the container so the volume is fully initialized and inspectable.
	err = dockerCli.ContainerStart(ctx, containerID, container.StartOptions{})
	require.NoError(t, err, "ContainerStart for job %s", jobID)

	// Inspect to find the anonymous volume name.
	info, err := dockerCli.ContainerInspect(ctx, containerID)
	require.NoError(t, err, "ContainerInspect for job %s", jobID)
	for _, m := range info.Mounts {
		if m.Destination == "/workspace" && m.Type == mount.TypeVolume {
			volumeName = m.Name
			break
		}
	}
	require.NotEmpty(t, volumeName, "expected anonymous /workspace volume for job %s", jobID)
	t.Logf("created container %s (job %s) with volume %s", containerID[:12], jobID, volumeName)
	return containerID, volumeName
}

// assertContainerGone asserts that no container with the given jobId label exists.
func assertContainerGone(t *testing.T, ctx context.Context, dockerCli *client.Client, jobID string) {
	t.Helper()
	f := filters.NewArgs()
	f.Add("label", fmt.Sprintf("code-runner.jobId=%s", jobID))
	containers, err := dockerCli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		t.Errorf("assertContainerGone: ContainerList: %v", err)
		return
	}
	if len(containers) > 0 {
		ids := make([]string, len(containers))
		for i, c := range containers {
			ids[i] = c.ID[:12]
		}
		t.Errorf("expected container for jobId=%s to be gone, but found: %v", jobID, ids)
	}
}

// assertVolumeGone asserts that the named volume no longer exists.
func assertVolumeGone(t *testing.T, ctx context.Context, dockerCli *client.Client, volumeName string) {
	t.Helper()
	vols, err := dockerCli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		t.Errorf("assertVolumeGone: VolumeList: %v", err)
		return
	}
	for _, v := range vols.Volumes {
		if v.Name == volumeName {
			t.Errorf("expected volume %s to be gone after reaper sweep, but it still exists", volumeName)
			return
		}
	}
}

// assertContainerExists asserts that at least one container with the given
// jobId label still exists (i.e. the reaper did NOT reap it).
func assertContainerExists(t *testing.T, ctx context.Context, dockerCli *client.Client, jobID string) {
	t.Helper()
	f := filters.NewArgs()
	f.Add("label", fmt.Sprintf("code-runner.jobId=%s", jobID))
	containers, err := dockerCli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	require.NoError(t, err, "ContainerList for jobId=%s", jobID)
	if len(containers) == 0 {
		t.Errorf("expected container for jobId=%s to still exist (live worker should protect it), but it was reaped", jobID)
	}
}

// forceRemoveContainer is a test cleanup helper that removes a container
// (+ volumes) if it still exists — used in deferred cleanup so the test
// environment is tidy even if an assertion fails.
func forceRemoveContainer(ctx context.Context, dockerCli *client.Client, containerID string) {
	_ = dockerCli.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Test A: orphaned container + dead heartbeat → reaped (container+volume gone)
// ─────────────────────────────────────────────────────────────────────────────

func TestReaper_OrphanedContainer_IsReaped(t *testing.T) {
	redisClient := dialTestRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := requireDockerAndImage(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx := context.Background()
	store := jobstore.New(redisClient)

	// Unique IDs for this test run.
	jobID := fmt.Sprintf("reaper-orphan-%d", time.Now().UnixNano())

	// Create the labelled container with an anonymous /workspace volume.
	containerID, volumeName := createTestContainer(t, ctx, dockerCli, jobID)

	// Ensure the container is cleaned up even if the test assertion fails.
	defer forceRemoveContainer(ctx, dockerCli, containerID)

	// Explicitly ensure NO heartbeat key / no owned-jobs membership exists for
	// this job.  (The dead worker never wrote one.)
	// We use a fictional dead worker ID — no heartbeat key for it in Redis.
	// Nothing to do: no keys exist by default.

	t.Logf("Test A: no heartbeat key for any worker owning job %s — expecting reap", jobID)

	// Create and run the reaper with a very short interval (not used here; we call
	// Sweep directly for deterministic test control).
	r := reaper.New(dockerCli, store, 24*time.Hour)

	sweepErr := r.Sweep(ctx)
	require.NoError(t, sweepErr, "Sweep should complete without error")

	// Assert container is gone.
	assertContainerGone(t, ctx, dockerCli, jobID)
	t.Logf("container for job %s is gone after sweep", jobID)

	// Assert the anonymous /workspace volume is gone (RemoveVolumes:true).
	assertVolumeGone(t, ctx, dockerCli, volumeName)
	t.Logf("anonymous volume %s is gone after sweep", volumeName)

	// Assert the job status was set to "error".
	status, err := store.ReadStatus(ctx, jobID)
	require.NoError(t, err, "ReadStatus should find the error status written by the reaper")
	assert.Equal(t, "error", string(status.State), "job status should be 'error' after reaping")
	t.Logf("job %s status is %q", jobID, status.State)
}

// ─────────────────────────────────────────────────────────────────────────────
// Test B: live worker owns the container → NOT reaped
// ─────────────────────────────────────────────────────────────────────────────

func TestReaper_LiveWorkerContainer_IsProtected(t *testing.T) {
	redisClient := dialTestRedis(t)
	defer redisClient.Close() //nolint:errcheck
	dockerCli := requireDockerAndImage(t)
	defer dockerCli.Close() //nolint:errcheck

	ctx := context.Background()
	store := jobstore.New(redisClient)

	// Unique IDs for this test run.
	workerID := fmt.Sprintf("test-worker-%d", time.Now().UnixNano())
	jobID := fmt.Sprintf("reaper-live-%d", time.Now().UnixNano())

	// Create the labelled container with an anonymous /workspace volume.
	containerID, _ := createTestContainer(t, ctx, dockerCli, jobID)

	// Always clean up the container at the end of the test (whether or not
	// the reaper ran — it should NOT reap it, so we clean up manually).
	defer forceRemoveContainer(ctx, dockerCli, containerID)

	// Simulate a LIVE worker: set its heartbeat key with a generous TTL and add
	// the jobID to its owned-jobs set.
	err := store.Heartbeat(ctx, workerID, 60*time.Second) // TTL = 60s — well alive
	require.NoError(t, err, "Heartbeat for live worker")
	err = store.AddOwnedJob(ctx, workerID, jobID)
	require.NoError(t, err, "AddOwnedJob for live worker")

	// Clean up the Redis keys at the end so they don't pollute other tests.
	defer func() {
		_ = redisClient.Del(ctx, keys.WorkerHeartbeatKey(workerID))
		_ = redisClient.Del(ctx, keys.WorkerJobsKey(workerID))
	}()

	t.Logf("Test B: worker %s is ALIVE and owns job %s — container should NOT be reaped", workerID, jobID)

	// Verify the heartbeat is live before sweeping.
	alive, err := store.HeartbeatAlive(ctx, workerID)
	require.NoError(t, err)
	require.True(t, alive, "worker heartbeat should be alive before sweep")

	// Run one sweep.
	r := reaper.New(dockerCli, store, 24*time.Hour)
	sweepErr := r.Sweep(ctx)
	require.NoError(t, sweepErr, "Sweep should complete without error")

	// The container must still exist — the live worker's containers are protected.
	assertContainerExists(t, ctx, dockerCli, jobID)
	t.Logf("container for job %s still exists after sweep (live worker protected)", jobID)
}
