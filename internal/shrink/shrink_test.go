package shrink_test

import (
	"fmt"
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/assert"
	"github.com/seanrobmerriam/agentchaos/internal/event"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
	"github.com/seanrobmerriam/agentchaos/internal/shrink"
)

// ============================================================================
// Phase 6 TDD: Shrinking — bisect the fault schedule to a minimal reproducer.
//
// Given a synthetic fault schedule where only 2 of 10 faults are necessary
// to trigger the failure, shrinking must converge to exactly those 2 within
// a bounded number of iterations.
// ============================================================================

// makeFaultSchedule builds a scenario with N faults, only faultIndices
// marked as "necessary" being needed to trigger the failure.
// The predicate function simulates running the scenario and checking if
// the relevant assertions fail.

// syntheticScenario: 10 faults, only faults at indices 3 and 7 are needed.
// We tag each fault with a unique tool name so the predicate can identify
// them regardless of their position in the shrunk list.
func syntheticScenario() *scenario.Scenario {
	s := &scenario.Scenario{Seed: 42, Assertions: []scenario.Assertion{
		{Type: "no_duplicate_effect"},
	}}
	for i := 0; i < 10; i++ {
		toolName := fmt.Sprintf("tool_%d", i)
		s.Faults = append(s.Faults, scenario.Fault{
			Match:  scenario.Matcher{Tool: &toolName},
			Action: "in_doubt",
		})
	}
	return s
}

// predicate10 simulates a run: it returns true (failure reproduced) if
// both faults tagged with tool "tool_3" and "tool_7" are present.
func predicate10(s *scenario.Scenario) bool {
	has3, has7 := false, false
	for _, f := range s.Faults {
		if f.Match.Tool != nil {
			switch *f.Match.Tool {
			case "tool_3":
				has3 = true
			case "tool_7":
				has7 = true
			}
		}
	}
	return has3 && has7
}

// ---- Example: shrink 10 faults to 2 necessary ----

func TestShrinkTenToTwo(t *testing.T) {
	original := syntheticScenario()
	f := predicate10

	result, err := shrink.Shrink(original, f, shrink.Options{MaxIterations: 100})
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}

	if len(result.Scenario.Faults) != 2 {
		t.Fatalf("expected 2 faults after shrinking, got %d", len(result.Scenario.Faults))
	}

	// Verify the predicate still holds on the shrunk scenario.
	if !f(result.Scenario) {
		t.Fatal("predicate should still hold after shrinking")
	}

	// The shrunk faults should be the ones originally at indices 3 and 7.
	// After shrinking the indices are remapped, but the faults themselves
	// should be the same objects.
	t.Logf("[shrink] 10 → %d faults, predicate holds", len(result.Scenario.Faults))
}

// ---- Example: shrink with target failures via event log ----

// predicateEvent simulates a real assertion-checking predicate: it builds
// an event log from the scenario's faults and checks assertions.
// For this test: a fault at index 2 causes a duplicate effect, and a fault
// at index 5 causes a missing checkpoint. Both are needed for the failure.
func predicateEvent(s *scenario.Scenario) bool {
	if len(s.Faults) == 0 {
		return false
	}
	// Build event log.
	l := event.New()
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Key: "dup-key"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 1})
	// Only include the "bad" events if the relevant faults are present.
	for i := range s.Faults {
		if i == 2 {
			// Fault 2 creates a duplicate.
			l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 2, Key: "dup-key"})
			l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 2})
		}
	}
	// Check assertions.
	results := assert.CheckAll([]scenario.Assertion{
		{Type: "no_duplicate_effect"},
	}, l)
	return assert.AnyFailed(results)
}

// ---- Property: shrink never increases fault count ----

func TestShrinkNeverIncreases(t *testing.T) {
	// Run with a simple predicate: failure iff any fault is present.
	original := &scenario.Scenario{Seed: 1}
	for i := 0; i < 20; i++ {
		original.Faults = append(original.Faults, scenario.Fault{
			Match:  scenario.Matcher{Tool: strPtrShort("*")},
			Action: "in_doubt",
		})
	}
	original.Assertions = []scenario.Assertion{{Type: "no_duplicate_effect"}}

	// Predicate: any fault with index >= 5 triggers failure.
	f := func(s *scenario.Scenario) bool {
		for i := range s.Faults {
			if i >= 5 {
				return true
			}
		}
		return false
	}

	result, err := shrink.Shrink(original, f, shrink.Options{MaxIterations: 50})
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}

	if len(result.Scenario.Faults) > len(original.Faults) {
		t.Fatalf("shrink increased fault count: %d → %d",
			len(original.Faults), len(result.Scenario.Faults))
	}
	// With predicate "any fault >= 5", shrinking should find exactly 1
	// fault (the minimal subset).
	if !f(result.Scenario) {
		t.Fatal("predicate should still hold after shrinking")
	}
	t.Logf("[shrink] 20 → %d faults (predicate: any >= 5)", len(result.Scenario.Faults))
}

// ---- Example: empty scenario ─ no crash ----

func TestShrinkEmpty(t *testing.T) {
	empty := &scenario.Scenario{Seed: 1, Faults: []scenario.Fault{}}
	_, err := shrink.Shrink(empty, func(*scenario.Scenario) bool { return true },
		shrink.Options{MaxIterations: 10})
	if err == nil {
		t.Fatal("expected error for empty scenario that always fails")
	}
}

// ---- Example: predicate never holds (no failure to shrink) ----

func TestShrinkNoFailureToReproduce(t *testing.T) {
	s := &scenario.Scenario{Seed: 1}
	s.Faults = []scenario.Fault{
		{Match: scenario.Matcher{Tool: strPtrShort("x")}, Action: "in_doubt"},
	}
	_, err := shrink.Shrink(s, func(*scenario.Scenario) bool { return false },
		shrink.Options{MaxIterations: 10})
	if err == nil {
		t.Fatal("expected error: predicate never holds on original")
	}
}

// ---- helpers ----

func strPtrShort(s string) *string { return &s }
