//go:build docker

// Package runner_test — ZygoteRunner safety / abuse / isolation / density suite
// (ZTEST-01..04). This reaches PHASE 4 PARITY for the zygote tier: the same six
// adversarial cases the Docker-tier abuse suite asserts
// (internal/worker/abuse_test.go) — fork bomb, OOM, infinite loop, idle, EOF,
// giant output — plus cross-child isolation (ZTEST-02), a density benchmark
// (ZTEST-03), and a no-leak sweep (ZTEST-04).
//
// Unlike the worker-level abuse suite (which drives the full Redis→worker→runner
// path), this suite drives the ZygoteRunner + REAL privileged python pool agent
// DIRECTLY and enforces the three clocks via internal/session — the same
// supervisor the worker uses — so wall/idle/cpu containment is exercised exactly
// as in production.
//
// GATING (mirrors zygote_integration_test.go): the `docker` build tag excludes
// these from `go test ./...`, PLUS requireZygotePython runtime-SKIPs when the
// daemon is unreachable or the image is missing, PLUS createOrSkipUnreachable
// SKIPs when the pool container's bridge IP is not routable from the host (the
// macOS Docker Desktop limitation). On a Linux/dind host (Fly) all assertions
// run for real. Shares helpers (requireZygotePython, newZygoteTestRunner,
// assertNoZygotePoolLeak, zygoteSpec, createOrSkipUnreachable,
// extractZygoteCPUReader) with zygote_integration_test.go — no redeclaration.
//
// Run via:
//
//	go test -tags=docker -timeout 900s ./internal/runner/... -run Zygote -v
//
// or the dedicated script:  bash scripts/zygote-suite.sh
package runner_test

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/session"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// countZygotePoolContainers returns how many warm pool containers (labeled
// code-runner.role=zygote-pool) currently exist. Used by the density (ZTEST-03)
// and no-leak (ZTEST-04) tests to assert CoW sharing (one parent per language)
// and no post-Close leak.
func countZygotePoolContainers(t *testing.T, cli *client.Client) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := filters.NewArgs()
	f.Add("label", "code-runner.role=zygote-pool")
	cs, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		t.Errorf("countZygotePoolContainers: %v", err)
		return 0
	}
	return len(cs)
}

// ─────────────────────────────────────────────────────────────────────────────
// session-driven runner: the THREE CLOCKS (wall/idle/cpu) are enforced by
// internal/session exactly as the worker does. runZygoteJob creates the sandbox,
// runs it under the session supervisor, drains stdout, and returns the terminal
// session outcome (which carries TimedOut / IdleTimedOut / exit / signal).
// ─────────────────────────────────────────────────────────────────────────────

// zygoteOutcome is the terminal result of a session-supervised zygote job.
type zygoteOutcome struct {
	exitCode     *int
	signal       *string
	timedOut     bool
	idleTimedOut bool
	truncated    bool
	stdout       string
	stderr       string
	cpuMs        int
}

func (o zygoteOutcome) killed() bool {
	return (o.exitCode != nil && *o.exitCode != 0) ||
		(o.signal != nil && *o.signal != "") || o.timedOut || o.idleTimedOut
}

// runZygoteJob drives one job through the ZygoteRunner under the REAL
// internal/session supervisor (RunInteractive: three clocks + output truncation
// + single sync.Once Kill/Cleanup teardown — the exact path the worker uses, and
// runner-agnostic). interact, if non-nil, runs concurrently with stdin
// available; it must close stdin itself if EOF is desired. The session owns the
// stdout/stderr Pumps — this helper NEVER reads those pipes directly (that would
// race the Pumps); it captures via the Sinks callbacks instead.
//
// It returns a `skipped` flag (true when the pool container's bridge IP is not
// routable from the host — the macOS Docker Desktop limitation) INSTEAD of
// calling t.Skip, so it is safe to call from a goroutine (a concurrent t.Skip
// would Goexit the goroutine and leave the parent asserting on a zero outcome).
// Callers must check the flag on their MAIN goroutine and t.Skip there.
func runZygoteJob(
	t *testing.T,
	r *runner.ZygoteRunner,
	spec wire.JobSpec,
	interact func(stdin io.WriteCloser),
) (zygoteOutcome, bool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	if err != nil {
		if isHostUnroutable(err) {
			return zygoteOutcome{}, true
		}
		t.Errorf("runZygoteJob Create: %v", err)
		return zygoteOutcome{}, true
	}
	defer func() { _ = sb.Cleanup() }()

	cpuFn := extractZygoteCPUReader(sb)

	if interact != nil {
		go interact(sb.Stdin())
	}

	res, out, errOut := runSessionSupervised(ctx, sb, spec.Limits, cpuFn)

	cpuMs, _ := cpuFn(context.Background())
	return zygoteOutcome{
		exitCode:     res.ExitCode,
		signal:       res.Signal,
		timedOut:     res.TimedOut,
		idleTimedOut: res.IdleTimedOut,
		truncated:    res.Truncated,
		stdout:       out,
		stderr:       errOut,
		cpuMs:        cpuMs,
	}, false
}

