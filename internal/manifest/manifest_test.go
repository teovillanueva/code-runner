package manifest_test

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/teovillanueva/code-runner/internal/manifest"
	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// testdataDir returns the absolute path to a subdirectory under testdata/,
// computed relative to this source file so tests work regardless of working
// directory.
func testdataDir(sub string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", sub)
}

// ─── Task 3 acceptance criterion: exactly ten named test cases ───────────────

func TestLoadValid(t *testing.T) {
	reg, err := manifest.Load(testdataDir("valid"))
	if err != nil {
		t.Fatalf("Load(valid) unexpected error: %v", err)
	}
	infos := reg.List()
	if len(infos) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(infos))
	}
	if infos[0].Language != "python" {
		t.Errorf("Language = %q; want %q", infos[0].Language, "python")
	}
	if infos[0].Version != "3.12" {
		t.Errorf("Version = %q; want %q", infos[0].Version, "3.12")
	}
}

func TestLoadMalformedErrors(t *testing.T) {
	_, err := manifest.Load(testdataDir("malformed"))
	if err == nil {
		t.Fatal("Load(malformed) expected error, got nil")
	}
}

func TestLoadDuplicateErrors(t *testing.T) {
	_, err := manifest.Load(testdataDir("duplicate"))
	if err == nil {
		t.Fatal("Load(duplicate) expected error, got nil")
	}
	if !errors.Is(err, manifest.ErrDuplicate) {
		t.Errorf("Load(duplicate) error should wrap ErrDuplicate; got: %v", err)
	}
}

func TestResolveByName(t *testing.T) {
	reg, err := manifest.Load(testdataDir("valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m, err := reg.Resolve("python", "")
	if err != nil {
		t.Fatalf("Resolve(python): %v", err)
	}
	if m.Language != "python" {
		t.Errorf("Language = %q; want %q", m.Language, "python")
	}
	if m.Image != "executor/python:3.12" {
		t.Errorf("Image = %q; want %q", m.Image, "executor/python:3.12")
	}
}

func TestResolveByAlias(t *testing.T) {
	reg, err := manifest.Load(testdataDir("valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m, err := reg.Resolve("py", "")
	if err != nil {
		t.Fatalf("Resolve(py): %v", err)
	}
	if m.Language != "python" {
		t.Errorf("Resolve by alias: Language = %q; want %q", m.Language, "python")
	}
}

func TestResolveByVersion(t *testing.T) {
	reg, err := manifest.Load(testdataDir("valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m, err := reg.Resolve("python", "3.12")
	if err != nil {
		t.Fatalf("Resolve(python, 3.12): %v", err)
	}
	if m.Version != "3.12" {
		t.Errorf("Version = %q; want %q", m.Version, "3.12")
	}
}

func TestResolveUnknownNotFound(t *testing.T) {
	reg, err := manifest.Load(testdataDir("valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = reg.Resolve("doesnotexist", "")
	if err == nil {
		t.Fatal("expected error for unknown language, got nil")
	}
	if !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("error should wrap ErrNotFound; got: %v", err)
	}
}

func TestListContents(t *testing.T) {
	reg, err := manifest.Load(testdataDir("valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	infos := reg.List()
	if len(infos) == 0 {
		t.Fatal("List() returned empty slice")
	}
	info := infos[0]
	if info.Language == "" {
		t.Error("LanguageInfo.Language is empty")
	}
	if info.Version == "" {
		t.Error("LanguageInfo.Version is empty")
	}
	if len(info.Aliases) == 0 {
		t.Error("LanguageInfo.Aliases is empty")
	}
}

func TestMergeLimitsPartialOverride(t *testing.T) {
	base := wire.Limits{
		WallTimeMs: 30000,
		IdleMs:     10000,
		CpuMs:      15000,
		MemoryMb:   128,
		Pids:       64,
		OutputKb:   512,
	}
	newWall := 60000
	ov := &wire.LimitsOverride{WallTimeMs: &newWall}

	merged := manifest.MergeLimits(base, ov)

	if merged.WallTimeMs != 60000 {
		t.Errorf("WallTimeMs = %d; want 60000", merged.WallTimeMs)
	}
	if merged.IdleMs != base.IdleMs {
		t.Errorf("IdleMs should be unchanged: got %d, want %d", merged.IdleMs, base.IdleMs)
	}
	if merged.MemoryMb != base.MemoryMb {
		t.Errorf("MemoryMb should be unchanged: got %d, want %d", merged.MemoryMb, base.MemoryMb)
	}
}

func ptrPreimport(v wire.ManifestPreimport) *wire.ManifestPreimport { return &v }

func TestZygoteEligibleAndPreimports(t *testing.T) {
	cases := []struct {
		name      string
		preimport *wire.ManifestPreimport
		eligible  bool
		want      []string
	}{
		{"nil_preimport_routes_to_docker", nil, false, nil},
		{"empty_preimport_routes_to_docker", ptrPreimport(wire.ManifestPreimport{}), false, []string{}},
		{"nonempty_preimport_is_zygote", ptrPreimport(wire.ManifestPreimport{"numpy", "pandas"}), true, []string{"numpy", "pandas"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := wire.Manifest{Language: "x", Preimport: tc.preimport}
			if got := manifest.ZygoteEligible(m); got != tc.eligible {
				t.Errorf("ZygoteEligible = %v; want %v", got, tc.eligible)
			}
			got := manifest.Preimports(m)
			if len(got) != len(tc.want) {
				t.Fatalf("Preimports len = %d (%v); want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Preimports[%d] = %q; want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestRealManifestsTiering pins the routing decision for the four shipped
// languages: Python + R are zygote-eligible (declare a preimport set), Rust +
// SQLite are not (route to Docker). This guards the locked tiered-coverage
// decision against accidental manifest edits.
func TestRealManifestsTiering(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	langRoot := filepath.Join(filepath.Dir(file), "..", "..", "languages")
	reg, err := manifest.Load(langRoot)
	if err != nil {
		t.Fatalf("Load(%s): %v", langRoot, err)
	}
	want := map[string]bool{"python": true, "r": true, "rust": false, "sqlite": false}
	for lang, wantEligible := range want {
		m, err := reg.Resolve(lang, "")
		if err != nil {
			t.Fatalf("Resolve(%s): %v", lang, err)
		}
		if got := manifest.ZygoteEligible(m); got != wantEligible {
			t.Errorf("%s: ZygoteEligible = %v; want %v (preimport=%v)", lang, got, wantEligible, manifest.Preimports(m))
		}
	}
}

func TestMergeLimitsDoesNotMutateBase(t *testing.T) {
	base := wire.Limits{
		WallTimeMs: 30000,
		IdleMs:     10000,
		CpuMs:      15000,
		MemoryMb:   128,
		Pids:       64,
		OutputKb:   512,
	}
	original := base // copy before merge

	newWall := 99999
	ov := &wire.LimitsOverride{WallTimeMs: &newWall}
	_ = manifest.MergeLimits(base, ov)

	if base != original {
		t.Errorf("MergeLimits mutated base: got %+v, want %+v", base, original)
	}
}
