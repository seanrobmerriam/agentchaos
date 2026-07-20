// Package shrink implements failure-shrinking: given a scenario that
// triggers an assertion failure, it searches for a smaller fault schedule
// (fewer faults) that still reproduces the same failure.
//
// Two strategies are available:
//
//   - Greedy (default): iterate through faults, try removing each one at
//     a time, and keep the smaller scenario if the predicate still holds.
//     Repeat until no further reduction is possible.
//   - Bisect: repeatedly halve the fault list, keeping the half that
//     still triggers the predicate, then fall through to the greedy
//     strategy on the remainder.
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

// Strategy names the shrink algorithm to apply. An empty Strategy is
// treated as StrategyGreedy.
type Strategy string

const (
	// StrategyGreedy is the default single-fault-removal shrinker.
	StrategyGreedy Strategy = "greedy"
	// StrategyBisect repeatedly halves the fault list, keeping the
	// half that still triggers the predicate, then falls through to
	// greedy.
	StrategyBisect Strategy = "bisect"
)

// Options controls the shrinking behaviour.
type Options struct {
	// MaxIterations bounds the total number of predicate calls. If
	// zero, defaults to 1000.
	MaxIterations int
	// Strategy selects the shrink algorithm. Empty string is treated
	// as StrategyGreedy.
	Strategy Strategy
}

// Result holds the shrunk scenario and statistics.
type Result struct {
	Scenario   *scenario.Scenario
	Iterations int
	OriginalN  int
	FinalN     int
}

// Shrink searches for a minimal subset of faults from the original
// scenario that still satisfies the predicate (reproduces the failure).
// The choice of algorithm is governed by opts.Strategy: "" or
// StrategyGreedy runs the single-fault-removal loop; StrategyBisect
// halves the fault list before falling through to greedy.
//
// The predicate MUST hold on the original scenario; if it doesn't,
// Shrink returns an error.
func Shrink(original *scenario.Scenario, pred Predicate, opts Options) (*Result, error) {
	if len(original.Faults) == 0 {
		return nil, fmt.Errorf("shrink: scenario has no faults to shrink")
	}
	if !pred(original) {
		return nil, fmt.Errorf("shrink: predicate does not hold on the original scenario (no failure to reproduce)")
	}

	switch opts.Strategy {
	case StrategyBisect:
		return shrinkByBisect(original, pred, opts)
	case "", StrategyGreedy:
		return shrinkGreedy(original, pred, opts)
	default:
		return nil, fmt.Errorf("shrink: unknown strategy %q (want %q or %q)",
			opts.Strategy, StrategyGreedy, StrategyBisect)
	}
}

// shrinkGreedy implements the default single-fault-removal loop: it
// iterates through faults, tries removing each, and keeps the smaller
// scenario if the predicate still holds. Rounds repeat until no
// further reduction is possible or MaxIterations is reached.
func shrinkGreedy(original *scenario.Scenario, pred Predicate, opts Options) (*Result, error) {
	maxIter := effectiveMaxIter(opts)

	current := copyScenario(original)
	iterations := 0

	for {
		reduced := false
		for i := 0; i < len(current.Faults); i++ {
			iterations++
			if iterations > maxIter {
				return &Result{
					Scenario:   current,
					Iterations: iterations,
					OriginalN:  len(original.Faults),
					FinalN:     len(current.Faults),
				}, nil
			}
			candidate := removeFault(current, i)
			if pred(candidate) {
				current = candidate
				reduced = true
				break
			}
		}
		if !reduced {
			break
		}
	}

	return &Result{
		Scenario:   current,
		Iterations: iterations,
		OriginalN:  len(original.Faults),
		FinalN:     len(current.Faults),
	}, nil
}

// shrinkByBisect repeatedly halves the fault list and keeps the half
// that still triggers the predicate. When neither half does (or when
// the remainder has been reduced to a single fault), it falls
// through to the greedy strategy on the current remainder.
func shrinkByBisect(original *scenario.Scenario, pred Predicate, opts Options) (*Result, error) {
	maxIter := effectiveMaxIter(opts)

	current := copyScenario(original)
	iterations := 0
	keepBisecting := true

	for keepBisecting && len(current.Faults) > 1 {
		faults := current.Faults
		mid := len(faults) / 2

		left := copyScenario(current)
		left.Faults = append([]scenario.Fault(nil), faults[:mid]...)
		right := copyScenario(current)
		right.Faults = append([]scenario.Fault(nil), faults[mid:]...)

		iterations++
		leftHolds := pred(left)
		iterations++
		rightHolds := pred(right)

		if iterations > maxIter {
			return &Result{
				Scenario:   current,
				Iterations: iterations,
				OriginalN:  len(original.Faults),
				FinalN:     len(current.Faults),
			}, nil
		}

		switch {
		case leftHolds && rightHolds:
			// Both halves work; pick the smaller one to shrink faster.
			if len(left.Faults) <= len(right.Faults) {
				current = left
			} else {
				current = right
			}
		case leftHolds:
			current = left
		case rightHolds:
			current = right
		default:
			// Neither half alone reproduces the failure; bisect
			// cannot reduce further at this granularity.
			keepBisecting = false
		}
	}

	// Hand off to greedy to finish on the remainder.
	remaining := maxIter - iterations
	if remaining <= 0 {
		return &Result{
			Scenario:   current,
			Iterations: iterations,
			OriginalN:  len(original.Faults),
			FinalN:     len(current.Faults),
		}, nil
	}
	optsCopy := opts
	optsCopy.MaxIterations = remaining
	optsCopy.Strategy = StrategyGreedy
	res, err := shrinkGreedy(current, pred, optsCopy)
	if err != nil {
		return nil, err
	}
	res.Iterations += iterations
	res.OriginalN = len(original.Faults)
	return res, nil
}

// effectiveMaxIter returns opts.MaxIterations or 1000 if zero.
func effectiveMaxIter(opts Options) int {
	if opts.MaxIterations == 0 {
		return 1000
	}
	return opts.MaxIterations
}

// copyScenario returns a shallow copy of the scenario with a copy of
// the Faults slice. The Fault structs themselves are value-copied.
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

// removeFault returns a copy of the scenario with the fault at index
// i removed. Falls are shifted down.
func removeFault(s *scenario.Scenario, i int) *scenario.Scenario {
	out := copyScenario(s)
	out.Faults = append(out.Faults[:i], out.Faults[i+1:]...)
	return out
}

// ShrinkResult is the old name kept for backward compatibility.
type ShrinkResult = Result
