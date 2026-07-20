// Package assert evaluates assertion rules against the event log after a
// fault-injection run completes. Built-in assertions cover:
//
//   - no_duplicate_effect: no two requests with the same idempotency key
//     produced an observed effect (response delivered to the agent).
//   - terminal_state_reached: a terminal state event was recorded within
//     within_retries attempts.
//   - effect_without_checkpoint_commit: an effect (response delivered) was
//     observed but no matching checkpoint commit was recorded.
//
// User-supplied verifiers can be registered via RegisterCustom for custom
// assertion types. See SPEC.md §7.
package assert

import (
	"fmt"
	"sync"

	"github.com/seanrobmerriam/agentchaos/internal/event"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// Result is the outcome of evaluating one assertion against the event log.
type Result struct {
	Failed bool
	Reason string
}

// Verifier is a user-supplied checker function for custom assertion types.
// It receives the event log and the assertion spec, and returns a Result.
type Verifier func(a scenario.Assertion, log *event.Log) Result

var (
	customMu sync.RWMutex
	custom   = make(map[string]Verifier)
)

// RegisterCustom registers a user-supplied verifier for a custom assertion
// type. If type already has a built-in checker, the custom one takes
// precedence. This allows override and extension.
func RegisterCustom(typeName string, fn Verifier) {
	customMu.Lock()
	defer customMu.Unlock()
	custom[typeName] = fn
}

// Check evaluates one assertion against the event log. Built-in assertions
// are checked first; if the type is not a built-in, a registered custom
// verifier is used; if none is registered, the assertion fails.
func Check(a scenario.Assertion, log *event.Log) Result {
	// Check custom verifiers first (they can override built-ins).
	customMu.RLock()
	fn, ok := custom[a.Type]
	customMu.RUnlock()
	if ok {
		return fn(a, log)
	}

	switch a.Type {
	case "no_duplicate_effect":
		return checkNoDuplicateEffect(a, log)
	case "terminal_state_reached":
		return checkTerminalStateReached(a, log)
	case "effect_without_checkpoint_commit":
		return checkEffectWithoutCheckpoint(a, log)
	default:
		return Result{
			Failed: true,
			Reason: fmt.Sprintf("unknown assertion type: %q", a.Type),
		}
	}
}

// CheckAll evaluates a list of assertions against the event log.
func CheckAll(assertions []scenario.Assertion, log *event.Log) []Result {
	results := make([]Result, len(assertions))
	for i, a := range assertions {
		results[i] = Check(a, log)
	}
	return results
}

// AnyFailed returns true if any result in the slice is failed.
func AnyFailed(results []Result) bool {
	for _, r := range results {
		if r.Failed {
			return true
		}
	}
	return false
}

// ---- no_duplicate_effect ----

// checkNoDuplicateEffect verifies that no two requests with the same
// idempotency key both produced an observed effect. An "observed effect"
// is a KindResponseDelivered event — a response that was actually
// delivered to the agent. A dropped response (KindResponseDropped) does
// NOT count as an observed effect (the agent never saw it).
func checkNoDuplicateEffect(a scenario.Assertion, log *event.Log) Result {
	sentEvents := log.Filter(event.KindRequestSent)
	deliveredEvents := log.Filter(event.KindResponseDelivered)

	// Build a set of MsgIDs that were delivered.
	deliveredIDs := make(map[int64]bool)
	for _, e := range deliveredEvents {
		deliveredIDs[e.MsgID] = true
	}

	// Count how many distinct requests with the same key had responses
	// delivered. If any key maps to 2+ delivered responses, it's a dup.
	keyToDeliveredCount := make(map[string]int)
	for _, e := range sentEvents {
		if e.Key == "" {
			continue
		}
		if deliveredIDs[e.MsgID] {
			keyToDeliveredCount[e.Key]++
		}
	}

	for key, count := range keyToDeliveredCount {
		if count > 1 {
			return Result{
				Failed: true,
				Reason: fmt.Sprintf("idempotency key %q produced %d observed effects (duplicate)", key, count),
			}
		}
	}

	return Result{Failed: false}
}

// ---- terminal_state_reached ----

// checkTerminalStateReached verifies that a terminal state event was
// recorded and the number of request retries before it is within the
// within_retries limit.
func checkTerminalStateReached(a scenario.Assertion, log *event.Log) Result {
	terminalEvents := log.Filter(event.KindTerminalState)
	if len(terminalEvents) == 0 {
		return Result{
			Failed: true,
			Reason: "terminal state was never reached",
		}
	}

	// Count the number of requests sent before the first terminal state.
	requestEvents := log.Filter(event.KindRequestSent)
	terminalSeq := terminalEvents[0].Seq
	retries := 0
	for _, e := range requestEvents {
		if e.Seq < terminalSeq {
			retries++
		}
	}

	limit := a.WithinRetries
	if limit == 0 {
		limit = 5 // default per spec
	}
	if retries > limit {
		return Result{
			Failed: true,
			Reason: fmt.Sprintf("terminal state reached after %d retries, limit is %d", retries, limit),
		}
	}

	return Result{Failed: false}
}

// ---- effect_without_checkpoint_commit ----

// checkEffectWithoutCheckpoint verifies that every delivered effect
// (KindResponseDelivered for a tools/call) has a matching checkpoint
// commit event (KindCheckpointCommit with the same MsgID or Tool).
func checkEffectWithoutCheckpoint(a scenario.Assertion, log *event.Log) Result {
	deliveredEvents := log.Filter(event.KindResponseDelivered)
	commitEvents := log.Filter(event.KindCheckpointCommit)

	// Build a set of MsgIDs and Tools that have commits.
	committedMsgIDs := make(map[int64]bool)
	committedTools := make(map[string]bool)
	for _, e := range commitEvents {
		committedMsgIDs[e.MsgID] = true
		if e.Tool != "" {
			committedTools[e.Tool] = true
		}
	}

	for _, e := range deliveredEvents {
		// Only check tools/call responses (effects with side effects).
		if e.Tool == "" {
			continue
		}
		hasCommit := committedMsgIDs[e.MsgID]
		if !hasCommit && e.Tool != "" {
			hasCommit = committedTools[e.Tool]
		}
		if !hasCommit {
			return Result{
				Failed: true,
				Reason: fmt.Sprintf("effect observed for tool %q (msg id=%d) without checkpoint commit", e.Tool, e.MsgID),
			}
		}
	}

	return Result{Failed: false}
}
