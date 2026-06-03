// Package artifactstore defines the ArtifactStore interface that abstracts how
// captured artifact bytes are stored and exposed for retrieval.
//
// Shipped implementation: S3Store, which uploads each artifact to an
// S3-compatible object store under the artifacts/<jobId>/ key prefix and returns
// a presigned GET URL (default 24h expiry). It works unchanged against MinIO,
// Cloudflare R2, and Fly Tigris.
//
// The interface is the swap seam (mirroring internal/runner.Runner and
// internal/stdintransport.StdinTransport): only S3Store ships today, but the
// abstraction is preserved so a future backend can be slotted in behind it
// without changing any caller. This swap-ability is the sole reason the
// interface exists (D-02).
//
// The interface is deliberately SDK-agnostic — it accepts []byte and returns
// string URLs, so no object-store SDK type leaks through. Callers (the worker
// teardown path) depend only on this package, never on the S3 client SDK.
package artifactstore

import "context"

// ArtifactStore stores artifact bytes and returns a fetchable URL. A nil
// ArtifactStore on the worker means artifact capture is disabled (D-04): output
// pull (stdout/stderr from Redis) still works, collected jobs just return zero
// artifacts.
type ArtifactStore interface {
	// Put uploads data for the given job under the artifacts/<jobID>/<name> key
	// with the provided MIME type, and returns a presigned GET URL that resolves
	// to exactly those bytes until the configured presigned-URL expiry (R7).
	//
	// The returned URL is unauthenticated: anyone holding it can fetch the
	// object until it expires. URL longevity is bounded by the env-managed
	// presigned-URL TTL, and the object lifecycle TTL is enforced to be >= the
	// URL TTL (threat T-09-04) so a live URL never points at a deleted object.
	Put(ctx context.Context, jobID, name, mimeType string, data []byte) (url string, err error)

	// EnsureLifecycle prepares the backend at boot. It is responsible for making
	// the bucket EXIST — implementations MUST create the bucket if it is absent —
	// so a fresh backend (e.g. a brand-new MinIO/R2/Tigris) works end-to-end with
	// NO external bucket-creation step (R14). Callers/infra need not run `mc mb`
	// or equivalent. After the bucket exists, EnsureLifecycle installs a
	// provider-side lifecycle expiration rule on the artifacts/ prefix matching
	// the configured object TTL, so aged objects are deleted by the provider with
	// no per-object code — code-runner stays stateless (R15).
	//
	// EnsureLifecycle is best-effort from the worker's perspective: a failure is
	// logged but does not block the worker boot (the lifecycle rule is a cleanup
	// optimization, not a correctness requirement for uploads).
	EnsureLifecycle(ctx context.Context) error
}

// Compile-time assertion (docker.go line 121 idiom): the shipped S3Store must
// satisfy ArtifactStore. The actual `var _ ArtifactStore = (*S3Store)(nil)`
// lives in s3.go, next to the implementation it guards.
