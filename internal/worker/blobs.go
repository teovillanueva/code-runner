package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// BlobStore is the subset of internal/blobstore.BlobStore the worker needs to
// pull blob bytes. Declared here (not imported as the concrete type) so tests
// can inject an in-memory fake without S3 — mirroring the Transport interface
// pattern. The worker NEVER reads from a client-supplied URL: hash addresses a
// blob in code-runner's OWN configured store, so there is no SSRF surface.
type BlobStore interface {
	// Get opens a streaming read of the blob; the caller Closes it. A store-miss
	// surfaces as an error (here or on first Read), which the worker maps to a
	// clean job failure.
	Get(ctx context.Context, hash string) (io.ReadCloser, error)
}

// BlobIndex is the subset of internal/blobindex.Index the worker needs for the
// lease lifecycle + liveness touch. *blobindex.Index satisfies it.
type BlobIndex interface {
	Lease(ctx context.Context, hash, jobID string, size int64, ttl time.Duration) error
	Release(ctx context.Context, hash, jobID string) error
	Touch(ctx context.Context, hash string, size int64, ttl time.Duration) error
}

// errBlobVerify is the sentinel wrapping every blob-resolution failure
// (store-miss, sha256 mismatch, bad ref). The worker maps it to a clean job
// error with NO partial run and NO leak.
var errBlobVerify = errors.New("blob resolution failed")

// refToHexHash validates a "sha256:<64hex>" ref and returns the bare 64-hex
// digest. The worker re-validates the ref shape regardless of API validation
// (the worker never trusts the API): a malformed ref is a resolution failure,
// not a panic.
func refToHexHash(ref string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("%w: ref %q missing sha256: prefix", errBlobVerify, ref)
	}
	hexPart := ref[len(prefix):]
	if len(hexPart) != 64 {
		return "", fmt.Errorf("%w: ref %q digest is not 64 hex chars", errBlobVerify, ref)
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", fmt.Errorf("%w: ref %q digest is not valid hex: %v", errBlobVerify, ref, err)
	}
	return hexPart, nil
}

