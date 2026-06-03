package artifactstore

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"

	"github.com/teovillanueva/code-runner/internal/config"
)

// artifactKeyPrefix is the key prefix under which every artifact is stored
// (D-03). Object keys are artifacts/<jobID>/<name>. The boot-time lifecycle
// expiration rule (R15) is scoped to this prefix.
const artifactKeyPrefix = "artifacts/"

// lifecycleRuleID identifies the single expiration rule installed by
// EnsureLifecycle so a re-run replaces (rather than duplicates) it.
const lifecycleRuleID = "code-runner-artifacts-expiry"

// S3Store is the shipped ArtifactStore backed by an S3-compatible object store
// via minio-go. It is constructed from config.Config and reads NO env directly
// (the artifactstore package never reads environment variables — all settings
// ride on Config, established by configFromEnv in apps/worker/main.go).
type S3Store struct {
	cli          *minio.Client
	bucket       string
	region       string
	presignedTTL time.Duration
	objectTTL    time.Duration
}

// Compile-time assertion (docker.go line 121 idiom): S3Store satisfies the
// ArtifactStore swap-seam interface declared in store.go.
var _ ArtifactStore = (*S3Store)(nil)

// NewS3Store builds an S3Store from cfg. Secure (TLS) is derived from whether
// the endpoint is an https:// URL — the bare host:port form passed to minio.New
// must NOT carry the scheme, so the scheme is stripped after deciding Secure.
//
// It fails closed: a malformed endpoint or client construction error returns a
// non-nil error (the caller treats a present-but-misconfigured S3 as a boot
// error). Credentials come only from cfg and are never logged (threat T-09-05).
func NewS3Store(cfg config.Config) (*S3Store, error) {
	endpoint := cfg.S3Endpoint
	secure := strings.HasPrefix(strings.ToLower(endpoint), "https://")
	// minio.New wants a bare host[:port], not a scheme-qualified URL.
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimSuffix(endpoint, "/")
	if endpoint == "" {
		return nil, fmt.Errorf("artifactstore: empty S3 endpoint")
	}

	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, ""),
		Secure: secure,
		Region: cfg.S3Region,
	})
	if err != nil {
		return nil, fmt.Errorf("artifactstore: create S3 client for %q: %w", endpoint, err)
	}

	return &S3Store{
		cli:          cli,
		bucket:       cfg.S3Bucket,
		region:       cfg.S3Region,
		presignedTTL: cfg.PresignedURLTTL,
		objectTTL:    cfg.S3ObjectTTL,
	}, nil
}

// Put uploads data under artifacts/<jobID>/<name> and returns a presigned GET
// URL that resolves to exactly those bytes for s.presignedTTL (R7).
func (s *S3Store) Put(ctx context.Context, jobID, name, mimeType string, data []byte) (string, error) {
	key := artifactKeyPrefix + jobID + "/" + name

	if _, err := s.cli.PutObject(
		ctx, s.bucket, key,
		bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: mimeType},
	); err != nil {
		return "", fmt.Errorf("artifactstore: put %q: %w", key, err)
	}

	u, err := s.cli.PresignedGetObject(ctx, s.bucket, key, s.presignedTTL, nil)
	if err != nil {
		return "", fmt.Errorf("artifactstore: presign %q: %w", key, err)
	}
	return u.String(), nil
}

// EnsureLifecycle makes the bucket exist (creating it if absent — R14) and then
// installs a provider-side lifecycle expiration rule on the artifacts/ prefix
// matching the configured object TTL (R15).
//
// Bucket creation is the store's responsibility, NOT infra's: a fresh
// MinIO/R2/Tigris with no pre-created bucket yields a working
// upload→presign→fetch cycle after this call.
func (s *S3Store) EnsureLifecycle(ctx context.Context) error {
	// 1) Make the bucket exist (R14 — fresh-backend bootstrap).
	exists, err := s.cli.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("artifactstore: bucket-exists %q: %w", s.bucket, err)
	}
	if !exists {
		if err := s.cli.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
			return fmt.Errorf("artifactstore: make-bucket %q: %w", s.bucket, err)
		}
	}

	// 2) Install the lifecycle expiration rule on the artifacts/ prefix (R15).
	// S3 lifecycle granularity is whole days, so round the object TTL UP to at
	// least 1 day (R15 caveat).
	days := lifecycleDays(s.objectTTL)
	lc := lifecycle.NewConfiguration()
	lc.Rules = []lifecycle.Rule{
		{
			ID:     lifecycleRuleID,
			Status: "Enabled",
			// Use ONLY the modern RuleFilter.Prefix. Setting both the legacy
			// top-level Prefix AND RuleFilter.Prefix makes minio-go emit both
			// <Prefix> and <Filter><Prefix> in one rule, which the S3 lifecycle
			// XML schema forbids — MinIO/AWS reject it as "not well-formed or did
			// not validate against our published schema".
			RuleFilter: lifecycle.Filter{Prefix: artifactKeyPrefix},
			Expiration: lifecycle.Expiration{Days: lifecycle.ExpirationDays(days)},
		},
	}
	if err := s.cli.SetBucketLifecycle(ctx, s.bucket, lc); err != nil {
		return fmt.Errorf("artifactstore: set-lifecycle %q: %w", s.bucket, err)
	}
	return nil
}

// lifecycleDays converts a duration to whole days for an S3 lifecycle
// expiration rule, rounding UP and clamping to a minimum of 1 (R15: sub-day
// cleanup is not achievable via lifecycle).
func lifecycleDays(d time.Duration) int {
	days := int(math.Ceil(d.Hours() / 24.0))
	if days < 1 {
		days = 1
	}
	return days
}
