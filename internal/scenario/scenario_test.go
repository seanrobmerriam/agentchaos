package scenario_test

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/scenario"

	"pgregory.net/rapid"
)

// idEqual compares two any values that are expected to be numeric after
// YAML round-trip (int64 may become float64).
func idEqual(a, b any) bool {
	switch av := a.(type) {
	case int64:
		switch bv := b.(type) {
		case int64:
			return av == bv
		case int:
			return av == int64(bv)
		case float64:
			return float64(av) == bv
		}
	case int:
		switch bv := b.(type) {
		case int:
			return av == bv
		case int64:
			return int64(av) == bv
		case float64:
			return float64(av) == bv
		}
	case float64:
		switch bv := b.(type) {
		case float64:
			return av == bv
		case int64:
			return av == float64(bv)
		case int:
			return av == float64(bv)
		}
	}
	return false
}

// ============================================================================
// Phase 2 TDD: Scenario DSL parser & matchers
//
// Tests are written to FAIL first (compilation failure — the scenario package
// doesn't exist yet). After the implementation is written, these tests must
// pass.
// ============================================================================

// ---- Example: minimal parse ----

func TestParseMinimalExample(t *testing.T) {
	yaml := []byte(`
seed: 4891
faults:
  - match: {tool: "send_invoice"}
    action: kill_process
    probability: 1.0
assertions: []
`)
	s, err := scenario.Parse(yaml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Seed != 4891 {
		t.Fatalf("seed: want 4891 got %d", s.Seed)
	}
	if len(s.Faults) != 1 {
		t.Fatalf("faults: want 1 got %d", len(s.Faults))
	}
	if len(s.Assertions) != 0 {
		t.Fatalf("assertions: want 0 got %d", len(s.Assertions))
	}
	f := s.Faults[0]
	if f.Action != "kill_process" {
		t.Fatalf("action: want kill_process got %q", f.Action)
	}
	if f.Match.Tool == nil || *f.Match.Tool != "send_invoice" {
		t.Fatalf("match.tool: want send_invoice got %v", f.Match.Tool)
	}
	if f.Probability == nil || *f.Probability != 1.0 {
		t.Fatalf("probability: want 1.0 got %v", f.Match.Tool)
	}
}

// ---- Example: full parse with all fields ----

func TestParseFullExample(t *testing.T) {
	yaml := []byte(`
seed: 42
faults:
  - match: {tool: "send_invoice"}
    at: after_request_sent
    action: kill_process
    probability: 1.0
  - match: {type: "notification", method: "notifications/webhook"}
    action: duplicate
    count: 2
  - match: {tool: "*"}
    at: before_response
    action: reorder
    window: 3
  - match: {tool: "charge_card"}
    action: in_doubt
  - match: {}
    action: corrupt_checkpoint
    path: /tmp/checkpoint.sqlite-wal
    offset: 100
    bytes: 4
assertions:
  - type: no_duplicate_effect
    key: idempotency_key
  - type: terminal_state_reached
    within_retries: 5
`)
	s, err := scenario.Parse(yaml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Seed != 42 {
		t.Fatalf("seed: want 42 got %d", s.Seed)
	}
	if len(s.Faults) != 5 {
		t.Fatalf("faults: want 5 got %d", len(s.Faults))
	}

	// Fault 0: kill_process with explicit at + probability
	if s.Faults[0].Action != "kill_process" {
		t.Fatalf("f0 action: %q", s.Faults[0].Action)
	}
	if s.Faults[0].At != "after_request_sent" {
		t.Fatalf("f0 at: %q", s.Faults[0].At)
	}
	if *s.Faults[0].Match.Tool != "send_invoice" {
		t.Fatalf("f0 tool: %v", s.Faults[0].Match.Tool)
	}

	// Fault 1: duplicate notification
	if s.Faults[1].Action != "duplicate" {
		t.Fatalf("f1 action: %q", s.Faults[1].Action)
	}
	if *s.Faults[1].Match.Type != "notification" {
		t.Fatalf("f1 type: %v", s.Faults[1].Match.Type)
	}
	if *s.Faults[1].Match.Method != "notifications/webhook" {
		t.Fatalf("f1 method: %v", s.Faults[1].Match.Method)
	}
	if s.Faults[1].Count != 2 {
		t.Fatalf("f1 count: %d", s.Faults[1].Count)
	}

	// Fault 2: reorder with wildcard tool
	if s.Faults[2].Action != "reorder" {
		t.Fatalf("f2 action: %q", s.Faults[2].Action)
	}
	if *s.Faults[2].Match.Tool != "*" {
		t.Fatalf("f2 tool: %v", s.Faults[2].Match.Tool)
	}
	if s.Faults[2].At != "before_response" {
		t.Fatalf("f2 at: %q", s.Faults[2].At)
	}
	if s.Faults[2].Window != 3 {
		t.Fatalf("f2 window: %d", s.Faults[2].Window)
	}

	// Fault 3: in_doubt
	if s.Faults[3].Action != "in_doubt" {
		t.Fatalf("f3 action: %q", s.Faults[3].Action)
	}
	if *s.Faults[3].Match.Tool != "charge_card" {
		t.Fatalf("f3 tool: %v", s.Faults[3].Match.Tool)
	}

	// Fault 4: corrupt_checkpoint
	if s.Faults[4].Action != "corrupt_checkpoint" {
		t.Fatalf("f4 action: %q", s.Faults[4].Action)
	}
	if s.Faults[4].Path != "/tmp/checkpoint.sqlite-wal" {
		t.Fatalf("f4 path: %q", s.Faults[4].Path)
	}
	if s.Faults[4].Offset != 100 {
		t.Fatalf("f4 offset: %d", s.Faults[4].Offset)
	}
	if s.Faults[4].Bytes != 4 {
		t.Fatalf("f4 bytes: %d", s.Faults[4].Bytes)
	}

	// Assertions
	if len(s.Assertions) != 2 {
		t.Fatalf("assertions: want 2 got %d", len(s.Assertions))
	}
	if s.Assertions[0].Type != "no_duplicate_effect" {
		t.Fatalf("a0 type: %q", s.Assertions[0].Type)
	}
	if s.Assertions[0].Key != "idempotency_key" {
		t.Fatalf("a0 key: %q", s.Assertions[0].Key)
	}
	if s.Assertions[1].Type != "terminal_state_reached" {
		t.Fatalf("a1 type: %q", s.Assertions[1].Type)
	}
	if s.Assertions[1].WithinRetries != 5 {
		t.Fatalf("a1 within_retries: %d", s.Assertions[1].WithinRetries)
	}
}

// ---- Parse errors ----

func TestParseMissingSeed(t *testing.T) {
	yaml := []byte("faults: []\nassertions: []")
	_, err := scenario.Parse(yaml)
	if err == nil {
		t.Fatal("expected error for missing seed")
	}
}

func TestParseInvalidAction(t *testing.T) {
	yaml := []byte(`
seed: 1
faults:
  - match: {tool: "x"}
    action: do_something_invalid
`)
	_, err := scenario.Parse(yaml)
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
}

// ---- Property: parse round-trips ----

// genMatcher generates arbitrary matcher fields used by the property test.
func genMatcher(t *rapid.T) scenario.Matcher {
	m := scenario.Matcher{}
	if rapid.Bool().Draw(t, "has_tool") {
		v := rapid.SampledFrom([]string{"send_invoice", "charge_card", "*", "echo"}).Draw(t, "tool")
		m.Tool = &v
	}
	if rapid.Bool().Draw(t, "has_method") {
		v := rapid.SampledFrom([]string{"tools/call", "initialize", "notifications/*"}).Draw(t, "method")
		m.Method = &v
	}
	if rapid.Bool().Draw(t, "has_type") {
		v := rapid.SampledFrom([]string{"request", "response", "notification"}).Draw(t, "type")
		m.Type = &v
	}
	if rapid.Bool().Draw(t, "has_id") {
		v := rapid.Int64Range(0, 100).Draw(t, "id")
		m.ID = v
	}
	return m
}

func TestParseRoundTripProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate a random scenario struct, marshal to YAML, parse back, and
		// verify the fields match. For Phase 2 we focus on the match fields
		// since they're what the matchers depend on.
		seed := rapid.Int64Range(0, 1<<40).Draw(rt, "seed")
		m := genMatcher(rt)
		f := scenario.Fault{
			Match:  m,
			Action: "in_doubt",
		}
		s := scenario.Scenario{
			Seed:   seed,
			Faults: []scenario.Fault{f},
		}
		out, err := scenario.Marshal(&s)
		if err != nil {
			rt.Fatalf("marshal: %v", err)
		}
		parsed, err := scenario.Parse(out)
		if err != nil {
			rt.Fatalf("round-trip parse: %v", err)
		}
		if parsed.Seed != seed {
			rt.Fatalf("seed: want %d got %d", seed, parsed.Seed)
		}
		if len(parsed.Faults) != 1 {
			rt.Fatalf("faults len: want 1 got %d", len(parsed.Faults))
		}
		if parsed.Faults[0].Action != "in_doubt" {
			rt.Fatalf("action: want in_doubt got %q", parsed.Faults[0].Action)
		}
		m2 := parsed.Faults[0].Match
		// nil comparison
		if (m.Tool == nil) != (m2.Tool == nil) {
			rt.Fatalf("tool nil mismatch: %v vs %v", m.Tool, m2.Tool)
		}
		if m.Tool != nil && *m.Tool != *m2.Tool {
			rt.Fatalf("tool: want %q got %q", *m.Tool, *m2.Tool)
		}
		if (m.Method == nil) != (m2.Method == nil) {
			rt.Fatalf("method nil mismatch: %v vs %v", m.Method, m2.Method)
		}
		if m.Method != nil && *m.Method != *m2.Method {
			rt.Fatalf("method: want %q got %q", *m.Method, *m2.Method)
		}
		if (m.Type == nil) != (m2.Type == nil) {
			rt.Fatalf("type nil mismatch: %v vs %v", m.Type, m2.Type)
		}
		if m.Type != nil && *m.Type != *m2.Type {
			rt.Fatalf("type: want %q got %q", *m.Type, *m2.Type)
		}
		if (m.ID != nil) != (m2.ID != nil) {
			rt.Fatalf("id nil mismatch: %v vs %v", m.ID, m2.ID)
		}
		if m.ID != nil && m.ID != m2.ID {
			// YAML round-trip may convert int64 to float64; compare numerically.
			if !idEqual(m.ID, m2.ID) {
				rt.Fatalf("id: want %v got %v", m.ID, m2.ID)
			}
		}
	})
}
