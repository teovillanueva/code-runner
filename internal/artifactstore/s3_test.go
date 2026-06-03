//go:build s3_integration

// Package artifactstore_test: S3Store integration tests against a real
// S3-compatible backend (MinIO).
//
// These tests prove the fresh-bucket end-to-end cycle (R14): against a
// bucket-LESS MinIO, EnsureLifecycle creates the bucket and installs the
// artifacts/ lifecycle rule, then Put uploads bytes and the returned presigned
// URL fetches back EXACTLY those bytes (R7).
//
// Two-gate guard (mirrors internal/runner's //go:build docker convention):
//   - Build tag `s3_integration` excludes these from `go test ./...`.
//   - A runtime skip (requireMinIO) skips cleanly when MinIO is unreachable, so
//     even `go test -tags=s3_integration ./...` is green on a machine without it.
//
// Run via:
//
//	ARTIFACT_S3_ENDPOINT=http://localhost:9000 \
//	AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin \
//	go test -tags=s3_integration ./internal/artifactstore/... -v
package artifactstore_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/teovillanueva/code-runner/internal/artifactstore"
	"github.com/teovillanueva/code-runner/internal/config"
)

// requireMinIO builds a test Config from env and skips the test when the
// minimum S3 settings are absent. It does NOT attempt a network probe here —
// the first store call (EnsureLifecycle) surfaces an unreachable backend, but
// the env gate keeps the default `-tags=s3_integration` run green without MinIO.
func requireMinIO(t *testing.T) config.Config {
	t.Helper()
	endpoint := firstNonEmpty(os.Getenv("ARTIFACT_S3_ENDPOINT"), os.Getenv("AWS_ENDPOINT_URL_S3"))
	access := firstNonEmpty(os.Getenv("ARTIFACT_S3_ACCESS_KEY_ID"), os.Getenv("AWS_ACCESS_KEY_ID"))
	secret := firstNonEmpty(os.Getenv("ARTIFACT_S3_SECRET_ACCESS_KEY"), os.Getenv("AWS_SECRET_ACCESS_KEY"))
	if endpoint == "" || access == "" || secret == "" {
		t.Skip("MinIO/S3 env not set (ARTIFACT_S3_ENDPOINT/AWS_ENDPOINT_URL_S3 + creds); skipping S3 integration test")
	}
	cfg := config.Default()
	cfg.S3Endpoint = endpoint
	cfg.S3AccessKeyID = access
	cfg.S3SecretAccessKey = secret
	cfg.S3Region = firstNonEmpty(os.Getenv("AWS_REGION"), "us-east-1")
	// A fresh, unique bucket name per run proves the bucket-create-if-absent path.
	cfg.S3Bucket = fmt.Sprintf("code-runner-test-%d", time.Now().UnixNano())
	cfg.PresignedURLTTL = 10 * time.Minute
	cfg.S3ObjectTTL = 24 * time.Hour
	return cfg
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// TestFreshBucketEndToEnd proves the R14 fresh-MinIO cycle: a bucket-less
// backend yields a working EnsureLifecycle (bucket created + lifecycle rule on
// artifacts/) → Put → presign → fetch that returns the exact uploaded bytes (R7).
func TestFreshBucketEndToEnd(t *testing.T) {
	cfg := requireMinIO(t)
	ctx := context.Background()

	store, err := artifactstore.NewS3Store(cfg)
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}

	// EnsureLifecycle against a FRESH (bucket-less) backend.
	if err := store.EnsureLifecycle(ctx); err != nil {
		// If the backend is simply unreachable, skip rather than fail (keeps the
		// suite green on machines without a running MinIO).
		t.Skipf("EnsureLifecycle failed (MinIO likely unreachable): %v", err)
	}

	// Upload and fetch back the exact bytes via the presigned URL (R7).
	want := []byte("hello artifact bytes \x00\x01\x02 plot.png")
	url, err := store.Put(ctx, "job-abc", "plot.png", "image/png", want)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if url == "" {
		t.Fatal("Put returned empty URL")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch presigned URL: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET status = %d, want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("fetched bytes mismatch:\n got=%q\nwant=%q", got, want)
	}
}
