package fault_test

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/assert"
	"github.com/seanrobmerriam/agentchaos/internal/event"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// ============================================================================
// Phase 5 GATE: a full scenario run against a deliberately buggy toy agent
// (known idempotency bug) fails the relevant assertion; the same scenario
// against a fixed version passes.
//
// The event log is the ground truth that assertions are evaluated against.
// We build it manually here to simulate exactly what a buggy vs fixed agent
// would produce under an in_doubt fault.
// ============================================================================

// simulateBuggyAgent: sends charge_card with key "order-123" (id=1),
// response dropped by in_doubt. Buggy agent retries with the SAME key
// (id=2). Upstream charges twice — two KindResponseDelivered events with
// the same key. This is a duplicate effect.
func simulateBuggyAgent() *event.Log {
	l := event.New()
	// Request 1: charge_card with key "order-123"
	l.Record(event.Event{
		Kind: event.KindRequestSent, MsgID: 1,
		Method: "tools/call", Tool: "charge_card", Key: "order-123",
	})
	// Response 1: delivered upstream→agent (simulating the charge hit)
	// BUT in_doubt drops it. The oracle sees KindResponseDropped.
	l.Record(event.Event{
		Kind: event.KindResponseDropped, MsgID: 1,
		Tool: "charge_card",
	})
	// Request 2 (retry): BUGGY agent reuses the same key "order-123"
	l.Record(event.Event{
		Kind: event.KindRequestSent, MsgID: 2,
		Method: "tools/call", Tool: "charge_card", Key: "order-123",
	})
	// Response 2: delivered (this time the response makes it through)
	l.Record(event.Event{
		Kind: event.KindResponseDelivered, MsgID: 2,
		Tool: "charge_card", Key: "order-123",
	})
	return l
}

// simulateFixedAgent: sends charge_card with key "order-123" (id=1),
// response dropped by in_doubt. Fixed agent retries with a DIFFERENT key
// "order-456" (id=2). No duplicate effect — each key maps to at most one
// delivered response.
func simulateFixedAgent() *event.Log {
	l := event.New()
	// Request 1: charge_card with key "order-123"
	l.Record(event.Event{
		Kind: event.KindRequestSent, MsgID: 1,
		Method: "tools/call", Tool: "charge_card", Key: "order-123",
	})
	// Response 1: dropped by in_doubt (oracle sees it, agent doesn't)
	l.Record(event.Event{
		Kind: event.KindResponseDropped, MsgID: 1,
		Tool: "charge_card",
	})
	// Request 2 (retry): FIXED agent uses a different key "order-456"
	l.Record(event.Event{
		Kind: event.KindRequestSent, MsgID: 2,
		Method: "tools/call", Tool: "charge_card", Key: "order-456",
	})
	// Response 2: delivered
	l.Record(event.Event{
		Kind: event.KindResponseDelivered, MsgID: 2,
		Tool: "charge_card", Key: "order-456",
	})
	return l
}

// simulateBuggyAgentAlsoChargedTwice: the truly buggy case where the
// upstream charges twice and both responses are delivered — two
// KindResponseDelivered events with the same key.
func simulateBuggyAgentDoubleCharge() *event.Log {
	l := event.New()
	// Request 1: charge_card with key "order-123"
	l.Record(event.Event{
		Kind: event.KindRequestSent, MsgID: 1,
		Method: "tools/call", Tool: "charge_card", Key: "order-123",
	})
	// Response 1: delivered (first charge — effect observed)
	l.Record(event.Event{
		Kind: event.KindResponseDelivered, MsgID: 1,
		Tool: "charge_card", Key: "order-123",
	})
	// Request 2 (retry): BUGGY agent reuses same key
	l.Record(event.Event{
		Kind: event.KindRequestSent, MsgID: 2,
		Method: "tools/call", Tool: "charge_card", Key: "order-123",
	})
	// Response 2: delivered (second charge — DUPLICATE EFFECT)
	l.Record(event.Event{
		Kind: event.KindResponseDelivered, MsgID: 2,
		Tool: "charge_card", Key: "order-123",
	})
	return l
}

// ---- Gate test: buggy agent fails no_duplicate_effect ----

func TestPhase5GateBuggyAgentFails(t *testing.T) {
	log := simulateBuggyAgentDoubleCharge()
	assertions := []scenario.Assertion{
		{Type: "no_duplicate_effect", Key: "idempotency_key"},
	}
	results := assert.CheckAll(assertions, log)
	if !results[0].Failed {
		t.Fatalf("BUGGY agent: expected no_duplicate_effect to FAIL. Events: %d", log.Len())
	}
	t.Logf("[phase5 gate] buggy agent caught: %s", results[0].Reason)
}

// ---- Gate test: fixed agent passes no_duplicate_effect ----

