// Package runner implements the DockerSocketRunner: a fully-hardened sandbox
// runner that speaks the moby/Docker SDK over the mounted host socket.
//
// Security model:
//   - Every container is created with the FULL hardening flag set in ONE place
//     (Create), applied unconditionally (HARD-01..05 per plan 02-03).
//   - The docker socket is NEVER mounted into any sandbox container.
//   - Kill destroys the entire container tree (not just PID-1) via
//     ContainerKill + ContainerRemove(force).
//   - Cleanup is idempotent via sync.Once so no container leaks on any path.
package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/teovillanueva/code-runner/internal/config"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// Compile-time assertions: DockerSocketRunner must implement Runner;
// dockerSandbox must implement Sandbox.
var _ Runner = (*DockerSocketRunner)(nil)
var _ Sandbox = (*dockerSandbox)(nil)

// sandboxUser is the non-root user run inside every sandbox.
// UID 65534 is "nobody" on most Linux distros (traditional "nobody" UID).
const sandboxUser = "65534:65534"

// sandboxWorkDir is the working directory inside the container where files
// are copied before the entrypoint runs.
const sandboxWorkDir = "/workspace"

// defaultTmpfsSize is the size cap for the /tmp tmpfs mount in megabytes when
// not derived from the job limits.
const defaultTmpfsSizeMb = 64

// DockerSocketRunner implements Runner over the moby Docker SDK. It creates
// fully-hardened containers, attaches stdin/stdout/stderr, and returns a
// dockerSandbox that delegates clock management and teardown to internal/session.
type DockerSocketRunner struct {
	cli            *client.Client
	cfg            config.Config
	seccompProfile string // inline JSON content of the seccomp profile (not a path)
}

// NewDockerSocketRunner creates a DockerSocketRunner. It connects to the Docker
// daemon using the environment settings (DOCKER_HOST), optionally overriding
// with cfg.DockerHost. cfg.SandboxRuntime is used as the OCI runtime for
// container creation (e.g. "runsc" for gVisor); empty string uses daemon default.
//
// seccompProfilePath must be the path to profiles/seccomp/runner.json readable
// by the worker process. The file is read at construction time and its JSON
// content is embedded inline in SecurityOpt at container creation time. This
// is the correct behaviour for the moby SDK: the Docker daemon (which may run
// inside a VM on macOS/Docker Desktop) cannot access host filesystem paths, so
// the JSON must be sent inline rather than as a path. The Docker CLI does the
// same: it reads the file and embeds the JSON before sending to the daemon.
//
// Passing an empty string disables the seccomp override (daemon default applies).
func NewDockerSocketRunner(cfg config.Config, seccompProfilePath string) (*DockerSocketRunner, error) {
	opts := []client.Opt{
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	}
	if cfg.DockerHost != "" {
		opts = append(opts, client.WithHost(cfg.DockerHost))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker: create client: %w", err)
	}

	// Read the seccomp profile JSON inline. The moby SDK sends SecurityOpt values
	// verbatim to the daemon; the daemon expects the raw JSON, not a path.
	// (Contrast with the Docker CLI which also reads the file before sending.)
	var seccompJSON string
	if seccompProfilePath != "" {
		data, readErr := os.ReadFile(seccompProfilePath)
		if readErr != nil {
			return nil, fmt.Errorf("docker: read seccomp profile %q: %w", seccompProfilePath, readErr)
		}
		seccompJSON = string(data)
	}

	return &DockerSocketRunner{
		cli:            cli,
		cfg:            cfg,
		seccompProfile: seccompJSON,
	}, nil
}

