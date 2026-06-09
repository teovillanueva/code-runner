package blobstore

import "github.com/teovillanueva/code-runner/internal/config"

// minimalCfg returns a Config with just enough set to construct an S3Store
// without a network call (NewS3Store does no network I/O). Used by the pure unit
// tests; the s3_integration tests build their own env-driven config.
func minimalCfg() config.Config {
	cfg := config.Default()
	cfg.S3Endpoint = "http://minio:9000"
	cfg.S3AccessKeyID = "test"
	cfg.S3SecretAccessKey = "test"
	cfg.S3Region = "us-east-1"
	cfg.S3Bucket = "artifacts-bucket"
	cfg.BlobS3Bucket = "blobs-bucket"
	return cfg
}
