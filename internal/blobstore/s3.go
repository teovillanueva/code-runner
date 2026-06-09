package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/teovillanueva/code-runner/internal/config"
)

// blobKeyPrefix is the key prefix under which every content-addressed blob is
// stored. Object keys are blobs/cas/<hash> where <hash> is the full
// "sha256:<64hex>" ref. This is intentionally distinct from artifactstore's
// "artifacts/" prefix so blobs and artifacts can share one bucket.
const blobKeyPrefix = "blobs/cas/"

// S3Store is the shipped BlobStore backed by an S3-compatible object store via
// minio-go. It is constructed from config.Config and reads NO env directly (all
// settings ride on Config, established by configFromEnv in apps/worker/main.go).
//
// NOTE on endpoints: the worker only ever GETs/Stats/Removes blobs over the
// INTERNAL endpoint — there is no split-horizon presign client here. Presigned
// PUT URLs (the client→store upload path) are signed by the API (TS) against the
// public endpoint; the Go worker never presigns blob URLs.
type S3Store struct {
	cli    *minio.Client
	bucket string
	region string
}

// Compile-time assertion: S3Store satisfies the BlobStore swap-seam interface.
var _ BlobStore = (*S3Store)(nil)

// NewS3Store builds an S3Store from cfg. It mirrors artifactstore.NewS3Store's
// endpoint handling (scheme decides Secure; the bare host:port is passed to
// minio.New). It uses the dedicated blob bucket (cfg.BlobS3Bucket, which the
// config layer defaults to the artifact bucket when no override is set), so
// blobs and artifacts can share a bucket via distinct prefixes or be split via
// BLOB_S3_BUCKET.
//
// It fails closed: a malformed endpoint or client construction error returns a
// non-nil error. Credentials come only from cfg and are never logged.
func NewS3Store(cfg config.Config) (*S3Store, error) {
	endpoint := cfg.S3Endpoint
	secure := strings.HasPrefix(strings.ToLower(endpoint), "https://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimSuffix(endpoint, "/")
	if endpoint == "" {
		return nil, fmt.Errorf("blobstore: empty S3 endpoint")
	}

	bucket := cfg.BlobS3Bucket
	if bucket == "" {
		// Defensive: the config layer already defaults this to the artifact
		// bucket, but never construct a store with an empty bucket name.
		bucket = cfg.S3Bucket
	}
	if bucket == "" {
		return nil, fmt.Errorf("blobstore: empty S3 bucket")
	}

	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, ""),
		Secure: secure,
		Region: cfg.S3Region,
	})
	if err != nil {
		return nil, fmt.Errorf("blobstore: create S3 client for %q: %w", endpoint, err)
	}

	return &S3Store{
		cli:    cli,
		bucket: bucket,
		region: cfg.S3Region,
	}, nil
}

// objectKey derives the full object key for a blob hash. The hash is the entire
// "sha256:<64hex>" ref; it is embedded verbatim so the S3 key, the FileInput.ref
// and the Redis liveness key are all the same token.
func objectKey(hash string) string {
	return blobKeyPrefix + hash
}

// Get opens a streaming read of the blob. minio's GetObject is lazy — it does
// not hit the network until the first Read — so callers get an io.ReadCloser
// they stream from (and MUST Close). A missing object surfaces on the first
// Read, not here; the worker's hash-while-copy loop treats that as a clean
// store-miss failure.
func (s *S3Store) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	obj, err := s.cli.GetObject(ctx, s.bucket, objectKey(hash), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("blobstore: get %q: %w", objectKey(hash), err)
	}
	return obj, nil
}

// Stat reports existence + size. A "not found" / "no such key" from the backend
// is mapped to (false, 0, nil) — absence is a normal answer for the existence
// check, not an error. Any other error (auth, transport) is returned.
func (s *S3Store) Stat(ctx context.Context, hash string) (bool, int64, error) {
	info, err := s.cli.StatObject(ctx, s.bucket, objectKey(hash), minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("blobstore: stat %q: %w", objectKey(hash), err)
	}
	return true, info.Size, nil
}

// Remove deletes the blob object (GC). Removing an already-absent object is not
// an error (idempotent across racing GC sweeps on different replicas).
func (s *S3Store) Remove(ctx context.Context, hash string) error {
	if err := s.cli.RemoveObject(ctx, s.bucket, objectKey(hash), minio.RemoveObjectOptions{}); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("blobstore: remove %q: %w", objectKey(hash), err)
	}
	return nil
}

// EnsureBucket makes the bucket exist (creating it if absent). It installs NO
// lifecycle rule — blob expiry is governed by the Redis-liveness GC, not an S3
// lifecycle clock. Best-effort from the worker's perspective (the caller logs a
// failure but boots anyway, exactly like artifactstore.EnsureLifecycle).
func (s *S3Store) EnsureBucket(ctx context.Context) error {
	exists, err := s.cli.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("blobstore: bucket-exists %q: %w", s.bucket, err)
	}
	if !exists {
		if err := s.cli.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
			return fmt.Errorf("blobstore: make-bucket %q: %w", s.bucket, err)
		}
	}
	return nil
}

// isNotFound reports whether err is a MinIO/S3 "object does not exist" error
// (NoSuchKey / 404). minio-go surfaces these as minio.ErrorResponse; we match on
// the canonical codes rather than string-sniffing.
func isNotFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" || resp.StatusCode == 404
	}
	return false
}
