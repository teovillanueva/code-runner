// Package blobstore defines the BlobStore interface: the content-addressed (CAS)
// object store that code-runner OWNS. Blob objects are keyed purely by their
// sha256 content hash under the blobs/cas/<hash> prefix — distinct from the
// artifacts/ prefix used by internal/artifactstore — so blobs and artifacts can
// safely share one bucket.
//
// Trust boundary (Phase 16): the WORKER is the only party that reads blob bytes,
// and it is the AUTHORITATIVE sha256 verifier. The worker pulls ONLY from this
// configured store at a known endpoint — never from a client-supplied URL — so
// there is no SSRF surface. The store hands back a streaming io.ReadCloser
// (NOT []byte) precisely so the worker can hash-while-copying into a disk
// staging file without ever buffering a whole large blob in RAM (RAM-bounded).
//
// The interface is the swap seam (mirroring artifactstore.ArtifactStore,
// runner.Runner, stdintransport.StdinTransport): only S3Store ships today.
// It is deliberately SDK-agnostic — the only object-store type that leaks is
// io.ReadCloser from Get, which is a stdlib type.
package blobstore

import (
	"context"
	"io"
)

// BlobStore stores and retrieves content-addressed blob bytes. The `hash`
// argument to every method is the FULL "sha256:<64hex>" ref string (the same
// token carried in FileInput.ref and used as the Redis liveness key), so one
// token addresses the blob end to end.
type BlobStore interface {
	// Get opens a streaming read of the blob at blobs/cas/<hash>. The caller MUST
	// Close the returned reader. The worker copies from it into a disk staging
	// file while feeding a sha256 hasher, so the whole blob never lives in RAM.
	// A missing object yields a non-nil error (see IsNotFound).
	Get(ctx context.Context, hash string) (io.ReadCloser, error)

	// Stat reports whether the blob exists and, if so, its size in bytes. A
	// missing object returns (false, 0, nil) — absence is not an error for Stat
	// (the API's existence check relies on this). A transport/permission failure
	// returns a non-nil error.
	Stat(ctx context.Context, hash string) (exists bool, size int64, err error)

	// Remove deletes the blob object (GC). Removing an already-absent object is
	// not an error (idempotent — GC may race another replica).
	Remove(ctx context.Context, hash string) error

	// EnsureBucket makes the backing bucket EXIST at boot (creating it if absent),
	// exactly like artifactstore.EnsureLifecycle's bucket-create step, so a fresh
	// MinIO/R2/Tigris works end to end with no external `mc mb`. Unlike the
	// artifact store, blobs install NO provider lifecycle rule: blob expiry is
	// driven by the Redis-liveness GC (BLOB-08), not by an S3 lifecycle clock.
	EnsureBucket(ctx context.Context) error
}
