// Package blobstore unit tests: pure (no network) checks of key derivation and
// construction guards. The live S3 round-trip lives in s3_integration_test.go
// behind the s3_integration build tag (mirroring internal/artifactstore).
package blobstore

import (
	"strings"
	"testing"
)

// TestObjectKey pins the blobs/cas/<hash> layout: the full sha256:<hex> ref is
// embedded verbatim, distinct from the artifacts/ prefix so blobs and artifacts
// can share one bucket.
func TestObjectKey(t *testing.T) {
	h := "sha256:" + strings.Repeat("a", 64)
	got := objectKey(h)
	want := "blobs/cas/" + h
	if got != want {
		t.Fatalf("objectKey(%q) = %q; want %q", h, got, want)
	}
	if !strings.HasPrefix(got, "blobs/cas/") {
		t.Fatalf("object key %q is not under the blobs/cas/ prefix", got)
	}
	if strings.HasPrefix(got, "artifacts/") {
		t.Fatalf("object key %q must not collide with the artifacts/ prefix", got)
	}
}

// TestNewS3StoreEmptyEndpoint fails closed on a blank endpoint.
func TestNewS3StoreEmptyEndpoint(t *testing.T) {
	cfg := minimalCfg()
	cfg.S3Endpoint = ""
	if _, err := NewS3Store(cfg); err == nil {
		t.Fatal("NewS3Store with empty endpoint: want error, got nil")
	}
}

// TestNewS3StoreEmptyBucket fails closed when neither blob nor artifact bucket
// is set (a misconfigured store must not be constructed).
func TestNewS3StoreEmptyBucket(t *testing.T) {
	cfg := minimalCfg()
	cfg.BlobS3Bucket = ""
	cfg.S3Bucket = ""
	if _, err := NewS3Store(cfg); err == nil {
		t.Fatal("NewS3Store with empty bucket: want error, got nil")
	}
}

// TestNewS3StoreFallsBackToArtifactBucket: an empty BlobS3Bucket falls back to
// S3Bucket so blobs and artifacts share one bucket by default.
func TestNewS3StoreFallsBackToArtifactBucket(t *testing.T) {
	cfg := minimalCfg()
	cfg.BlobS3Bucket = ""
	cfg.S3Bucket = "shared-bucket"
	s, err := NewS3Store(cfg)
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	if s.bucket != "shared-bucket" {
		t.Fatalf("bucket = %q; want shared-bucket (fallback to artifact bucket)", s.bucket)
	}
}
