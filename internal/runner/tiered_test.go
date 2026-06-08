package runner

import (
	"context"
	"testing"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// fakeRunner records whether its Create was called and returns a nil sandbox.
type fakeRunner struct {
	name    string
	created int
	lastJob string
}

func (f *fakeRunner) Create(_ context.Context, spec wire.JobSpec) (Sandbox, error) {
	f.created++
	f.lastJob = spec.JobId
	return nil, nil
}

// predicate routing a fixed allow-list of languages to the zygote tier, standing
// in for the manifest-driven ZygoteEligibleFromRegistry without needing a real
// registry. The TieredRunner contract is "route per predicate", which this
// exercises directly.
func eligibleLangs(langs ...string) func(wire.JobSpec) bool {
	set := make(map[string]bool, len(langs))
	for _, l := range langs {
		set[l] = true
	}
	return func(spec wire.JobSpec) bool { return set[spec.Language] }
}

func TestTieredRunner_RoutesEligibleToZygote(t *testing.T) {
	docker := &fakeRunner{name: "docker"}
	zygote := &fakeRunner{name: "zygote"}
	// python is eligible; rust/sqlite/r are not.
	tr := NewTieredRunner(docker, zygote, eligibleLangs("python"))

	cases := []struct {
		lang       string
		wantZygote bool
	}{
		{"python", true},
		{"rust", false},
		{"sqlite", false},
		{"r", false},
	}
	for _, c := range cases {
		docker.created, zygote.created = 0, 0
		_, err := tr.Create(context.Background(), wire.JobSpec{Language: c.lang, JobId: c.lang})
		if err != nil {
			t.Fatalf("%s: Create error: %v", c.lang, err)
		}
		if c.wantZygote {
			if zygote.created != 1 || docker.created != 0 {
				t.Errorf("%s: expected zygote runner (zygote=%d docker=%d)", c.lang, zygote.created, docker.created)
			}
		} else {
			if docker.created != 1 || zygote.created != 0 {
				t.Errorf("%s: expected docker runner (zygote=%d docker=%d)", c.lang, zygote.created, docker.created)
			}
		}
	}
}

func TestTieredRunner_NilZygoteAlwaysDocker(t *testing.T) {
	docker := &fakeRunner{name: "docker"}
	// zygote=nil → disabled path. Even an "eligible" predicate must route to docker.
	tr := NewTieredRunner(docker, nil, eligibleLangs("python"))

	for _, lang := range []string{"python", "rust", "sqlite", "r"} {
		docker.created = 0
		if _, err := tr.Create(context.Background(), wire.JobSpec{Language: lang}); err != nil {
			t.Fatalf("%s: Create error: %v", lang, err)
		}
		if docker.created != 1 {
			t.Errorf("%s: disabled zygote must route to docker; docker created=%d", lang, docker.created)
		}
	}
}

func TestTieredRunner_NilPredicateAlwaysDocker(t *testing.T) {
	docker := &fakeRunner{name: "docker"}
	zygote := &fakeRunner{name: "zygote"}
	tr := NewTieredRunner(docker, zygote, nil)
	if _, err := tr.Create(context.Background(), wire.JobSpec{Language: "python"}); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if docker.created != 1 || zygote.created != 0 {
		t.Errorf("nil predicate must route to docker; docker=%d zygote=%d", docker.created, zygote.created)
	}
}

// closableFake is a fakeRunner whose Close is recorded, to prove TieredRunner.Close
// forwards to a zygote runner exposing Close (the real ZygoteRunner does).
type closableFake struct {
	fakeRunner
	closed int
}

func (c *closableFake) Close() error { c.closed++; return nil }

func TestTieredRunner_CloseForwardsToZygote(t *testing.T) {
	docker := &fakeRunner{name: "docker"}
	zygote := &closableFake{fakeRunner: fakeRunner{name: "zygote"}}
	tr := NewTieredRunner(docker, zygote, eligibleLangs("python"))
	if err := tr.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if zygote.closed != 1 {
		t.Errorf("Close should forward to zygote runner; closed=%d", zygote.closed)
	}

	// nil zygote → Close is a safe no-op.
	trNil := NewTieredRunner(docker, nil, nil)
	if err := trNil.Close(); err != nil {
		t.Fatalf("Close(nil zygote) error: %v", err)
	}
}