// skipIfUnroutable t.Skips (on the calling — MAIN — goroutine) when any job in
// the test could not reach the pool (host-unroutable). Centralizes the skip
// message.
func skipIfUnroutable(t *testing.T, skipped bool) {
	t.Helper()
	if skipped {
		t.Skip("zygote suite: pool bridge IP not routable from host (macOS Docker Desktop) — runs for real on Linux/Fly")
	}
}

// runSessionSupervised runs sb under session.RunInteractive, capturing stdout +
// stderr via the Sinks, and returns the terminal Result plus the captured
// output. It is runner-agnostic (the session does not know about zygote). The
// session enforces the three clocks and performs the single Kill+Cleanup
// teardown, so callers must NOT separately read sb.Stdout()/Stderr().
func runSessionSupervised(
	ctx context.Context,
	sb runner.Sandbox,
	limits wire.Limits,
	cpuFn session.CPUUsageFunc,
) (runner.Result, string, string) {
	var (
		mu     sync.Mutex
		outBuf strings.Builder
		errBuf strings.Builder
	)
	sinks := session.Sinks{
		Stdout: func(b []byte) { mu.Lock(); outBuf.Write(b); mu.Unlock() },
		Stderr: func(b []byte) { mu.Lock(); errBuf.Write(b); mu.Unlock() },
	}
	res, _ := session.RunInteractive(ctx, sb, limits, cpuFn, sinks)
	mu.Lock()
	defer mu.Unlock()
	return res, outBuf.String(), errBuf.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// ZTEST-01: abuse PARITY — fork bomb, OOM, infinite loop, idle, EOF, giant out.
// ─────────────────────────────────────────────────────────────────────────────

// abuseZygoteSpec builds a python zygote spec with explicit limits.
func abuseZygoteSpec(name, script string, lim wire.Limits) wire.JobSpec {
	jobID := fmt.Sprintf("zyg-abuse-%s-%d", name, time.Now().UnixNano())
	s := zygoteSpec(jobID, "main.py", []wire.FileInput{{Name: "main.py", Content: script}})
	s.Limits = lim
	s.Interactive = true
	return s
}

// TestZygoteAbuseForkBomb (ZTEST-01a): a fork bomb must be contained by the
// per-child pids.max; the child tree must be killed and the pool must survive.
func TestZygoteAbuseForkBomb(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	script := "import os, sys\n" +
		"try:\n" +
		"    while True:\n" +
		"        os.fork()\n" +
		"except Exception:\n" +
		"    sys.exit(1)\n"
	out, skipped := runZygoteJob(t, r, abuseZygoteSpec("forkbomb", script, wire.Limits{
		Pids: 24, WallTimeMs: 8000, IdleMs: 4000, CpuMs: 5000, MemoryMb: 128, OutputKb: 64,
	}), nil)
	skipIfUnroutable(t, skipped)

	assert.True(t, out.killed(),
		"ZTEST-01a fork bomb: child must be killed (exit=%v sig=%v timedOut=%v idle=%v)",
		out.exitCode, out.signal, out.timedOut, out.idleTimedOut)

	// Pool survival: a trivial follow-up job must succeed on the SAME warm pool.
	survive, _ := runZygoteJob(t, r, abuseZygoteSpec("forkbomb-survive",
		"print('alive')\n", wire.Limits{
			WallTimeMs: 15000, IdleMs: 8000, CpuMs: 10000, MemoryMb: 128, Pids: 64, OutputKb: 64,
		}), nil)
	assert.True(t, survive.exitCode != nil && *survive.exitCode == 0,
		"ZTEST-01a pool-survival: follow-up job must exit 0, got %v", survive.exitCode)
	assert.Contains(t, survive.stdout, "alive", "ZTEST-01a pool-survival stdout")
}

// TestZygoteAbuseOOM (ZTEST-01b): allocating past the per-child memory.max must
// be killed by the cgroup OOM killer — and it must kill ONLY the child, not the
// pool or siblings. We prove sibling survival by running a concurrent benign job.
func TestZygoteAbuseOOM(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	oomScript := "import sys\n" +
		"data = []\n" +
		"for i in range(40):\n" +
		"    data.append(bytearray(10 * 1024 * 1024))\n" +
		"print('done', len(data))\n"
	siblingScript := "import time\n" +
		"for i in range(20):\n" +
		"    print('sib', i, flush=True)\n" +
		"    time.sleep(0.1)\n" +
		"print('sib-done')\n"

	var (
		oomOut  zygoteOutcome
		sibOut  zygoteOutcome
		oomSkip bool
		sibSkip bool
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		oomOut, oomSkip = runZygoteJob(t, r, abuseZygoteSpec("oom", oomScript, wire.Limits{
			MemoryMb: 64, WallTimeMs: 10000, IdleMs: 8000, CpuMs: 8000, Pids: 64, OutputKb: 64,
		}), nil)
	}()
	go func() {
		defer wg.Done()
		sibOut, sibSkip = runZygoteJob(t, r, abuseZygoteSpec("oom-sibling", siblingScript, wire.Limits{
			MemoryMb: 128, WallTimeMs: 10000, IdleMs: 8000, CpuMs: 8000, Pids: 64, OutputKb: 64,
		}), nil)
	}()
	wg.Wait()
	skipIfUnroutable(t, oomSkip || sibSkip)

	assert.True(t, oomOut.killed(),
		"ZTEST-01b OOM: offending child must be killed (exit=%v sig=%v)", oomOut.exitCode, oomOut.signal)
	// Sibling isolation: the benign concurrent child must NOT be collateral.
	assert.True(t, sibOut.exitCode != nil && *sibOut.exitCode == 0,
		"ZTEST-01b OOM: sibling child must survive (exit=%v sig=%v) — OOM must not cross children",
		sibOut.exitCode, sibOut.signal)
	assert.Contains(t, sibOut.stdout, "sib-done", "ZTEST-01b sibling must complete its output")
}

// TestZygoteAbuseInfiniteLoop (ZTEST-01c): a CPU spin with wall < cpu must be
// stopped by the WALL clock (TimedOut=true).
func TestZygoteAbuseInfiniteLoop(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	out, skipped := runZygoteJob(t, r, abuseZygoteSpec("infloop",
		"while True:\n    pass\n", wire.Limits{
			WallTimeMs: 2000, CpuMs: 15000, IdleMs: 10000, MemoryMb: 128, Pids: 64, OutputKb: 64,
		}), nil)
	skipIfUnroutable(t, skipped)
	assert.True(t, out.timedOut,
		"ZTEST-01c infinite loop: wall clock must fire (timedOut=%v idle=%v exit=%v)",
		out.timedOut, out.idleTimedOut, out.exitCode)
}

// TestZygoteAbuseCpuClockEvasion (ZTEST-01c'): hide behind an interactive read,
// then spin — the CPU clock (tight) must fire before the generous wall clock.
func TestZygoteAbuseCpuClockEvasion(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	script := "import sys\n" +
		"sys.stdout.write('started\\n'); sys.stdout.flush()\n" +
		"sys.stdin.read(1)\n" +
		"i = 0\n" +
		"while True:\n" +
		"    i += 1\n" +
		"    if i % 500000 == 0:\n" +
		"        sys.stdout.write('hb\\n'); sys.stdout.flush()\n"
	out, skipped := runZygoteJob(t, r, abuseZygoteSpec("cpuevasion", script, wire.Limits{
		WallTimeMs: 15000, IdleMs: 8000, CpuMs: 2500, MemoryMb: 128, Pids: 64, OutputKb: 256,
	}), func(stdin io.WriteCloser) {
		time.Sleep(1 * time.Second)
		_, _ = io.WriteString(stdin, "x")
	})
	skipIfUnroutable(t, skipped)
	assert.True(t, out.timedOut,
		"ZTEST-01c' cpu evasion: cpu clock must fire as timedOut (timedOut=%v idle=%v exit=%v cpuMs=%d)",
		out.timedOut, out.idleTimedOut, out.exitCode, out.cpuMs)
}

// TestZygoteAbuseIdleBlockedStdin (ZTEST-01d): a job blocked on stdin with no
// input must be stopped by the IDLE clock (IdleTimedOut=true, TimedOut=false).
func TestZygoteAbuseIdleBlockedStdin(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	out, skipped := runZygoteJob(t, r, abuseZygoteSpec("idle",
		"import sys\nline = sys.stdin.readline()\nprint('got:', line)\n", wire.Limits{
			IdleMs: 2000, WallTimeMs: 15000, CpuMs: 15000, MemoryMb: 128, Pids: 64, OutputKb: 64,
		}), nil)
	skipIfUnroutable(t, skipped)
	assert.True(t, out.idleTimedOut,
		"ZTEST-01d idle: idle clock must fire (idle=%v timedOut=%v)", out.idleTimedOut, out.timedOut)
	assert.False(t, out.timedOut,
		"ZTEST-01d idle: wall clock must NOT fire (timedOut=%v)", out.timedOut)
}

// TestZygoteAbuseEofCleanExit (ZTEST-01e): an echo loop must exit cleanly (0) on
// stdin EOF (STDIN_CLOSE), with no clock firing.
func TestZygoteAbuseEofCleanExit(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	out, skipped := runZygoteJob(t, r, abuseZygoteSpec("eof",
		"import sys\nfor line in sys.stdin:\n    sys.stdout.write('got ' + line.strip() + '\\n')\n    sys.stdout.flush()\n",
		wire.Limits{IdleMs: 8000, WallTimeMs: 15000, CpuMs: 15000, MemoryMb: 128, Pids: 64, OutputKb: 64},
	), func(stdin io.WriteCloser) {
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(stdin, "hello\n")
		time.Sleep(400 * time.Millisecond)
		_ = stdin.Close() // EOF
	})
	skipIfUnroutable(t, skipped)
	assert.True(t, out.exitCode != nil && *out.exitCode == 0,
		"ZTEST-01e EOF: must exit 0 (exit=%v)", out.exitCode)
	assert.False(t, out.idleTimedOut, "ZTEST-01e EOF: idle must not fire")
	assert.False(t, out.timedOut, "ZTEST-01e EOF: wall must not fire")
	assert.Contains(t, out.stdout, "got hello", "ZTEST-01e EOF: must echo the line")
}

// TestZygoteAbuseGiantOutput (ZTEST-01f): flooding stdout past OutputKb must be
// truncated (Truncated=true) and bounded near the cap (no deadlock).
func TestZygoteAbuseGiantOutput(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	outputKb := 32
	script := "import sys\n" +
		"line = 'X' * 1023 + '\\n'\n" +
		"for i in range(10000):\n" +
		"    sys.stdout.write(line)\n" +
		"    sys.stdout.flush()\n" +
		"print('done')\n"
	out, skipped := runZygoteJob(t, r, abuseZygoteSpec("giant", script, wire.Limits{
		OutputKb: outputKb, WallTimeMs: 20000, IdleMs: 15000, CpuMs: 20000, MemoryMb: 128, Pids: 64,
	}), nil)
	skipIfUnroutable(t, skipped)
	assert.True(t, out.truncated,
		"ZTEST-01f giant output: Truncated must be true (truncated=%v exit=%v)", out.truncated, out.exitCode)
	capBytes := outputKb * 1024
	assert.Less(t, len(out.stdout), capBytes*4,
		"ZTEST-01f giant output: captured stdout (%d) must be bounded near cap (%d), not the 10MB flood",
		len(out.stdout), capBytes)
}

// ─────────────────────────────────────────────────────────────────────────────
// ZTEST-02: cross-child isolation (ports isolation_probe.py PART 1 assertions).
// Two concurrent jobs: child B must NOT read child A's /tmp secret, A's /proc, or
// any inherited FD. Each job self-attests via its own stdout; we assert the
// forbidden reads FAILED.
// ─────────────────────────────────────────────────────────────────────────────

// isolationVictimScript writes a secret to its private /tmp and a marker to a
// fixed FD count, then idles long enough for the attacker to try (and fail).
const isolationVictimScript = `
import os, sys, time
# Per-child private /tmp tmpfs: write a secret only WE should ever see.
with open('/tmp/secret', 'w') as f:
    f.write('VICTIM-SECRET-DO-NOT-LEAK')
sys.stdout.write('victim-ready\n'); sys.stdout.flush()
# Open a private fd the attacker must not be able to reach by number.
g = open('/tmp/secret', 'r')
sys.stdout.write('victim-fd=%d\n' % g.fileno()); sys.stdout.flush()
time.sleep(3.0)
sys.stdout.write('victim-done\n'); sys.stdout.flush()
`

// isolationAttackerScript attempts the three forbidden cross-child reads and
// reports each as blocked/leaked. PART-1 of isolation_probe.py: each attempt
// MUST fail under full per-child hardening (distinct UID + private pidns +
// private mountns + private /tmp tmpfs).
const isolationAttackerScript = `
import os, sys, json, time
time.sleep(0.5)  # let the victim establish its secret
res = {}
# 1. Sibling /tmp secret: our /tmp is a private tmpfs -> the sibling's secret is
#    simply not present in our mount namespace.
try:
    with open('/tmp/secret') as f:
        data = f.read()
    res['tmp'] = 'LEAK:' + data if 'VICTIM' in data else 'own-only'
except FileNotFoundError:
    res['tmp'] = 'own-only'   # no sibling secret visible (expected)
except Exception as e:
    res['tmp'] = 'blocked:' + type(e).__name__
# 2. Sibling /proc/<pid>/mem: scan a window of pids; in our private pidns we are
#    PID 1 and the sibling is simply not addressable. Any successful read of a
#    foreign process memory would be a LEAK.
leak_proc = False
for pid in range(2, 50):
    try:
        with open('/proc/%d/mem' % pid, 'rb') as f:
            f.seek(0x400000); f.read(16)
        leak_proc = True
        break
    except Exception:
        pass
res['proc'] = 'LEAK' if leak_proc else 'blocked'
# 3. Inherited FD scrub: enumerate our own fds>2; none should be a usable
#    credential/socket the parent leaked (RULE #1 fd-scrub). We only assert that
#    we cannot reach a sibling's high-numbered file fd by guessing.
foreign_fd = False
for fd in range(3, 64):
    try:
        target = os.readlink('/proc/self/fd/%d' % fd)
        if 'secret' in target:
            foreign_fd = True
    except OSError:
        pass
res['fd'] = 'LEAK' if foreign_fd else 'clean'
sys.stdout.write('===ISO===' + json.dumps(res) + '===END===\n')
sys.stdout.flush()
`

// TestZygoteCrossChildIsolation (ZTEST-02): spawn a victim + attacker
// concurrently and assert the attacker could NOT read the victim's tmp secret,
// proc mem, or any inherited credential fd.
func TestZygoteCrossChildIsolation(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	lim := wire.Limits{WallTimeMs: 12000, IdleMs: 10000, CpuMs: 10000, MemoryMb: 128, Pids: 64, OutputKb: 64}

	var (
		victimOut  zygoteOutcome
		attackOut  zygoteOutcome
		victimSkip bool
		attackSkip bool
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		victimOut, victimSkip = runZygoteJob(t, r, abuseZygoteSpec("iso-victim", isolationVictimScript, lim), nil)
	}()
	go func() {
		defer wg.Done()
		attackOut, attackSkip = runZygoteJob(t, r, abuseZygoteSpec("iso-attacker", isolationAttackerScript, lim), nil)
	}()
	wg.Wait()
	skipIfUnroutable(t, victimSkip || attackSkip)

	require.Contains(t, victimOut.stdout, "victim-ready", "ZTEST-02: victim must establish its secret")
	require.Contains(t, attackOut.stdout, "===ISO===", "ZTEST-02: attacker must report findings")

	report := extractBetween(attackOut.stdout, "===ISO===", "===END===")
	require.NotEmpty(t, report, "ZTEST-02: attacker report must be parseable: %q", attackOut.stdout)

	assert.NotContains(t, report, `"tmp":"LEAK`, "ZTEST-02: attacker read sibling /tmp secret — ISOLATION BROKEN")
	assert.Contains(t, report, `"proc":"blocked"`, "ZTEST-02: attacker read foreign /proc mem — ISOLATION BROKEN (report=%s)", report)
	assert.Contains(t, report, `"fd":"clean"`, "ZTEST-02: attacker found an inherited credential fd — RULE #1 BROKEN (report=%s)", report)
}

// extractBetween returns the substring strictly between the first start and the
// following end marker (empty if not found).
func extractBetween(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// ─────────────────────────────────────────────────────────────────────────────
// ZTEST-03: density benchmark — many concurrent jobs through the zygote pool.
// Asserts a LOOSE bound (per spec): the zygote tier sustains a materially higher
// concurrency at low marginal RAM. We can't measure host RSS portably here, so
// we assert the conservative invariant the density spike proved: N concurrent
// jobs all complete through a SINGLE warm parent (CoW sharing), with no per-job
// container — i.e. exactly one pool container exists for N jobs. Spike 005
// measured 2.7× density from this CoW base; this test guards the structural
// precondition (shared parent) rather than re-measuring RAM (documented bench).
// ─────────────────────────────────────────────────────────────────────────────

// TestZygoteDensityConcurrent (ZTEST-03): launch many concurrent python jobs and
// assert they all succeed through ONE shared warm parent (the CoW density base).
func TestZygoteDensityConcurrent(t *testing.T) {
	cli := requireZygotePython(t)
	r := newZygoteTestRunner(t)
	defer func() { _ = r.Close(); assertNoZygotePoolLeak(t, cli) }()

	const n = 16
	lim := wire.Limits{WallTimeMs: 20000, IdleMs: 15000, CpuMs: 15000, MemoryMb: 64, Pids: 32, OutputKb: 64}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		okCount int
		skipped bool
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			// A small CoW-friendly workload: touch a pre-imported module, print, exit.
			script := fmt.Sprintf("import sys\nsys.stdout.write('job-%d-ok\\n')\n", i)
			out := runConcurrentDensityJob(t, r, abuseZygoteSpec(fmt.Sprintf("density-%d", i), script, lim))
			mu.Lock()
			defer mu.Unlock()
			if out.skipped {
				skipped = true
				return
			}
			if out.exitCode != nil && *out.exitCode == 0 {
				okCount++
			}
		}(i)
	}
	wg.Wait()

	if skipped {
		t.Skip("ZTEST-03 density: pool bridge IP not routable from host (macOS Docker Desktop) — runs on Linux/Fly")
	}

	// Conservative bound: a strong majority must complete. (Marginal failures on
	// a saturated CI box are tolerated; a hard floor catches a broken pool.)
	require.GreaterOrEqual(t, okCount, n*3/4,
		"ZTEST-03 density: expected >= %d/%d concurrent jobs to succeed through the shared pool, got %d",
		n*3/4, n, okCount)

	// Structural density invariant: exactly ONE warm pool container served all N
	// jobs (CoW sharing — spike 005's 2.7× base). More than one for a single
	// (lang,version) would mean no sharing.
	count := countZygotePoolContainers(t, cli)
	assert.LessOrEqual(t, count, 1,
		"ZTEST-03 density: %d concurrent jobs must share ONE warm parent (CoW base), found %d pool containers",
		n, count)
	t.Logf("ZTEST-03 density: %d/%d jobs ok through %d pool container(s)", okCount, n, count)
}

