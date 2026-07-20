package scenario_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

func writeYAML(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadNoComposition(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "s.yaml", "seed: 7\nfaults:\n  - {action: duplicate, match: {}, count: 1}\nassertions: []\n")
	s, err := scenario.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Seed != 7 || len(s.Faults) != 1 {
		t.Fatalf("unexpected: %+v", s)
	}
}

func TestLoadExtends(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "base.yaml", "seed: 1\nfaults:\n  - {action: in_doubt, match: {}}\nassertions:\n  - {type: terminal_state_reached}\n")
	path := writeYAML(t, dir, "child.yaml", "seed: 2\nextends: base.yaml\nfaults:\n  - {action: duplicate, match: {}, count: 1}\nassertions: []\n")
	s, err := scenario.Load(path)
	if err != nil {
		t.Fatalf("Load extends: %v", err)
	}
	if s.Seed != 2 {
		t.Fatalf("expected child seed 2, got %d", s.Seed)
	}
	if len(s.Faults) != 2 {
		t.Fatalf("expected 2 faults (base + child), got %d", len(s.Faults))
	}
	// base assertion is inherited
	if len(s.Assertions) != 1 {
		t.Fatalf("expected 1 inherited assertion, got %d", len(s.Assertions))
	}
}

func TestLoadInclude(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "extra.yaml", "seed: 0\nfaults:\n  - {action: reorder, match: {}, window: 3}\nassertions: []\n")
	path := writeYAML(t, dir, "main.yaml", "seed: 5\nfaults:\n  - {action: kill_process, match: {}}\ninclude:\n  - extra.yaml\nassertions: []\n")
	s, err := scenario.Load(path)
	if err != nil {
		t.Fatalf("Load include: %v", err)
	}
	if len(s.Faults) != 2 {
		t.Fatalf("expected 2 faults (own + included), got %d: %+v", len(s.Faults), s.Faults)
	}
}

func TestLoadCycleDetected(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "a.yaml", "seed: 1\nextends: b.yaml\nfaults: []\nassertions: []\n")
	writeYAML(t, dir, "b.yaml", "seed: 2\nextends: a.yaml\nfaults: []\nassertions: []\n")
	_, err := scenario.Load(filepath.Join(dir, "a.yaml"))
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
}