// resolveBlobRefs converts every FileInput carrying a Ref into an ordinary
// inline (base64) FileInput, so BOTH the DockerSocketRunner and the zygote relay
// see refs as normal workspace files — refs become files BEFORE the runner is
// invoked. For each ref it:
//
//  1. Validates the ref shape and the workspace path (host-escape guard).
//  2. Leases the blob for jobID (SADD + liveness touch) so GC cannot reclaim it
//     mid-run. Every leased hash is recorded in `leased` so the caller releases
//     them on EVERY terminal path (idempotent with the once-only teardown).
//  3. Streams the blob from the store into a DISK staging file while feeding a
//     sha256 hasher — the whole blob never lives in RAM during the verify gate.
//  4. Verifies the computed digest == the ref. A store-miss or a mismatch fails
//     the job cleanly (no partial run, no leak) — the ref's bytes are discarded.
//  5. Materializes the verified bytes as inline base64 content on the FileInput
//     (Ref cleared), so the existing runner file path writes it to the workspace.
//
// It returns a COPY of spec with Files rewritten, the set of leased hashes (for
// release on terminal), and an error on the first resolution failure. On error
// the caller must still release any hashes already in `leased`.
//
// A spec with no refs is returned unchanged (inline-only callers are unaffected).
func (w *Worker) resolveBlobRefs(ctx context.Context, spec wire.JobSpec) (wire.JobSpec, []string, error) {
	// Fast path: nothing to resolve. Avoid copying for the common inline case.
	hasRef := false
	for i := range spec.Files {
		if spec.Files[i].Ref != nil && *spec.Files[i].Ref != "" {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return spec, nil, nil
	}

	// Only now (we have real blob work) open the resolve span — an inline-only
	// job never reaches here, so it emits no extra span (OBS-03 phase-span set).
	var resolveSpan trace.Span
	ctx, resolveSpan = tracer().Start(ctx, "blobs.resolve")
	defer resolveSpan.End()

	// A blob store + index are REQUIRED to resolve a ref. If a ref arrives but the
	// worker has no blob store configured, fail cleanly rather than silently
	// dropping the file.
	if w.cfg.BlobStore == nil || w.cfg.BlobIndex == nil {
		return spec, nil, fmt.Errorf("%w: job references a blob but the worker has no blob store configured", errBlobVerify)
	}

	ttl := w.cfg.BlobIdleTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	// Copy Files so we never mutate the caller's slice (the spec is read from
	// Redis and may be reused/logged).
	files := make([]wire.FileInput, len(spec.Files))
	copy(files, spec.Files)

	leased := make([]string, 0, len(files))

	for i := range files {
		f := files[i]
		if f.Ref == nil || *f.Ref == "" {
			continue // inline file — untouched.
		}
		ref := *f.Ref

		// A file must carry EXACTLY ONE of content/ref. The worker enforces this
		// regardless of API validation (the worker never trusts the API).
		if f.Content != nil {
			return spec, leased, fmt.Errorf("%w: file %q has BOTH inline content and a ref", errBlobVerify, f.Name)
		}

		// Validate ref shape + workspace path before any store I/O.
		if _, err := refToHexHash(ref); err != nil {
			return spec, leased, err
		}
		if _, err := runner.SanitizeWorkspacePath(f.Name); err != nil {
			return spec, leased, fmt.Errorf("%w: file %q has an unsafe path: %v", errBlobVerify, f.Name, err)
		}

		// Lease BEFORE the pull so GC cannot reclaim the blob between our existence
		// check and our read. Record it for release-on-terminal even if the pull
		// then fails.
		if err := w.cfg.BlobIndex.Lease(ctx, ref, spec.JobId, 0, ttl); err != nil {
			return spec, leased, fmt.Errorf("%w: lease %q: %v", errBlobVerify, ref, err)
		}
		leased = append(leased, ref)

		// Stream the blob into a disk staging file while hashing (RAM-bounded).
		data, err := w.pullAndVerify(ctx, ref, f.Name)
		if err != nil {
			return spec, leased, err
		}

		// Touch liveness with the now-known size (monotonic — never shrinks).
		if tErr := w.cfg.BlobIndex.Touch(ctx, ref, int64(len(data)), ttl); tErr != nil {
			// Non-fatal: the lease already pins the blob for this run. Log via the
			// caller's terminal handling; do not fail the job on a touch error.
			_ = tErr
		}

		// Materialize as an ordinary inline base64 file: Ref cleared, Content set.
		// Both runners now treat it as a normal binary input.
		enc := base64.StdEncoding.EncodeToString(data)
		files[i].Content = &enc
		files[i].Encoding = wire.FileInputEncodingBase64
		files[i].Ref = nil
	}

	out := spec
	out.Files = files
	return out, leased, nil
}

// pullAndVerify streams the blob from the store into a temp staging file on disk
// while feeding a sha256 hasher, verifies digest == ref, and returns the
// verified bytes. The blob is never held whole in RAM during the verify gate
// (it is streamed through io.Copy into the file + hasher). The staging file is
// removed before returning. A store-miss or digest mismatch is a clean failure.
//
// NOTE (RAM bound): the verified bytes ARE read back into memory at the end so
// they can be handed to the existing runner file-materialization path (which is
// []byte-based, like every inline file). The security-critical VERIFY step is
// RAM-bounded by streaming-to-disk; end-to-end streaming into the container tar
// is a future optimization tracked for plan 02+. /workspace and the staging dir
// are disk-backed.
func (w *Worker) pullAndVerify(ctx context.Context, ref, name string) ([]byte, error) {
	wantHex, err := refToHexHash(ref)
	if err != nil {
		return nil, err
	}

	rc, err := w.cfg.BlobStore.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("%w: store-miss for %q (%s): %v", errBlobVerify, name, ref, err)
	}
	defer rc.Close() //nolint:errcheck

	staging, err := os.CreateTemp("", "cr-blob-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create staging file: %v", errBlobVerify, err)
	}
	stagingPath := staging.Name()
	defer os.Remove(stagingPath) //nolint:errcheck

	hasher := sha256.New()
	// Tee the stream into the hasher as it is copied to disk — the whole blob is
	// never buffered in memory during this copy.
	if _, err := io.Copy(io.MultiWriter(staging, hasher), rc); err != nil {
		staging.Close() //nolint:errcheck
		return nil, fmt.Errorf("%w: stream %q (%s): %v", errBlobVerify, name, ref, err)
	}
	if err := staging.Close(); err != nil {
		return nil, fmt.Errorf("%w: close staging file: %v", errBlobVerify, err)
	}

	gotHex := hex.EncodeToString(hasher.Sum(nil))
	if gotHex != wantHex {
		// Tamper / corruption gate: discard the bytes, fail the job cleanly.
		return nil, fmt.Errorf("%w: sha256 mismatch for %q: got sha256:%s, want %s", errBlobVerify, name, gotHex, ref)
	}

	// Verified — read the staging file back for materialization.
	data, err := os.ReadFile(stagingPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read verified staging file: %v", errBlobVerify, err)
	}
	return data, nil
}

// releaseLeases releases every leased blob hash for jobID (idempotent). It is
// called on EVERY terminal path inside the once-only teardown, AND on the
// early-return resolution-failure path. Errors are best-effort (logged by the
// caller) — a release failure must never block teardown.
func (w *Worker) releaseLeases(ctx context.Context, leased []string, jobID string) {
	if w.cfg.BlobIndex == nil {
		return
	}
	for _, ref := range leased {
		_ = w.cfg.BlobIndex.Release(ctx, ref, jobID)
	}
}