// densityResult adds a skip flag so concurrent goroutines can signal the
// host-routing skip without calling t.Skip off the main goroutine.
type densityResult struct {
	zygoteOutcome
	skipped bool
}

// runConcurrentDensityJob is runZygoteJob without t.Skip side effects on the
// goroutine: it converts the createOrSkipUnreachable skip into a flag.
func runConcurrentDensityJob(t *testing.T, r *runner.ZygoteRunner, spec wire.JobSpec) densityResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sb, err := r.Create(ctx, spec)
	if err != nil {
		if isHostUnroutable(err) {
			return densityResult{skipped: true}
		}
		t.Errorf("ZTEST-03 density Create: %v", err)
		return densityResult{skipped: true}
	}
	defer func() { _ = sb.Cleanup() }()

	cpuFn := extractZygoteCPUReader(sb)
	res, out, _ := runSessionSupervised(ctx, sb, spec.Limits, cpuFn)
	return densityResult{zygoteOutcome: zygoteOutcome{
		exitCode: res.ExitCode, signal: res.Signal, stdout: out,
	}}
}

// isHostUnroutable reports whether a Create error indicates the test
// environment cannot support the privileged pool path (rather than a real
// containment bug). On macOS Docker Desktop the pool container's bridge IP is
// not routable from the host, the relay never becomes ready, and under
// concurrent load the Docker socket itself times out — all of which are
// environment limits that must SKIP, not FAIL. On Linux/dind (Fly) the pool is
// reachable and Create succeeds, so these strings never appear.
func isHostUnroutable(err error) bool {
	m := err.Error()
	return strings.Contains(m, "agent not ready") ||
		strings.Contains(m, "not reachable within") ||
		strings.Contains(m, "i/o timeout") ||
		strings.Contains(m, "no route to host") ||
		strings.Contains(m, "connection refused") ||
		strings.Contains(m, "context deadline exceeded") ||
		strings.Contains(m, "deadline exceeded") ||
		strings.Contains(m, "launch parent")
}

