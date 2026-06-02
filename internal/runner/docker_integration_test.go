//go:build docker

// Package runner_test: Docker integration tests.
//
// These tests launch REAL containers through DockerSocketRunner and assert:
//   - Hardening flags are actually applied (inspect the running container)
//   - Three clocks each kill a container (wall, idle, CPU)
//   - Output truncation produces Result.Truncated=true
//   - stdin round-trips byte-for-byte
//   - No labeled container survives after teardown
//
// Build tag `docker` + runtime skip (requireDocker) form a two-gate guard:
// `go test ./...` excludes these tests; only `go test -tags=docker ...` runs them,
// and only when the daemon is reachable.
//
// Run via:
//
//	make test-docker
//	go test -tags=docker -timeout 300s ./internal/runner/... -run Integration -v
package runner_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/session"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// ── Task 2: Hardening inspect ─────────────────────────────────────────────────

// TestIntegrationHardeningFlags creates a container and uses ContainerInspect to
// assert that EVERY hardening flag from HARD-01..05 is actually applied:
//   - NetworkMode == "none"           (HARD-01)
//   - ReadonlyRootfs == true          (HARD-02)
//   - Tmpfs "/tmp" with size= option  (HARD-02)
//   - Memory == MemorySwap > 0        (HARD-03, no swap)
//   - PidsLimit set                   (HARD-04)
//   - NanoCPUs > 0                    (HARD-04)
//   - CapDrop contains "ALL"          (HARD-05)
//   - SecurityOpt contains no-new-privileges (HARD-05)
//   - SecurityOpt contains seccomp entry (HARD-05)
//   - Container user is non-root (uid 65534) (T-02-12)
func TestIntegrationHardeningFlags(t *testing.T) {
	cli := requireDocker(t)
	r := newTestRunner(t)

	jobID := fmt.Sprintf("test-hardening-%d", time.Now().UnixNano())
	spec := buildSpec(jobID, []string{"sleep", "30"}, testLimitsDefault())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	require.NoError(t, err, "Create must not error")
	defer func() {
		_ = sb.Cleanup()
		assertNoLeak(t, cli, jobID)
	}()

	// Find the container by label to get its ID for ContainerInspect.
	f := filters.NewArgs()
	f.Add("label", fmt.Sprintf("code-runner.jobId=%s", jobID))

	clist, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	require.NoError(t, err, "ContainerList must not error")
	require.Len(t, clist, 1, "exactly one container should exist with jobId=%s", jobID)

	containerID := clist[0].ID

	info, err := cli.ContainerInspect(ctx, containerID)
	require.NoError(t, err, "ContainerInspect must not error")

	hc := info.HostConfig

	// HARD-01: NetworkMode == none
	assert.Equal(t, "none", string(hc.NetworkMode),
		"HARD-01: NetworkMode must be 'none'")

	// HARD-02: ReadonlyRootfs
	assert.True(t, hc.ReadonlyRootfs,
		"HARD-02: ReadonlyRootfs must be true")

	// HARD-02: Tmpfs /tmp with a size= cap
	require.Contains(t, hc.Tmpfs, "/tmp",
		"HARD-02: Tmpfs must contain '/tmp' entry")
	assert.Contains(t, hc.Tmpfs["/tmp"], "size=",
		"HARD-02: Tmpfs /tmp options must include a size= cap")

	// HARD-03: Memory == MemorySwap (no swap); both > 0
	assert.Greater(t, hc.Memory, int64(0),
		"HARD-03: Memory must be > 0")
	assert.Equal(t, hc.Memory, hc.MemorySwap,
		"HARD-03: Memory must equal MemorySwap (swap disabled)")

	// HARD-04: PidsLimit set
	require.NotNil(t, hc.PidsLimit,
		"HARD-04: PidsLimit must be set (non-nil)")
	assert.Greater(t, *hc.PidsLimit, int64(0),
		"HARD-04: PidsLimit must be > 0")

	// HARD-04: NanoCPUs > 0
	assert.Greater(t, hc.NanoCPUs, int64(0),
		"HARD-04: NanoCPUs must be > 0")

	// HARD-05: CapDrop contains "ALL"
	capDropStr := strings.Join([]string(hc.CapDrop), ",")
	assert.Contains(t, strings.ToUpper(capDropStr), "ALL",
		"HARD-05: CapDrop must contain 'ALL'")

	// HARD-05: SecurityOpt contains "no-new-privileges"
	secOpts := strings.Join(hc.SecurityOpt, " ")
	assert.Contains(t, secOpts, "no-new-privileges",
		"HARD-05: SecurityOpt must contain 'no-new-privileges'")

	// HARD-05: SecurityOpt contains a seccomp entry
	assert.Contains(t, secOpts, "seccomp",
		"HARD-05: SecurityOpt must contain a 'seccomp' entry")

	// T-02-12: Non-root user (65534:65534)
	user := info.Config.User
	assert.Equal(t, "65534:65534", user,
		"T-02-12: Container user must be non-root (65534:65534)")
}

