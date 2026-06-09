//go:build s3_integration

// Package blobstore integration tests against a real S3-compatible backend
// (MinIO). They prove the fresh-bucket CAS round-trip: EnsureBucket creates the
// bucket, Stat reports absence then presence, a PUT (via the minio client) lands
// under blobs/cas/<hash>, Get streams the exact bytes back, and Remove deletes.
//
// Two-gate guard (mirrors internal/artifactstore/s3_test.go):
//   - Build tag `s3_integration` excludes these from `go test ./...`.
//   - A runtime skip (requireMinIO) skips cleanly when MinIO is unreachable.
//
// Run via:
//
//	ARTIFACT_S3_ENDPOINT=http://localhost:9000 \
//	AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin \
//	go test -tags=s3_integration ./internal/blobstore/... -v
package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/teovillanueva/code-runner/internal/config"
)

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func requireMinIO(t *testing.T) config.Config {
	t.Helper()
	endpoint := firstNonEmpty(os.Getenv("ARTIFACT_S3_ENDPOINT"), os.Getenv("AWS_ENDPOINT_URL_S3"))
	access := firstNonEmpty(os.Getenv("ARTIFACT_S3_ACCESS_KEY_ID"), os.Getenv("AWS_ACCESS_KEY_ID"))
	secret := firstNonEmpty(os.Getenv("ARTIFACT_S3_SECRET_ACCESS_KEY"), os.Getenv("AWS_SECRET_ACCESS_KEY"))
	if endpoint == "" || access == "" || secret == "" {
		t.Skip("MinIO/S3 env not set (ARTIFACT_S3_ENDPOINT/AWS_ENDPOINT_URL_S3 + creds); skipping blob S3 integration test")
	}
	cfg := config.Default()
	cfg.S3Endpoint = endpoint
	cfg.S3AccessKeyID = access
	cfg.S3SecretAccessKey = secret
	cfg.S3Region = firstNonEmpty(os.Getenv("AWS_REGION"), "us-east-1")
	cfg.S3Bucket = fmt.Sprintf("code-runner-blob-test-%d", time.Now().UnixNano())
	cfg.BlobS3Bucket = cfg.S3Bucket
	return cfg
}

// sha256Ref computes the "sha256:<hex>" ref for data.
func sha256Ref(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestBlobRoundTrip proves the fresh-bucket CAS cycle: EnsureBucket → Stat
// (absent) → put bytes under blobs/cas/<hash> → Stat (present, size) → Get
// streams the exact bytes → Remove → Stat (absent again).
func TestBlobRoundTrip(t *testing.T) {
	cfg := requireMinIO(t)
	ctx := context.Background()

	store, err := NewS3Store(cfg)
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	if err := store.EnsureBucket(ctx); err != nil {
		t.Skipf("EnsureBucket failed (MinIO likely unreachable): %v", err)
	}

	data := []byte("content-addressed blob bytes \x00\x01\x02 big.csv")
	hash := sha256Ref(data)

	// Absent before upload.
	exists, _, err := store.Stat(ctx, hash)
	if err != nil {
		t.Fatalf("Stat (pre): %v", err)
	}
	if exists {
		t.Fatal("Stat (pre): blob exists before upload")
	}

	// Upload under the blobs/cas/<hash> key directly via the minio client (the
	// real upload path is a client→store presigned PUT signed by the API; here we
	// just need the bytes present so the worker-side Get/Stat/Remove are exercised).
	if _, err := store.cli.PutObject(ctx, store.bucket, objectKey(hash),
		bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// Present with correct size.
	exists, size, err := store.Stat(ctx, hash)
	if err != nil {
		t.Fatalf("Stat (post): %v", err)
	}
	if !exists {
		t.Fatal("Stat (post): blob missing after upload")
	}
	if size != int64(len(data)) {
		t.Fatalf("Stat size = %d; want %d", size, len(data))
	}

	// Get streams the exact bytes.
	rc, err := store.Get(ctx, hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close() //nolint:errcheck
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("blob bytes mismatch:\n got=%q\nwant=%q", got, data)
	}

	// Remove, then absent again. Remove is idempotent.
	if err := store.Remove(ctx, hash); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := store.Remove(ctx, hash); err != nil {
		t.Fatalf("Remove (idempotent re-delete): %v", err)
	}
	exists, _, err = store.Stat(ctx, hash)
	if err != nil {
		t.Fatalf("Stat (after remove): %v", err)
	}
	if exists {
		t.Fatal("Stat (after remove): blob still present")
	}
}
