package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// fakeBlobStore is an in-memory BlobStore for unit tests. hash → bytes.
type fakeBlobStore struct {
	blobs map[string][]byte
}

func (f *fakeBlobStore) Get(_ context.Context, hash string) (io.ReadCloser, error) {
	b, ok := f.blobs[hash]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// fakeBlobIndex records lease/release calls so tests can assert the lifecycle.
type fakeBlobIndex struct {
	mu       sync.Mutex
	leased   map[string]int // ref → active lease count for the job under test
	leaseErr error
}

func newFakeIndex() *fakeBlobIndex { return &fakeBlobIndex{leased: map[string]int{}} }

func (f *fakeBlobIndex) Lease(_ context.Context, hash, _ string, _ int64, _ time.Duration) error {
	if f.leaseErr != nil {
		return f.leaseErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leased[hash]++
	return nil
}

func (f *fakeBlobIndex) Release(_ context.Context, hash, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leased[hash] > 0 {
		f.leased[hash]--
	}
	return nil
}

func (f *fakeBlobIndex) Touch(_ context.Context, _ string, _ int64, _ time.Duration) error {
	return nil
}

func (f *fakeBlobIndex) activeLeases() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.leased {
		n += c
	}
	return n
}

func refFor(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// newBlobWorker builds a minimal Worker wired with the given fake blob store +
// index, no real Redis/Docker/transport — enough to exercise resolveBlobRefs.
func newBlobWorker(store BlobStore, idx BlobIndex) *Worker {
	return &Worker{
		cfg: Config{
			BlobStore:   store,
			BlobIndex:   idx,
			BlobIdleTTL: time.Hour,
		},
	}
}

// TestResolveBlobRefs_MatchInlinesAndLeases: a ref whose bytes hash to the ref
// resolves to an inline base64 file (Ref cleared) and is leased.
func TestResolveBlobRefs_MatchInlinesAndLeases(t *testing.T) {
	data := []byte("the big shared input file\n\x00\x01")
	ref := refFor(data)
	store := &fakeBlobStore{blobs: map[string][]byte{ref: data}}
	idx := newFakeIndex()
	w := newBlobWorker(store, idx)

	spec := wire.JobSpec{
		JobId: "job-1",
		Files: []wire.FileInput{
			{Name: "main.py", Content: wire.Ptr("print(1)")},
			{Name: "data/big.bin", Ref: wire.Ptr(ref)},
		},
	}

	out, leased, err := w.resolveBlobRefs(context.Background(), spec)
	if err != nil {
		t.Fatalf("resolveBlobRefs: %v", err)
	}
	if len(leased) != 1 || leased[0] != ref {
		t.Fatalf("leased = %v; want [%s]", leased, ref)
	}
	if idx.activeLeases() != 1 {
		t.Fatalf("active leases = %d; want 1", idx.activeLeases())
	}

	// Inline file untouched.
	if out.Files[0].Ref != nil || out.Files[0].Content == nil || *out.Files[0].Content != "print(1)" {
		t.Fatalf("inline file mutated: %+v", out.Files[0])
	}
	// Ref file: Ref cleared, Content = base64(data), encoding base64.
	rf := out.Files[1]
	if rf.Ref != nil {
		t.Fatalf("resolved file still has a ref: %v", *rf.Ref)
	}
	if rf.Encoding != wire.FileInputEncodingBase64 {
		t.Fatalf("resolved file encoding = %q; want base64", rf.Encoding)
	}
	if rf.Content == nil {
		t.Fatal("resolved file has nil content")
	}
	gotBytes, err := base64.StdEncoding.DecodeString(*rf.Content)
	if err != nil {
		t.Fatalf("resolved content not base64: %v", err)
	}
	if !bytes.Equal(gotBytes, data) {
		t.Fatalf("resolved bytes mismatch: got %q want %q", gotBytes, data)
	}

	// The original spec must NOT be mutated (a fresh slice was returned).
	if spec.Files[1].Ref == nil {
		t.Fatal("resolveBlobRefs mutated the caller's spec.Files (ref cleared in place)")
	}
}

// TestResolveBlobRefs_Mismatch fails cleanly when the stored bytes do not hash
// to the ref — the verify/tamper gate.
func TestResolveBlobRefs_Mismatch(t *testing.T) {
	good := []byte("expected bytes")
	ref := refFor(good)
	// Store DIFFERENT bytes under the ref (tamper/corruption).
	store := &fakeBlobStore{blobs: map[string][]byte{ref: []byte("TAMPERED bytes")}}
	idx := newFakeIndex()
	w := newBlobWorker(store, idx)

	spec := wire.JobSpec{
		JobId: "job-2",
		Files: []wire.FileInput{{Name: "x.bin", Ref: wire.Ptr(ref)}},
	}
	_, leased, err := w.resolveBlobRefs(context.Background(), spec)
	if err == nil {
		t.Fatal("resolveBlobRefs: want sha256 mismatch error, got nil")
	}
	if !errors.Is(err, errBlobVerify) {
		t.Fatalf("error is not errBlobVerify: %v", err)
	}
	// The blob WAS leased before the pull, so the caller must release it.
	if len(leased) != 1 {
		t.Fatalf("leased = %v; want the ref recorded for release-on-terminal", leased)
	}
	// Simulate the worker's release-on-failure path.
	w.releaseLeases(context.Background(), leased, spec.JobId)
	if idx.activeLeases() != 0 {
		t.Fatalf("lease not released after mismatch: %d active", idx.activeLeases())
	}
}

// TestResolveBlobRefs_StoreMiss fails cleanly when the blob is absent.
func TestResolveBlobRefs_StoreMiss(t *testing.T) {
	ref := refFor([]byte("never uploaded"))
	store := &fakeBlobStore{blobs: map[string][]byte{}} // empty
	idx := newFakeIndex()
	w := newBlobWorker(store, idx)

	spec := wire.JobSpec{
		JobId: "job-3",
		Files: []wire.FileInput{{Name: "x.bin", Ref: wire.Ptr(ref)}},
	}
	_, leased, err := w.resolveBlobRefs(context.Background(), spec)
	if err == nil || !errors.Is(err, errBlobVerify) {
		t.Fatalf("want errBlobVerify store-miss, got %v", err)
	}
	w.releaseLeases(context.Background(), leased, spec.JobId)
	if idx.activeLeases() != 0 {
		t.Fatalf("lease not released after store-miss: %d active", idx.activeLeases())
	}
}

// TestResolveBlobRefs_NoRefs: an inline-only spec is returned unchanged with no
// leases (back-compat: existing callers unaffected).
func TestResolveBlobRefs_NoRefs(t *testing.T) {
	w := newBlobWorker(&fakeBlobStore{blobs: map[string][]byte{}}, newFakeIndex())
	spec := wire.JobSpec{
		JobId: "job-4",
		Files: []wire.FileInput{{Name: "main.py", Content: wire.Ptr("print(1)")}},
	}
	out, leased, err := w.resolveBlobRefs(context.Background(), spec)
	if err != nil {
		t.Fatalf("resolveBlobRefs: %v", err)
	}
	if len(leased) != 0 {
		t.Fatalf("leased = %v; want none for an inline-only spec", leased)
	}
	if out.Files[0].Content == nil || *out.Files[0].Content != "print(1)" {
		t.Fatalf("inline spec mutated: %+v", out.Files[0])
	}
}

// TestResolveBlobRefs_NoStoreConfigured: a ref with no blob store fails cleanly.
func TestResolveBlobRefs_NoStoreConfigured(t *testing.T) {
	w := &Worker{cfg: Config{}} // no BlobStore / BlobIndex
	spec := wire.JobSpec{
		JobId: "job-5",
		Files: []wire.FileInput{{Name: "x.bin", Ref: wire.Ptr(refFor([]byte("x")))}},
	}
	_, _, err := w.resolveBlobRefs(context.Background(), spec)
	if err == nil || !errors.Is(err, errBlobVerify) {
		t.Fatalf("want errBlobVerify for unconfigured store, got %v", err)
	}
}

// TestResolveBlobRefs_BothContentAndRef rejects a file carrying BOTH.
func TestResolveBlobRefs_BothContentAndRef(t *testing.T) {
	ref := refFor([]byte("x"))
	w := newBlobWorker(&fakeBlobStore{blobs: map[string][]byte{ref: []byte("x")}}, newFakeIndex())
	spec := wire.JobSpec{
		JobId: "job-6",
		Files: []wire.FileInput{{Name: "x.bin", Content: wire.Ptr("inline"), Ref: wire.Ptr(ref)}},
	}
	_, leased, err := w.resolveBlobRefs(context.Background(), spec)
	if err == nil || !errors.Is(err, errBlobVerify) {
		t.Fatalf("want errBlobVerify for content+ref, got %v", err)
	}
	// No lease should have been taken (the XOR check precedes the lease).
	if len(leased) != 0 {
		t.Fatalf("leased = %v; want none (XOR check precedes lease)", leased)
	}
}

// TestRefToHexHash validates the ref-shape guard.
func TestRefToHexHash(t *testing.T) {
	good := "sha256:" + hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte("x")); return s[:] }())
	if h, err := refToHexHash(good); err != nil || len(h) != 64 {
		t.Fatalf("refToHexHash(%q) = %q, %v; want 64-hex, nil", good, h, err)
	}
	bad := []string{
		"",
		"deadbeef",   // no prefix
		"sha256:zzz", // too short + non-hex
		"sha256:" + "g" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde", // non-hex
		"md5:" + hex.EncodeToString(make([]byte, 32)),                                       // wrong algo prefix
	}
	for _, b := range bad {
		if _, err := refToHexHash(b); err == nil {
			t.Errorf("refToHexHash(%q) = nil error; want rejection", b)
		}
	}
}