// ── Task 2: stdin round-trip ──────────────────────────────────────────────────

// TestIntegrationStdinRoundtrip writes a known line to Stdin() and reads it
// back from Stdout(), asserting exact bytes. Then closes Stdin to signal EOF,
// which causes `cat` to exit cleanly.
//
// Design: we read Stdout() directly in a goroutine BEFORE calling session.Run,
// so the output is captured before the session pump goroutine consumes it.
// Then we call Kill+Cleanup explicitly (session.Run not used here because it
// would race with our direct stdout read). The key assertions are:
//  1. The echoed line matches testLine byte-for-byte.
//  2. cat exits 0 on EOF (clean exit, no signal kill required).
func TestIntegrationStdinRoundtrip(t *testing.T) {
	cli := requireDocker(t)
	r := newTestRunner(t)

	jobID := fmt.Sprintf("test-stdin-%d", time.Now().UnixNano())
	// `cat` reads from stdin and echoes to stdout; exits on EOF.
	spec := buildSpec(jobID, []string{"cat"}, testLimitsDefault())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	require.NoError(t, err, "Create must not error")
	defer func() {
		_ = sb.Cleanup()
		assertNoLeak(t, cli, jobID)
	}()

	const testLine = "hello integration\n"

	// Start a goroutine to read the echo from stdout BEFORE writing to stdin
	// (avoids deadlock if the pipe fills and the write blocks).
	outCh := make(chan string, 1)
	go func() {
		buf := make([]byte, len(testLine))
		n, readErr := io.ReadFull(sb.Stdout(), buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			outCh <- fmt.Sprintf("READ_ERROR:%v", readErr)
			return
		}
		outCh <- string(buf[:n])
	}()

	// Write the test line to stdin.
	_, err = fmt.Fprint(sb.Stdin(), testLine)
	require.NoError(t, err, "Stdin write must not error")

	// Wait for the echo to arrive.
	select {
	case got := <-outCh:
		require.NotContains(t, got, "READ_ERROR:", "stdout read must not error: %s", got)
		assert.Equal(t, testLine, got,
			"stdin round-trip: stdout must echo stdin exactly")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for stdout echo")
	}

	// Drain remaining stdout (in case cat buffered more) to unblock the pipe,
	// then close stdin to send EOF → cat exits cleanly.
	go io.Copy(io.Discard, sb.Stdout()) //nolint:errcheck

	err = sb.Stdin().Close()
	require.NoError(t, err, "Stdin Close must not error")

	// Wait for cat to exit naturally (it exits on EOF with code 0).
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()

	result, waitErr := sb.Wait(waitCtx)
	require.NoError(t, waitErr, "Wait must not error")

	// Clean exit: no timeout flags, exit code 0.
	assert.False(t, result.TimedOut,
		"stdin close should produce clean exit, not wall/cpu timeout")
	assert.False(t, result.IdleTimedOut,
		"stdin close should produce clean exit, not idle timeout")
	if result.ExitCode != nil {
		assert.Equal(t, 0, *result.ExitCode,
			"cat must exit 0 on clean EOF")
	}
}

// ── Task 2: tree-kill + no-leak ───────────────────────────────────────────────

