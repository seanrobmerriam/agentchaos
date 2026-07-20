package assert_test

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/assert"
	"github.com/seanrobmerriam/agentchaos/internal/event"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// ============================================================================
// Phase 5 TDD: Assertion checkers — built-in assertions evaluated against
// the event log.
//
// Built-ins:
//   1. no_duplicate_effect  — no two requests with the same idempotency key
//      produced an effect (response received).
//   2. terminal_state_reached — a terminal state event was recorded within
//      within_retries attempts.
//   3. effect_without_checkpoint_commit — an effect was observed (response
//      delivered) but no matching checkpoint commit was recorded.
// ============================================================================

// ---- no_duplicate_effect: positive (no duplicates) ----

func TestNoDuplicateEffectPositive(t *testing.T) {
	l := event.New()
	// Two requests with different idempotency keys — both get effects.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Key: "key-A"})
	l.Record(event.Event{Kind: event.KindResponseReceived, MsgID: 1})
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 2, Key: "key-B"})
	l.Record(event.Event{Kind: event.KindResponseReceived, MsgID: 2})

	a := scenario.Assertion{Type: "no_duplicate_effect", Key: "idempotency_key"}
	result := assert.Check(a, l)
	if result.Failed {
		t.Fatalf("expected pass, got: %s", result.Reason)
	}
}

// ---- no_duplicate_effect: negative (duplicate effect) ----

func TestNoDuplicateEffectNegative(t *testing.T) {
	l := event.New()
	// Two requests with the SAME idempotency key — both get effects delivered.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Key: "key-A"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 1})
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 2, Key: "key-A"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 2})

	a := scenario.Assertion{Type: "no_duplicate_effect", Key: "idempotency_key"}
	result := assert.Check(a, l)
	if !result.Failed {
		t.Fatal("expected failure for duplicate idempotency key")
	}
}

// ---- no_duplicate_effect: same key but one dropped (in_doubt) = no dup ----

func TestNoDuplicateEffectDroppedNotDuplicate(t *testing.T) {
	l := event.New()
	// First request: response dropped (in_doubt) — no effect observed by agent.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Key: "key-A"})
	l.Record(event.Event{Kind: event.KindResponseDropped, MsgID: 1})
	// Second request (retry with same key): response delivered — one effect.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 2, Key: "key-A"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 2})

	a := scenario.Assertion{Type: "no_duplicate_effect", Key: "idempotency_key"}
	result := assert.Check(a, l)
	if result.Failed {
		t.Fatalf("expected pass (one dropped, one delivered = no duplicate): %s", result.Reason)
	}
}

// ---- terminal_state_reached: positive ----

func TestTerminalStateReachedPositive(t *testing.T) {
	l := event.New()
	// 3 retries, then terminal state on attempt 4.
	for i := int64(1); i <= 3; i++ {
		l.Record(event.Event{Kind: event.KindRequestSent, MsgID: i})
		l.Record(event.Event{Kind: event.KindResponseReceived, MsgID: i})
	}
	l.Record(event.Event{Kind: event.KindTerminalState, MsgID: 4})

	a := scenario.Assertion{Type: "terminal_state_reached", WithinRetries: 5}
	result := assert.Check(a, l)
	if result.Failed {
		t.Fatalf("expected pass: %s", result.Reason)
	}
}

// ---- terminal_state_reached: negative (too many retries) ----

func TestTerminalStateReachedNegativeTooManyRetries(t *testing.T) {
	l := event.New()
	// 10 retries, terminal state on attempt 11 — exceeds within_retries=5.
	for i := int64(1); i <= 10; i++ {
		l.Record(event.Event{Kind: event.KindRequestSent, MsgID: i})
		l.Record(event.Event{Kind: event.KindResponseReceived, MsgID: i})
	}
	l.Record(event.Event{Kind: event.KindTerminalState, MsgID: 11})

	a := scenario.Assertion{Type: "terminal_state_reached", WithinRetries: 5}
	result := assert.Check(a, l)
	if !result.Failed {
		t.Fatal("expected failure: terminal state reached but exceeded within_retries")
	}
}

// ---- terminal_state_reached: negative (never reached) ----

func TestTerminalStateReachedNegativeNeverReached(t *testing.T) {
	l := event.New()
	for i := int64(1); i <= 3; i++ {
		l.Record(event.Event{Kind: event.KindRequestSent, MsgID: i})
		l.Record(event.Event{Kind: event.KindResponseReceived, MsgID: i})
	}
	// No terminal state event.

	a := scenario.Assertion{Type: "terminal_state_reached", WithinRetries: 5}
	result := assert.Check(a, l)
	if !result.Failed {
		t.Fatal("expected failure: terminal state never reached")
	}
}

