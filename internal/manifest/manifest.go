// Package manifest loads, validates, and resolves language manifests from a
// directory of the form <root>/*/manifest.json.  It is the sole mechanism that
// maps language names/aliases to Docker images and resource limits — no
// language identifier is hardcoded in this package.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// Sentinel errors callers can match with errors.Is.
var (
	// ErrNotFound is returned by Resolve when no manifest matches the given
	// language/alias (and optional version).
	ErrNotFound = errors.New("manifest: language not found")

	// ErrDuplicate is returned by Load when two manifests claim the same
	// language+version pair or the same alias.
	ErrDuplicate = errors.New("manifest: duplicate language/alias")
)

// Registry is an immutable index of loaded manifests, keyed by language name,
// language+version, and alias.  Obtain one via Load.
type Registry struct {
	// byKey is indexed by "language" and "language@version".
	byKey map[string]wire.Manifest
	// byAlias maps each alias string to the manifest that declared it.
	byAlias map[string]wire.Manifest
	// all holds every manifest in load order (stable for List).
	all []wire.Manifest
}

// Load reads every <root>/*/manifest.json, validates each manifest, and
// returns a Registry.  An error is returned if any manifest is malformed or if
// two manifests conflict on language+version or alias.
func Load(root string) (*Registry, error) {
	pattern := filepath.Join(root, "*", "manifest.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("manifest: glob %q: %w", pattern, err)
	}

	reg := &Registry{
		byKey:   make(map[string]wire.Manifest),
		byAlias: make(map[string]wire.Manifest),
	}

	for _, path := range paths {
		m, err := loadOne(path)
		if err != nil {
			return nil, err
		}
		if err := reg.index(m, path); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// loadOne reads and validates a single manifest file.
func loadOne(path string) (wire.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return wire.Manifest{}, fmt.Errorf("manifest: read %q: %w", path, err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return wire.Manifest{}, fmt.Errorf("manifest: decode %q: %w", path, err)
	}
	if err := validate(m, path); err != nil {
		return wire.Manifest{}, err
	}
	return m, nil
}

// validate checks required fields and minimum Limits values.
func validate(m wire.Manifest, path string) error {
	if m.Language == "" {
		return fmt.Errorf("manifest: %q: field language is empty", path)
	}
	if m.Version == "" {
		return fmt.Errorf("manifest: %q: field version is empty", path)
	}
	if m.Image == "" {
		return fmt.Errorf("manifest: %q: field image is empty", path)
	}
	if m.Entrypoint == "" {
		return fmt.Errorf("manifest: %q: field entrypoint is empty", path)
	}
	if len(m.Run) == 0 {
		return fmt.Errorf("manifest: %q: field run must have at least one element", path)
	}
	if err := validateLimits(m.DefaultLimits, path); err != nil {
		return err
	}
	return nil
}

// validateLimits checks that all six Limits fields are >= 1.
func validateLimits(l wire.Limits, path string) error {
	fields := []struct {
		name  string
		value int
	}{
		{"defaultLimits.wallTimeMs", l.WallTimeMs},
		{"defaultLimits.idleMs", l.IdleMs},
		{"defaultLimits.cpuMs", l.CpuMs},
		{"defaultLimits.memoryMb", l.MemoryMb},
		{"defaultLimits.pids", l.Pids},
		{"defaultLimits.outputKb", l.OutputKb},
	}
	for _, f := range fields {
		if f.value < 1 {
			return fmt.Errorf("manifest: %q: field %s must be >= 1, got %d", path, f.name, f.value)
		}
	}
	return nil
}

// index registers a manifest in the Registry, returning ErrDuplicate if there
// is a conflict.
func (r *Registry) index(m wire.Manifest, path string) error {
	// Check language+version duplicate.
	versionKey := vkey(m.Language, m.Version)
	if existing, ok := r.byKey[versionKey]; ok {
		return fmt.Errorf("%w: language %q version %q already registered (image %q), conflict at %q",
			ErrDuplicate, m.Language, m.Version, existing.Image, path)
	}

	// Check alias duplicates.
	for _, alias := range m.Aliases {
		if existing, ok := r.byAlias[alias]; ok {
			return fmt.Errorf("%w: alias %q already claimed by language %q version %q, conflict at %q",
				ErrDuplicate, alias, existing.Language, existing.Version, path)
		}
	}

	// All clear — register.
	r.byKey[vkey(m.Language, m.Version)] = m
	// Also index by bare language name (first registration wins for bare lookups
	// when there is only one version; for multi-version we use version-keyed lookup).
	if _, ok := r.byKey[m.Language]; !ok {
		r.byKey[m.Language] = m
	}
	for _, alias := range m.Aliases {
		r.byAlias[alias] = m
	}
	r.all = append(r.all, m)
	return nil
}

// vkey builds the language@version index key.
func vkey(language, version string) string {
	return language + "@" + version
}

// Resolve returns the manifest for the given language name-or-alias and
// optional version.  When version is empty and there is exactly one matching
// manifest, it is returned; otherwise ErrNotFound is returned.
func (r *Registry) Resolve(language, version string) (wire.Manifest, error) {
	// Try language name first, then alias.
	m, found := r.lookup(language)
	if !found {
		return wire.Manifest{}, fmt.Errorf("%w: %q", ErrNotFound, language)
	}

	// If a specific version was requested, check the version-keyed index using
	// the resolved canonical language name (handles the alias case).
	if version != "" {
		versioned, ok := r.byKey[vkey(m.Language, version)]
		if !ok {
			return wire.Manifest{}, fmt.Errorf("%w: %q version %q", ErrNotFound, language, version)
		}
		return versioned, nil
	}
	return m, nil
}

// lookup looks up a manifest by language name OR alias.
func (r *Registry) lookup(nameOrAlias string) (wire.Manifest, bool) {
	if m, ok := r.byKey[nameOrAlias]; ok {
		return m, true
	}
	if m, ok := r.byAlias[nameOrAlias]; ok {
		return m, true
	}
	return wire.Manifest{}, false
}

// List returns a LanguageInfo descriptor for every loaded manifest.  The
// result is in load order and contains no hardcoded language names.
func (r *Registry) List() []wire.LanguageInfo {
	infos := make([]wire.LanguageInfo, len(r.all))
	for i, m := range r.all {
		infos[i] = wire.LanguageInfo{
			Language:    m.Language,
			Version:     m.Version,
			Aliases:     m.Aliases,
			Interactive: m.Interactive,
		}
	}
	return infos
}

// MergeLimits returns a new Limits value with only the non-nil fields from ov
// overriding base.  The base Limits value is never mutated.
func MergeLimits(base wire.Limits, ov *wire.LimitsOverride) wire.Limits {
	if ov == nil {
		return base
	}
	result := base
	if ov.WallTimeMs != nil {
		result.WallTimeMs = *ov.WallTimeMs
	}
	if ov.IdleMs != nil {
		result.IdleMs = *ov.IdleMs
	}
	if ov.CpuMs != nil {
		result.CpuMs = *ov.CpuMs
	}
	if ov.MemoryMb != nil {
		result.MemoryMb = *ov.MemoryMb
	}
	if ov.Pids != nil {
		result.Pids = *ov.Pids
	}
	if ov.OutputKb != nil {
		result.OutputKb = *ov.OutputKb
	}
	return result
}