// TestIntegrationTreeKillNoLeak launches a long-running container (sleep), calls
// Kill, then asserts zero containers with the job's label survive.
func TestIntegrationTreeKillNoLeak(t *testing.T) {
	cli := requireDocker(t)
	r := newTestRunner(t)

	jobID := fmt.Sprintf("test-kill-leak-%d", time.Now().UnixNano())
	spec := buildSpec(jobID, []string{"sleep", "300"}, testLimitsDefault())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	require.NoError(t, err, "Create must not error")
	defer func() {
		_ = sb.Cleanup()
	}()

	// Kill the container; this should remove it immediately.
	err = sb.Kill(ctx)
	require.NoError(t, err, "Kill must not error")

	// assertNoLeak: verify no container with this label survives.
	assertNoLeak(t, cli, jobID)
}

// ── Task 3: Wall clock ────────────────────────────────────────────────────────

// TestIntegrationWallClock runs an infinite sleep with a short WallTimeMs.
// The wall clock must fire and terminate the container; Result.TimedOut must
// be true, and no container with the job label should survive.
func TestIntegrationWallClock(t *testing.T) {
	cli := requireDocker(t)
	r := newTestRunner(t)

	jobID := fmt.Sprintf("test-wall-%d", time.Now().UnixNano())
	limits := wire.Limits{
		WallTimeMs: 2000, // 2 s wall clock — fires quickly
		IdleMs:     30000,
		CpuMs:      30000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}
	spec := buildSpec(jobID, []string{"sleep", "999"}, limits)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	require.NoError(t, err, "Create must not error")
	defer func() {
		_ = sb.Cleanup()
	}()

	start := time.Now()
	result, runErr := session.Run(ctx, sb, limits, func(_ context.Context) (int, error) {
		return 0, nil
	})
	elapsed := time.Since(start)

	require.NoError(t, runErr, "session.Run must not error")
	assert.True(t, result.TimedOut, "wall clock must set TimedOut=true")
	assert.False(t, result.IdleTimedOut, "wall clock must not set IdleTimedOut")
	// Should terminate close to wallTimeMs (allow generous margin).
	assert.Less(t, elapsed, 10*time.Second,
		"wall clock should terminate within 10s of a 2s limit")

	assertNoLeak(t, cli, jobID)
}

// ── Task 3: Idle clock ────────────────────────────────────────────────────────

// TestIntegrationIdleClock runs a program that blocks reading stdin and produces
// no output. The idle clock must fire because there is no stdout/stderr activity.
// Result.IdleTimedOut must be true.
func TestIntegrationIdleClock(t *testing.T) {
	cli := requireDocker(t)
	r := newTestRunner(t)

	jobID := fmt.Sprintf("test-idle-%d", time.Now().UnixNano())
	limits := wire.Limits{
		WallTimeMs: 30000,
		IdleMs:     2000, // 2 s idle clock — fires because cat produces no output without input
		CpuMs:      30000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   512,
	}
	// `cat` blocks reading stdin and produces no output → idle clock fires.
	spec := buildSpec(jobID, []string{"cat"}, limits)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	require.NoError(t, err, "Create must not error")
	defer func() {
		_ = sb.Cleanup()
	}()

	start := time.Now()
	result, runErr := session.Run(ctx, sb, limits, func(_ context.Context) (int, error) {
		return 0, nil
	})
	elapsed := time.Since(start)

	require.NoError(t, runErr, "session.Run must not error")
	assert.True(t, result.IdleTimedOut, "idle clock must set IdleTimedOut=true")
	assert.False(t, result.TimedOut, "idle clock must not set TimedOut (that's wall/CPU)")
	assert.Less(t, elapsed, 15*time.Second,
		"idle clock should terminate within 15s of a 2s idle limit")

	assertNoLeak(t, cli, jobID)
}

// ── Task 3: CPU clock ─────────────────────────────────────────────────────────