// ---- effect_without_checkpoint_commit: positive (effect has commit) ----

func TestEffectWithoutCheckpointPositive(t *testing.T) {
	l := event.New()
	// Request → response delivered → checkpoint commit.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Tool: "charge_card"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 1, Tool: "charge_card"})
	l.Record(event.Event{Kind: event.KindCheckpointCommit, MsgID: 1, Tool: "charge_card"})

	a := scenario.Assertion{Type: "effect_without_checkpoint_commit"}
	result := assert.Check(a, l)
	if result.Failed {
		t.Fatalf("expected pass: %s", result.Reason)
	}
}

// ---- effect_without_checkpoint_commit: negative (no commit) ----

func TestEffectWithoutCheckpointNegative(t *testing.T) {
	l := event.New()
	// Request → response delivered, but NO checkpoint commit.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Tool: "charge_card"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 1, Tool: "charge_card"})
	// No KindCheckpointCommit event.

	a := scenario.Assertion{Type: "effect_without_checkpoint_commit"}
	result := assert.Check(a, l)
	if !result.Failed {
		t.Fatal("expected failure: effect without checkpoint commit")
	}
}

// ---- effect_without_checkpoint_commit: dropped response = no effect ----

func TestEffectWithoutCheckpointDroppedNoEffect(t *testing.T) {
	l := event.New()
	// Request → response DROPPED (in_doubt) — no effect observed by agent.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Tool: "charge_card"})
	l.Record(event.Event{Kind: event.KindResponseDropped, MsgID: 1})
	// No commit, but also no delivered effect.

	a := scenario.Assertion{Type: "effect_without_checkpoint_commit"}
	result := assert.Check(a, l)
	if result.Failed {
		t.Fatalf("expected pass (dropped = no effect): %s", result.Reason)
	}
}

// ---- CheckAll: multiple assertions ----

func TestCheckAllMultiple(t *testing.T) {
	l := event.New()
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Key: "key-A"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 1, Tool: "charge_card"})
	l.Record(event.Event{Kind: event.KindTerminalState, MsgID: 1})
	// No checkpoint commit for the charge_card effect.

	assertions := []scenario.Assertion{
		{Type: "no_duplicate_effect", Key: "idempotency_key"},
		{Type: "terminal_state_reached", WithinRetries: 5},
		{Type: "effect_without_checkpoint_commit"},
	}
	results := assert.CheckAll(assertions, l)
	// First two pass, third fails.
	if results[0].Failed {
		t.Fatalf("a0 should pass: %s", results[0].Reason)
	}
	if results[1].Failed {
		t.Fatalf("a1 should pass: %s", results[1].Reason)
	}
	if !results[2].Failed {
		t.Fatal("a2 should fail (effect without commit)")
	}
}

// ---- unknown assertion type ----

func TestUnknownAssertionType(t *testing.T) {
	l := event.New()
	a := scenario.Assertion{Type: "something_custom"}
	result := assert.Check(a, l)
	if !result.Failed {
		t.Fatal("expected failure for unknown assertion type")
	}
}

// ---- custom verifier hook ----

func TestCustomVerifier(t *testing.T) {
	// Register a custom verifier that checks if any events were recorded.
	assert.RegisterCustom("has_events", func(a scenario.Assertion, log *event.Log) assert.Result {
		if log.Len() == 0 {
			return assert.Result{Failed: true, Reason: "no events recorded"}
		}
		return assert.Result{Failed: false}
	})

	l := event.New()
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1})

	result := assert.Check(scenario.Assertion{Type: "has_events"}, l)
	if result.Failed {
		t.Fatalf("custom verifier should pass: %s", result.Reason)
	}

	emptyLog := event.New()
	result = assert.Check(scenario.Assertion{Type: "has_events"}, emptyLog)
	if !result.Failed {
		t.Fatal("custom verifier should fail on empty log")
	}
}

// ---- custom verifier overrides built-in ----

func TestCustomVerifierOverridesBuiltIn(t *testing.T) {
	// Register a custom no_duplicate_effect that always passes.
	assert.RegisterCustom("no_duplicate_effect", func(a scenario.Assertion, log *event.Log) assert.Result {
		return assert.Result{Failed: false}
	})

	l := event.New()
	// Duplicate effect — built-in would fail, but custom overrides.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Key: "dup"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 1})
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 2, Key: "dup"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 2})

	result := assert.Check(scenario.Assertion{Type: "no_duplicate_effect", Key: "idempotency_key"}, l)
	if result.Failed {
		t.Fatal("custom override should make it always pass")
	}
}