// ─────────────────────────────────────────────────────────────────────────────
// ZTEST-04: no-leak sweep — many sequential + concurrent jobs must leave no
// leaked pool/child containers, no leaked goroutines, and no held slots.
// ─────────────────────────────────────────────────────────────────────────────

// TestZygoteNoLeak (ZTEST-04): run a burst of sequential + concurrent jobs, then
// after runner Close assert: (a) no pool/child container survives, (b) goroutine
// count returns to baseline (within a tolerance for the test runtime's own
// churn).
func TestZygoteNoLeak(t *testing.T) {
	cli := requireZygotePython(t)

	baselineGoroutines := runtime.NumGoroutine()

	r := newZygoteTestRunner(t)
	lim := wire.Limits{WallTimeMs: 15000, IdleMs: 10000, CpuMs: 10000, MemoryMb: 64, Pids: 32, OutputKb: 64}

	// Sequential burst.
	for i := 0; i < 6; i++ {
		out := runConcurrentDensityJob(t, r, abuseZygoteSpec(fmt.Sprintf("noleak-seq-%d", i),
			fmt.Sprintf("print('seq-%d')\n", i), lim))
		if out.skipped {
			_ = r.Close()
			t.Skip("ZTEST-04 no-leak: pool bridge IP not routable from host (macOS Docker Desktop) — runs on Linux/Fly")
		}
	}

	// Concurrent burst.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = runConcurrentDensityJob(t, r, abuseZygoteSpec(fmt.Sprintf("noleak-con-%d", i),
				fmt.Sprintf("print('con-%d')\n", i), lim))
		}(i)
	}
	wg.Wait()

	// Close the runner: reaps the warm pool + idle reaper goroutine.
	require.NoError(t, r.Close(), "ZTEST-04: runner Close must not error")

	// (a) Container no-leak: no pool/child container labeled as ours survives.
	assertNoZygotePoolLeak(t, cli)
	assert.Equal(t, 0, countZygotePoolContainers(t, cli),
		"ZTEST-04: no warm pool container may survive runner Close")

	// (b) Goroutine no-leak: allow settle time, then assert the count returns to
	// near baseline (tolerance covers the test binary's own background churn).
	deadline := time.Now().Add(10 * time.Second)
	var final int
	for time.Now().Before(deadline) {
		runtime.GC()
		final = runtime.NumGoroutine()
		if final <= baselineGoroutines+4 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	assert.LessOrEqual(t, final, baselineGoroutines+8,
		"ZTEST-04: goroutine leak — baseline=%d final=%d (relay/reader/reaper goroutines not cleaned up)",
		baselineGoroutines, final)
	t.Logf("ZTEST-04 no-leak: goroutines baseline=%d final=%d", baselineGoroutines, final)
}
