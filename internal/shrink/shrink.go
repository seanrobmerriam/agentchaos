// Package shrink implements failure-shrinking: given a scenario that
// triggers an assertion failure, it searches for a smaller fault schedule
// (fewer faults) that still reproduces the same failure.
//
// The algorithm is a Jepsen-style bisect: try removing each fault one at a
// time; if the predicate still holds (failure reproduces), keep the smaller
// version. Repeat until no further reduction is possible.
//
// See SPEC.md §6.
package shrink

import (
	"fmt"

	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// Predicate is the function that determines whether a given scenario
// reproduces the failure. It should return true iff the failure is
// observed. In production this runs the actual scenario through the
// executor + assertion checker; in tests it can be a synthetic function.
type Predicate func(s *scenario.Scenario) bool

// Options controls the shrinking behaviour.
type Options struct {
	// MaxIterations bounds the total number of predicate calls. If zero,
	// defaults to 1000.
	MaxIterations int
}

// Result holds the shrunk scenario and statistics.
type Result struct {
	Scenario    *scenario.Scenario
	Iterations  int
	OriginalN   int
	FinalN      int
}

// Shrink searches for a minimal subset of faults from the original scenario
// that still satisfies the predicate (reproduces the failure). It uses a
// greedy single-fault-removal strategy: iterate through faults, try removing
// each, and keep the smaller scenario if the predicate still holds. Repeat
// rounds until no further reduction is possible or MaxIterations is reached.
//
// The predicate MUST hold on the original scenario; if it doesn't,
// Shrink returns an error.
func Shrink(original *scenario.Scenario, pred Predicate, opts Options) (*scenario.Scenario, error) {
	if len(original.Faults) == 0 {
		return nil, fmt.Errorf("shrink: scenario has no faults to shrink")
	}
	if !pred(original) {
		return nil, fmt.Errorf("shrink: predicate does not hold on the original scenario (no failure to reproduce)")
	}

	maxIter := opts.MaxIterations
	if maxIter == 0 {
		maxIter = 1000
	}

	current := copyScenario(original)
	iterations := 0

	// Keep shrinking until no more faults can be removed.
	for {
		reduced := false
		for i := 0; i < len(current.Faults); i++ {
			iterations++
			if iterations > maxIter {
				return current, nil
			}
			// Try removing fault at index i.
			candidate := removeFault(current, i)
			if pred(candidate) {
				current = candidate
				reduced = true
				// Restart scanning from the beginning since indices shifted.
				break
			}
		}
		if !reduced {
			// No further reductions possible.
			break
		}
	}

	_ = Result{Scenario: current, Iterations: iterations, OriginalN: len(original.Faults), FinalN: len(current.Faults)}
	return current, nil
}

// copyScenario returns a shallow copy of the scenario with a copy of the
// Faults slice. The Fault structs themselves are value-copied.
func copyScenario(s *scenario.Scenario) *scenario.Scenario {
	out := *s
	if s.Faults != nil {
		out.Faults = make([]scenario.Fault, len(s.Faults))
		copy(out.Faults, s.Faults)
	}
	if s.Assertions != nil {
		out.Assertions = make([]scenario.Assertion, len(s.Assertions))
		copy(out.Assertions, s.Assertions)
	}
	return &out
}

// removeFault returns a copy of the scenario with the fault at index i
// removed. Falls are shifted down.
func removeFault(s *scenario.Scenario, i int) *scenario.Scenario {
	out := copyScenario(s)
	out.Faults = append(out.Faults[:i], out.Faults[i+1:]...)
	return out
}

// ShrinkResult is the old name kept for backward compatibility.
type ShrinkResult = Result