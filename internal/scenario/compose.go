package scenario

import (
	"fmt"
	"os"
	"path/filepath"
)

// Load reads the scenario at path and resolves any extends/include
// directives, returning a fully merged *Scenario. Circular extends chains
// are detected and returned as an error. Load is the recommended entry
// point when composition is needed; Parse does NOT resolve composition.
func Load(path string) (*Scenario, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return load(abs, nil)
}

func load(abs string, seen map[string]bool) (*Scenario, error) {
	if seen == nil {
		seen = make(map[string]bool)
	}
	if seen[abs] {
		return nil, fmt.Errorf("composition cycle detected at %s", abs)
	}
	seen[abs] = true

	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", abs, err)
	}
	s, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", abs, err)
	}
	return resolve(s, filepath.Dir(abs), seen)
}

// Resolve applies extends/include directives to a scenario that was already
// parsed. here is the directory of the scenario file (used to resolve
// relative paths in extends/include). seen is the set of already-loaded
// absolute paths (used for cycle detection). Pass nil to start fresh.
func Resolve(s *Scenario, here string, seen map[string]bool) (*Scenario, error) {
	if seen == nil {
		seen = make(map[string]bool)
	}
	return resolve(s, here, seen)
}

func resolve(s *Scenario, here string, seen map[string]bool) (*Scenario, error) {
	result := &Scenario{
		Seed:       s.Seed,
		Faults:     nil,
		Assertions: nil,
	}

	// --- extends: load the base scenario first, then overlay s ---
	if s.Extends != "" {
		baseAbs, err := filepath.Abs(filepath.Join(here, s.Extends))
		if err != nil {
			return nil, fmt.Errorf("extends: resolve path %q: %w", s.Extends, err)
		}
		base, err := load(baseAbs, copySet(seen))
		if err != nil {
			return nil, fmt.Errorf("extends: %w", err)
		}
		result.Faults = append(result.Faults, base.Faults...)
		result.Assertions = append(result.Assertions, base.Assertions...)
		// The child's seed overrides the base's seed.
		if s.Seed != 0 {
			result.Seed = s.Seed
		} else {
			result.Seed = base.Seed
		}
	}

	// --- own faults/assertions overlay (extend overrides base) ---
	result.Faults = append(result.Faults, s.Faults...)
	result.Assertions = append(result.Assertions, s.Assertions...)

	// --- include: append faults/assertions from each included scenario ---
	for _, inc := range s.Include {
		incAbs, err := filepath.Abs(filepath.Join(here, inc))
		if err != nil {
			return nil, fmt.Errorf("include: resolve path %q: %w", inc, err)
		}
		included, err := load(incAbs, copySet(seen))
		if err != nil {
			return nil, fmt.Errorf("include %s: %w", inc, err)
		}
		result.Faults = append(result.Faults, included.Faults...)
		result.Assertions = append(result.Assertions, included.Assertions...)
	}

	return result, nil
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
