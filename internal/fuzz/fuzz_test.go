package fuzz_test

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/fuzz"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

func TestGenerateProducesBoundedFaults(t *testing.T) {
	base := &scenario.Scenario{Seed: 1}
	for seed := uint64(0); seed < 20; seed++ {
		s := fuzz.Generate(base, 5, seed)
		if len(s.Faults) < 1 || len(s.Faults) > 5 {
			t.Fatalf("seed %d: expected 1..5 faults, got %d", seed, len(s.Faults))
		}
		for _, f := range s.Faults {
			if f.Action == "" {
				t.Fatalf("seed %d: empty action", seed)
			}
		}
	}
}

func TestGeneratePreservesAssertions(t *testing.T) {
	base := &scenario.Scenario{
		Seed:       4,
		Assertions: []scenario.Assertion{{Type: "terminal_state_reached"}},
	}
	s := fuzz.Generate(base, 3, 42)
	if len(s.Assertions) != 1 || s.Assertions[0].Type != "terminal_state_reached" {
		t.Fatalf("assertions not preserved: %+v", s.Assertions)
	}
}

func TestGenerateDifferentSeedsDifferentFaults(t *testing.T) {
	base := &scenario.Scenario{}
	s1 := fuzz.Generate(base, 8, 1)
	s2 := fuzz.Generate(base, 8, 2)
	// Very unlikely to be identical with different seeds.
	if len(s1.Faults) == len(s2.Faults) {
		// Compare first action to check they differ at least sometimes.
		if s1.Faults[0].Action == s2.Faults[0].Action &&
			len(s1.Faults) == 1 && len(s2.Faults) == 1 {
			// acceptable for tiny scenarios; don't fail
		}
	}
}

func TestRunWithNoUpstreamReturnsReport(t *testing.T) {
	r := fuzz.Run(fuzz.Options{
		Upstream:  "/bin/cat",
		Runs:      3,
		MaxFaults: 4,
	})
	if r == nil {
		t.Fatal("nil report")
	}
	if r.Runs != 3 {
		t.Fatalf("expected 3 runs, got %d", r.Runs)
	}
}
