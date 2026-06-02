//go:build docker

// Package runner_test contains Docker integration test helpers.
// This file is excluded from normal `go test ./...` runs by the build tag above.
// Tests in this file (and docker_integration_test.go) are only compiled and run
// when the `docker` build tag is explicitly passed:
//
//	go test -tags=docker ./internal/runner/...
//
// A secondary runtime guard (requireDocker) skips individual tests when the
// Docker daemon is unreachable, so the tag alone is not sufficient.
package runner_test

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// testImage is the small public image used for all integration tests.
// alpine:3.20 is already available on this machine and is tiny (~7 MB).
const testImage = "alpine:3.20"

// seccompProfilePath returns the absolute path to the seccomp profile that the
// Docker daemon can read. On macOS with Docker Desktop the host filesystem is
// bind-mounted into the VM so absolute paths work directly.
func seccompProfilePath() string {
	// Navigate from this source file's directory up to the project root.
	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(filepath.Join(projectRoot, "profiles", "seccomp", "runner.json"))
	if err != nil {
		// Fallback: return the path relative to a well-known location.
		return "/profiles/seccomp/runner.json"
	}
	return abs
}

// dockerOnce ensures the image pull happens at most once per test binary run.
var dockerOnce sync.Once

// requireDocker creates a Docker client and SKIPs the calling test when the
// daemon is unreachable. It also ensures testImage has been pulled at most once.
// It returns a client that is safe for use in tests.
func requireDocker(t *testing.T) *client.Client {
	t.Helper()

	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Skipf("docker: cannot create client: %v", err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := cli.Ping(ctx); err != nil {
		_ = cli.Close()
		t.Skipf("docker: daemon unreachable (%v) — skipping Docker integration test", err)
		return nil
	}

	// Pull the test image once per test-binary run.
	dockerOnce.Do(func() {
		pullCtx, pullCancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer pullCancel()

		out, pullErr := cli.ImagePull(pullCtx, testImage, image.PullOptions{})
		if pullErr != nil {
			// Log but do not fail — the image may already be present.
			t.Logf("image pull warning (%s): %v", testImage, pullErr)
			return
		}
		_, _ = io.Copy(io.Discard, out)
		_ = out.Close()
	})

	return cli
}

// newTestRunner builds a DockerSocketRunner backed by config.Default() and the
// bundled seccomp profile. Requires the daemon to be reachable (call
// requireDocker first).
func newTestRunner(t *testing.T) *runner.DockerSocketRunner {
	t.Helper()
	cfg := config.Default()
	r, err := runner.NewDockerSocketRunner(cfg, seccompProfilePath())
	if err != nil {
		t.Fatalf("NewDockerSocketRunner: %v", err)
	}
	return r
}

// buildSpec returns a wire.JobSpec for integration tests. image is set to
// testImage; run is the command to execute inside the container; limits is
// applied verbatim. jobID must be unique per test (use t.Name() as a prefix).
func buildSpec(jobID string, run []string, limits wire.Limits) wire.JobSpec {
	return wire.JobSpec{
		JobId:       jobID,
		Language:    "test",
		Version:     "integration",
		Image:       testImage,
		Run:         run,
		Interactive: false,
		Limits:      limits,
		Channel:     fmt.Sprintf("private-run-%s", jobID),
	}
}

// assertNoLeak verifies that no container with label `code-runner.jobId=<jobID>`
// survives after teardown. It calls t.Errorf (not Fatal) so deferred cleanup can
// still run.
func assertNoLeak(t *testing.T, cli *client.Client, jobID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	f := filters.NewArgs()
	f.Add("label", fmt.Sprintf("code-runner.jobId=%s", jobID))

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		t.Errorf("assertNoLeak: ContainerList error: %v", err)
		return
	}

	if len(containers) > 0 {
		ids := make([]string, len(containers))
		for i, c := range containers {
			ids[i] = c.ID[:12]
		}
		t.Errorf("assertNoLeak: %d container(s) with jobId=%s still exist after cleanup: %v",
			len(containers), jobID, ids)
	}
}

// testLimitsDefault returns a standard small-limit set for integration tests.
// Keep values short so the suite runs quickly.
func testLimitsDefault() wire.Limits {
	return wire.Limits{
		WallTimeMs: 10000,
		IdleMs:     5000,
		CpuMs:      5000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}
}