// TestIntegrationCpuClock runs the "read one byte then spin" pattern described
// in Pitfall 1. The program reads a single byte from stdin to look "interactive",
// then enters a tight CPU loop. The CPU clock must fire before the wall clock
// (which has a much longer budget).
//
// Alpine sh script: read 1 byte via dd, then busy-spin in a while loop.
func TestIntegrationCpuClock(t *testing.T) {
	cli := requireDocker(t)
	r := newTestRunner(t)

	jobID := fmt.Sprintf("test-cpu-%d", time.Now().UnixNano())
	limits := wire.Limits{
		WallTimeMs: 15000, // generous wall clock (15 s)
		IdleMs:     15000, // generous idle
		CpuMs:      1000,  // tight CPU budget: kill after 1 s of CPU
		MemoryMb:   64,
		Pids:       64,
		OutputKb:   512,
	}

	// Shell one-liner:
	// 1. Read one byte from stdin (looks interactive to an idle-clock-only system)
	// 2. Spin in a tight arithmetic loop burning CPU
	// The CPU clock must catch this even though wall+idle both have 15s budgets.
	script := `dd if=/dev/stdin bs=1 count=1 2>/dev/null; while true; do :; done`
	spec := buildSpec(jobID, []string{"/bin/sh", "-c", script}, limits)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	require.NoError(t, err, "Create must not error")
	defer func() {
		_ = sb.Cleanup()
	}()

	// Write one byte to stdin to unblock the dd read so the loop can start.
	go func() {
		time.Sleep(200 * time.Millisecond) // give container a moment to start
		_, _ = sb.Stdin().Write([]byte("x"))
	}()

	start := time.Now()

	// Use the real cgroup CPU reader from the sandbox via CPUReader() accessor.
	cpuFn := extractCPUReader(sb)

	result, runErr := session.Run(ctx, sb, limits, cpuFn)
	elapsed := time.Since(start)

	require.NoError(t, runErr, "session.Run must not error")
	assert.True(t, result.TimedOut,
		"CPU clock must set TimedOut=true for read-one-byte-then-spin")
	assert.False(t, result.IdleTimedOut,
		"CPU clock must not set IdleTimedOut")
	// Must fire well before the 15s wall clock.
	assert.Less(t, elapsed, 12*time.Second,
		"CPU clock (1s budget) should kill the container before the 15s wall clock")

	assertNoLeak(t, cli, jobID)
}

// ── Task 3: Output truncation ─────────────────────────────────────────────────

// TestIntegrationOutputTruncation runs a program that floods stdout with far
// more bytes than OutputKb allows. The pump must set Truncated=true, the
// container must eventually be terminated (not block), and no container leaks.
func TestIntegrationOutputTruncation(t *testing.T) {
	cli := requireDocker(t)
	r := newTestRunner(t)

	jobID := fmt.Sprintf("test-trunc-%d", time.Now().UnixNano())
	limits := wire.Limits{
		WallTimeMs: 15000,
		IdleMs:     5000,
		CpuMs:      10000,
		MemoryMb:   64,
		Pids:       32,
		OutputKb:   4, // 4 KiB cap — easily exceeded by yes
	}

	// `yes` outputs "y\n" in a tight loop — will produce MBs quickly.
	// The pump drains but does not forward past 4 KiB, then sets Truncated=true.
	// The idle clock fires (after 5s) because the pump stops emitting activity
	// signals once the budget is exhausted.
	spec := buildSpec(jobID, []string{"yes"}, limits)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	require.NoError(t, err, "Create must not error")
	defer func() {
		_ = sb.Cleanup()
	}()

	result, runErr := session.Run(ctx, sb, limits, func(_ context.Context) (int, error) {
		return 0, nil
	})

	require.NoError(t, runErr, "session.Run must not error")
	assert.True(t, result.Truncated,
		"output-flood program must produce Truncated=true")

	assertNoLeak(t, cli, jobID)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// cpuReaderSandbox is the interface implemented by *dockerSandbox that exposes
// CPUReader(). We access it via type assertion.
// The dockerSandbox type is unexported, but the CPUReader() accessor method is
// exported and satisfies this interface.
type cpuReaderSandbox interface {
	CPUReader() func(ctx context.Context) (int, error)
}

// extractCPUReader type-asserts sb to cpuReaderSandbox and returns the real
// CPUUsageFunc that reads cgroup stats. If the assertion fails, it returns a
// zero-reader (CPU clock never fires).
func extractCPUReader(sb interface{}) func(ctx context.Context) (int, error) {
	if crs, ok := sb.(cpuReaderSandbox); ok {
		return crs.CPUReader()
	}
	return func(_ context.Context) (int, error) { return 0, nil }
}