// Create allocates a fully-hardened sandbox from the resolved spec and returns
// a dockerSandbox ready for pipe attachment. The container is created and
// started; stdin/stdout/stderr are attached and demuxed via stdcopy.StdCopy.
//
// All hardening flags are set unconditionally in one place here (HARD-01..05):
//   - NetworkMode="none"                           (HARD-01, T-02-09)
//   - ReadonlyRootfs=true + tmpfs /tmp             (HARD-02)
//   - Memory==MemorySwap (no swap)                 (HARD-03, T-02-10)
//   - PidsLimit, NanoCPUs                          (HARD-04, T-02-10)
//   - CapDrop=ALL, no-new-privileges, seccomp      (HARD-05, T-02-08)
//   - non-root user, no docker socket in mounts    (T-02-12)
func (r *DockerSocketRunner) Create(ctx context.Context, spec wire.JobSpec) (Sandbox, error) {
	// ── Derive limits ────────────────────────────────────────────────────────
	memBytes := int64(spec.Limits.MemoryMb) * 1024 * 1024
	if memBytes <= 0 {
		memBytes = 128 * 1024 * 1024 // 128 MiB default
	}

	// NanoCPUs: sets the Linux CPU quota for the container. We cap at 1 CPU;
	// the session CPU clock enforces the cumulative CpuMs budget independently.
	// 1 NanoCPU unit = 1e-9 CPU; 1 full CPU = 1_000_000_000 NanoCPUs.
	nanoCPUs := int64(1_000_000_000) // 1 CPU

	pidsLimit := int64(spec.Limits.Pids)
	if pidsLimit <= 0 {
		pidsLimit = 64 // sane default
	}

	tmpfsSizeMb := defaultTmpfsSizeMb
	if spec.Limits.OutputKb > 0 {
		// Give /tmp a bit more headroom than output cap; still bounded.
		tmpfsSizeMb = (spec.Limits.OutputKb/1024 + 1) * 2
		if tmpfsSizeMb < defaultTmpfsSizeMb {
			tmpfsSizeMb = defaultTmpfsSizeMb
		}
	}
	tmpfsOpts := fmt.Sprintf("rw,noexec,nosuid,size=%dm", tmpfsSizeMb)

	// ── Seccomp security options (HARD-05) ──────────────────────────────────
	// r.seccompProfile holds the raw JSON content (read at construction time).
	// The moby SDK expects the JSON inline, not a filesystem path, because the
	// Docker daemon may run in a VM (Docker Desktop / macOS) and cannot access
	// the host's filesystem paths directly.
	securityOpts := []string{"no-new-privileges"}
	if r.seccompProfile != "" {
		securityOpts = append(securityOpts, "seccomp="+r.seccompProfile)
	}

	// ── Container config ────────────────────────────────────────────────────
	containerCfg := &container.Config{
		Image:        spec.Image,
		Cmd:          strslice.StrSlice(spec.Run),
		WorkingDir:   sandboxWorkDir,
		User:         sandboxUser,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		StdinOnce:    true, // Close stdin when the single attached client disconnects.
		// Without StdinOnce=true, the container's stdin pipe stays open after the
		// attach connection closes (OpenStdin=true + StdinOnce=false is the Docker
		// default: stdin remains open for re-attachment). With StdinOnce=true, Docker
		// Engine closes the container's stdin pipe (delivering EOF) when our attach
		// session ends — which is the correct behaviour for single-session interactive
		// runs where stdin_close → EOF → clean exit is the expected code path.
		Labels: map[string]string{
			"code-runner.jobId": spec.JobId,
		},
	}

	// ── Host config (full hardening in one place) ─────────────────────────
	hostCfg := &container.HostConfig{
		// HARD-01: no network access — no SSRF, no Redis/soketi/metadata reach
		NetworkMode: "none",

		// HARD-02: read-only root + size-capped tmpfs scratch space.
		//
		// /tmp  — scratch space for Python/other runtimes (byte-code cache etc.)
		//         Declared in HostConfig.Tmpfs (legacy map form) with noexec.
		//
		// /workspace — anonymous Docker volume that is writable even with
		//         ReadonlyRootfs=true. We use mount.TypeVolume (not tmpfs) for
		//         /workspace because Docker's CopyToContainer respects volume
		//         contents across ContainerCreate → ContainerStart. If /workspace
		//         were a tmpfs, Docker would overwrite it with an empty tmpfs on
		//         container start, discarding any files we copied before start.
		//         With a volume, files copied via CopyToContainer before start are
		//         visible to the running process (preserving the start-handshake
		//         contract: files in place before the process reads them).
		//
		//         The anonymous volume is removed on Cleanup/Kill via
		//         RemoveOptions{RemoveVolumes:true} so no host-disk residue
		//         survives the job. HARD-02 is preserved: ReadonlyRootfs remains
		//         true; only /tmp (tmpfs) and /workspace (volume) are writable.
		ReadonlyRootfs: true,
		Tmpfs: map[string]string{
			"/tmp": tmpfsOpts,
		},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeVolume,
				Target: sandboxWorkDir,
				// Empty Source → Docker creates an anonymous volume scoped to
				// this container. It is auto-removed when the container is removed
				// with RemoveVolumes=true (see Kill and Cleanup below).
				VolumeOptions: &mount.VolumeOptions{
					Labels: map[string]string{
						"code-runner.jobId": spec.JobId,
					},
				},
			},
		},

		// HARD-03: memory cap with swap disabled (Memory == MemorySwap)
		// HARD-04: CPU quota + pids limit
		Resources: container.Resources{
			Memory:    memBytes,
			MemorySwap: memBytes, // equal → no swap (Pitfall 5, HARD-03)
			NanoCPUs:  nanoCPUs,
			PidsLimit: &pidsLimit,
		},

		// HARD-05: drop all capabilities, restrict syscalls
		CapDrop:     strslice.StrSlice{"ALL"},
		SecurityOpt: securityOpts,
	}

	// Optional runtime override (e.g. "runsc" for gVisor) — one-line swap.
	if r.cfg.SandboxRuntime != "" {
		hostCfg.Runtime = r.cfg.SandboxRuntime
	}

	// ── Create container ────────────────────────────────────────────────────
	resp, err := r.cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("docker: ContainerCreate: %w", err)
	}
	containerID := resp.ID

	// ── Copy source files into workspace before starting ────────────────────
	// CopyToContainer works before ContainerStart because /workspace is an
	// anonymous Docker volume (TypeVolume). Unlike tmpfs, volume contents
	// survive the container create→start transition and are visible to the
	// running process.
	if len(spec.Files) > 0 {
		if err := r.copyFilesToContainer(ctx, containerID, spec.Files); err != nil {
			// Clean up: remove container AND its anonymous volume.
			_ = r.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
			return nil, fmt.Errorf("docker: copy files: %w", err)
		}
	}

	// ── Attach stdin/stdout/stderr ───────────────────────────────────────────
	// ContainerAttach returns a hijacked connection. We demux the multiplexed
	// stdout/stderr stream via stdcopy.StdCopy into two io.Pipe pairs so
	// callers receive clean per-stream bytes (RUN-03).
	attachResp, err := r.cli.ContainerAttach(ctx, containerID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		_ = r.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		return nil, fmt.Errorf("docker: ContainerAttach: %w", err)
	}

	// Create io.Pipe pairs for demuxed stdout and stderr.
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	// Start the demux goroutine. It calls stdcopy.StdCopy which blocks until
	// the attach connection is closed (i.e. container exits).
	go func() {
		_, copyErr := stdcopy.StdCopy(stdoutW, stderrW, attachResp.Reader)
		// Close pipes with the copy error (nil on clean EOF) so readers receive EOF.
		stdoutW.CloseWithError(copyErr) //nolint:errcheck
		stderrW.CloseWithError(copyErr) //nolint:errcheck
	}()

	// ── Start container ──────────────────────────────────────────────────────
	if err := r.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		attachResp.Close()
		_ = r.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		return nil, fmt.Errorf("docker: ContainerStart: %w", err)
	}

	// ── Stdin pipe ───────────────────────────────────────────────────────────
	// Create an internal io.Pipe for stdin. Writes go to the pipe writer (stdinW);
	// a goroutine copies from the pipe reader to attachResp.Conn (the actual Docker
	// attach connection). When stdinW.Close() is called (to signal EOF), the copy
	// goroutine sees io.EOF from the pipe reader. It then waits stdinEOFFlushDelay
	// to let the container process produce its final stdout, then closes the attach
	// connection.
	//
	// This indirection is required on macOS Docker Desktop where closing the attach
	// connection immediately closes both read and write directions (the Unix socket
	// proxy does not support TCP half-close). By delaying the connection close, we
	// ensure stdcopy.StdCopy has time to forward the process's last output bytes.
	stdinR, stdinW := io.Pipe()
	go func() {
		io.Copy(attachResp.Conn, stdinR) //nolint:errcheck
		stdinR.Close()                   //nolint:errcheck
		// Flush window: give the container process time to write its final output
		// before we close the read direction of the attach connection.
		time.Sleep(stdinEOFFlushDelay)
		attachResp.Conn.Close() //nolint:errcheck
	}()

	sb := &dockerSandbox{
		cli:         r.cli,
		containerID: containerID,
		attachResp:  attachResp,
		stdinW:      stdinW,
		stdoutR:     stdoutR,
		stderrR:     stderrR,
		spec:        spec,
		startTime:   time.Now(),
		cpuReader:   newContainerCPUReader(r.cli, containerID),
	}
	return sb, nil
}