func TestPhase5GateFixedAgentPasses(t *testing.T) {
	log := simulateFixedAgent()
	assertions := []scenario.Assertion{
		{Type: "no_duplicate_effect", Key: "idempotency_key"},
	}
	results := assert.CheckAll(assertions, log)
	if results[0].Failed {
		t.Fatalf("FIXED agent: expected no_duplicate_effect to PASS: %s", results[0].Reason)
	}
	t.Logf("[phase5 gate] fixed agent passes assertion")
}

// ---- Gate test: in_doubt scenario with dropped response — no dup ----

func TestPhase5GateInDoubtDroppedNoDuplicate(t *testing.T) {
	log := simulateBuggyAgent() // one dropped, one delivered with same key
	assertions := []scenario.Assertion{
		{Type: "no_duplicate_effect", Key: "idempotency_key"},
	}
	results := assert.CheckAll(assertions, log)
	if results[0].Failed {
		t.Fatalf("expected pass (one dropped, one delivered = no dup): %s", results[0].Reason)
	}
	t.Logf("[phase5 gate] in_doubt with single delivery passes")
}

// ---- Gate test: effect_without_checkpoint_commit catches missing commit ----

func TestPhase5GateEffectWithoutCheckpoint(t *testing.T) {
	l := event.New()
	// Agent sends charge_card, response delivered, but no checkpoint commit.
	l.Record(event.Event{
		Kind: event.KindRequestSent, MsgID: 1,
		Method: "tools/call", Tool: "charge_card",
	})
	l.Record(event.Event{
		Kind: event.KindResponseDelivered, MsgID: 1,
		Tool: "charge_card",
	})

	results := assert.CheckAll([]scenario.Assertion{
		{Type: "effect_without_checkpoint_commit"},
	}, l)
	if !results[0].Failed {
		t.Fatal("expected failure: effect without checkpoint commit")
	}
	t.Logf("[phase5 gate] effect_without_checkpoint caught: %s", results[0].Reason)
}

// ---- Gate test: terminal_state_reached catches missing terminal state ----

func TestPhase5GateTerminalStateNotReached(t *testing.T) {
	l := event.New()
	for i := int64(1); i <= 3; i++ {
		l.Record(event.Event{Kind: event.KindRequestSent, MsgID: i})
		l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: i})
	}

	results := assert.CheckAll([]scenario.Assertion{
		{Type: "terminal_state_reached", WithinRetries: 5},
	}, l)
	if !results[0].Failed {
		t.Fatal("expected failure: terminal state never reached")
	}
	t.Logf("[phase5 gate] terminal_state_reached caught: %s", results[0].Reason)
}

// ---- Full gate: multiple assertions against buggy agent ----

func TestPhase5GateFullBuggyScenario(t *testing.T) {
	// Buggy agent: double charges, never reaches terminal state, and
	// has effects without commits.
	l := event.New()
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Method: "tools/call", Tool: "charge_card", Key: "order-A"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 1, Tool: "charge_card", Key: "order-A"})
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 2, Method: "tools/call", Tool: "charge_card", Key: "order-A"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 2, Tool: "charge_card", Key: "order-A"})

	assertions := []scenario.Assertion{
		{Type: "no_duplicate_effect", Key: "idempotency_key"},
		{Type: "terminal_state_reached", WithinRetries: 5},
		{Type: "effect_without_checkpoint_commit"},
	}
	results := assert.CheckAll(assertions, l)

	// All three should fail.
	for i, r := range results {
		if !r.Failed {
			t.Fatalf("assertion[%d] %q should fail but passed", i, assertions[i].Type)
		}
		t.Logf("[phase5 gate full] assertion[%d] %q failed: %s", i, assertions[i].Type, r.Reason)
	}
}

// ---- Full gate: fixed agent passes all assertions ----

func TestPhase5GateFullFixedScenario(t *testing.T) {
	l := event.New()
	// Agent sends charge_card with unique keys, reaches terminal state,
	// and commits after each effect.
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Method: "tools/call", Tool: "charge_card", Key: "order-A"})
	l.Record(event.Event{Kind: event.KindResponseDelivered, MsgID: 1, Tool: "charge_card", Key: "order-A"})
	l.Record(event.Event{Kind: event.KindCheckpointCommit, MsgID: 1, Tool: "charge_card"})
	l.Record(event.Event{Kind: event.KindTerminalState, MsgID: 1})

	assertions := []scenario.Assertion{
		{Type: "no_duplicate_effect", Key: "idempotency_key"},
		{Type: "terminal_state_reached", WithinRetries: 5},
		{Type: "effect_without_checkpoint_commit"},
	}
	results := assert.CheckAll(assertions, l)

	for i, r := range results {
		if r.Failed {
			t.Fatalf("assertion[%d] %q should pass but failed: %s", i, assertions[i].Type, r.Reason)
		}
		t.Logf("[phase5 gate full fixed] assertion[%d] %q passed", i, assertions[i].Type)
	}
}