// copyFilesToContainer writes spec.Files into the container's workspace via a
// tar archive uploaded with CopyToContainer.
func (r *DockerSocketRunner) copyFilesToContainer(ctx context.Context, containerID string, files []wire.FileInput) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, f := range files {
		content := []byte(f.Content)
		hdr := &tar.Header{
			Name: filepath.Base(f.Name), // sanitize to avoid path traversal
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(content); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return r.cli.CopyToContainer(ctx, containerID, sandboxWorkDir, &buf, container.CopyToContainerOptions{})
}

// ── dockerSandbox ─────────────────────────────────────────────────────────────

// stdinEOFFlushDelay is the time to wait after stdin is closed before closing
// the attach connection. This gives the container process time to produce its
// final output after receiving EOF on stdin.
//
// On macOS Docker Desktop (Unix socket proxy), closing the attach connection
// also closes the read direction, discarding any stdout the process emits after
// reading EOF. A 500ms delay ensures that stdout (e.g. a Python print() after
// stdin.read()) is forwarded through stdcopy.StdCopy before the connection tears
// down. 500ms is generous: Python's print() + flush is nearly instant.
const stdinEOFFlushDelay = 500 * time.Millisecond

// dockerSandbox is the live handle to a single running container sandbox.
// It implements the Sandbox interface.
type dockerSandbox struct {
	cli         *client.Client
	containerID string
	attachResp  types.HijackedResponse
	stdinW      *io.PipeWriter  // write end of the stdin pipe (pump goroutine reads it)
	stdoutR     *io.PipeReader
	stderrR     *io.PipeReader
	spec        wire.JobSpec
	startTime   time.Time
	cpuReader   CPUUsageFunc

	cleanupOnce sync.Once
}

// Stdin returns the write end of the container's stdin pipe. Writing here
// routes bytes to the container's stdin via the hijacked attach connection.
//
// The returned WriteCloser is always the same *io.PipeWriter that was created
// once in Create() alongside the pump goroutine. Callers must not call Stdin()
// more than once for write purposes; closeStdin() in the worker uses sync.Once
// to guarantee exactly one Close() call.
func (s *dockerSandbox) Stdin() io.WriteCloser {
	return s.stdinW
}

// Stdout returns the demuxed stdout reader. Bytes here are plain stdout from
// the container (Docker's stdcopy framing stripped by the demux goroutine).
func (s *dockerSandbox) Stdout() io.Reader {
	return s.stdoutR
}

// Stderr returns the demuxed stderr reader.
func (s *dockerSandbox) Stderr() io.Reader {
	return s.stderrR
}

// Wait blocks until the container exits or the context is cancelled, and
// returns the exit Result. It only handles the NORMAL-EXIT path (ContainerWait).
//
// The three clocks (wall, idle, CPU) are orchestrated by the session supervisor
// (internal/session.Run), which is called by the worker layer. That supervisor
// calls Kill/Cleanup on clock expiry, which terminates the container and causes
// this ContainerWait to unblock. The session supervisor then assembles the
// final Result from whichever clock fired.
//
// When the container exits normally before any clock fires, this Wait returns
// the ExitCode/Signal info so the session supervisor can produce a Result with
// the correct exit code and no timeout flags.
func (s *dockerSandbox) Wait(ctx context.Context) (Result, error) {
	statusCh, errCh := s.cli.ContainerWait(ctx, s.containerID, container.WaitConditionNotRunning)

	select {
	case waitResp := <-statusCh:
		exitCode := int(waitResp.StatusCode)
		result := Result{
			ExitCode:   &exitCode,
			DurationMs: int(time.Since(s.startTime).Milliseconds()),
		}
		return result, nil
	case err := <-errCh:
		if err != nil {
			// Context cancelled (Kill/Cleanup fired) or network error.
			// The session supervisor handles context-cancel termination; we
			// return a zero result here and the supervisor fills in the reason.
			if ctx.Err() != nil {
				return Result{DurationMs: int(time.Since(s.startTime).Milliseconds())}, nil
			}
			return Result{}, fmt.Errorf("docker: ContainerWait: %w", err)
		}
		return Result{DurationMs: int(time.Since(s.startTime).Milliseconds())}, nil
	case <-ctx.Done():
		return Result{DurationMs: int(time.Since(s.startTime).Milliseconds())}, nil
	}
}

// Kill destroys the ENTIRE container process tree: ContainerKill (SIGKILL)
// followed by ContainerRemove(force). Never kills a bare PID (RUN-04).
// Errors from ContainerKill are tolerated (container may already be stopped);
// errors from ContainerRemove are returned.
// RemoveVolumes=true removes the anonymous /workspace volume so no host-disk
// residue survives the job.
func (s *dockerSandbox) Kill(ctx context.Context) error {
	// ContainerKill sends SIGKILL; ignore not-found errors (already killed).
	_ = s.cli.ContainerKill(ctx, s.containerID, "KILL")
	return s.cli.ContainerRemove(ctx, s.containerID, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
}

// Cleanup releases all resources exactly once (idempotent via sync.Once).
// It closes the hijacked connection, the demuxed pipe readers, and
// force-removes the container. Not-found errors are silently ignored since
// Kill may have already removed it (LIFE-01/02, T-02-13).
func (s *dockerSandbox) Cleanup() error {
	var cleanupErr error
	s.cleanupOnce.Do(func() {
		// Close the stdin pipe writer to unblock the pump goroutine.
		// The pump goroutine will then close the attach connection after the
		// flush delay. If the connection is already closed (e.g. by Kill), the
		// pump goroutine's Close() call is a no-op.
		if s.stdinW != nil {
			_ = s.stdinW.Close()
		}

		// Close the hijacked attach connection immediately as well (the pump
		// goroutine may have already closed it; Close is idempotent on net.Conn).
		s.attachResp.Close()

		// Close pipe readers — unblocks any goroutines reading Stdout/Stderr.
		_ = s.stdoutR.Close()
		_ = s.stderrR.Close()

		// Force-remove container (and its anonymous /workspace volume).
		// RemoveVolumes=true removes the anonymous volume so no host-disk
		// residue survives the job. Ignore not-found: Kill may have removed it.
		removeErr := s.cli.ContainerRemove(context.Background(), s.containerID,
			container.RemoveOptions{Force: true, RemoveVolumes: true})
		if removeErr != nil && !isNotFound(removeErr) {
			cleanupErr = fmt.Errorf("docker: Cleanup ContainerRemove: %w", removeErr)
		}
	})
	return cleanupErr
}

// CPUReader returns the CPUUsageFunc for this sandbox. It is intended for the
// worker layer to pass to session.Run:
//
//	result, err := session.Run(ctx, sandbox, spec.Limits, sandbox.CPUReader())
//
// This accessor allows the worker to use the concrete *dockerSandbox without
// the session package needing to know about the Docker SDK.
func (s *dockerSandbox) CPUReader() CPUUsageFunc {
	return s.cpuReader
}

// Limits returns the job limits stored in the sandbox for use by the worker
// when calling session.Run.
func (s *dockerSandbox) Limits() wire.Limits {
	return s.spec.Limits
}

// isNotFound reports whether err is a Docker "not found" error (container
// already removed). Uses the containerd errdefs package exposed by the moby
// client errors.go.
func isNotFound(err error) bool {
	return client.IsErrNotFound(err)
}

