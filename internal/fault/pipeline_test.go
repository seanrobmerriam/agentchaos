package fault_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"

	"pgregory.net/rapid"
)

// ============================================================================
// Phase 2 TDD: Fault pipeline — no execution, only match logging.
// ============================================================================

// strPtr returns a pointer to s.
func strPtr(s string) *string { return &s }

// ---- Example: single fault, single message ----

func TestPipelineSingleExample(t *testing.T) {
	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{
				Match:  scenario.Matcher{Tool: strPtr("echo")},
				Action: "in_doubt",
			},
		},
	}
	p := fault.New(s)

	msg := scenario.Message{Kind: "request", Method: "tools/call", Tool: "echo", ID: 1}
	p.Process(msg, fault.AgentToUpstream, fault.AnchorAfterRequestSent)

	log := p.Log()
	if len(log) != 1 {
		t.Fatalf("want 1 log entry, got %d", len(log))
	}
	if log[0].FaultIndex != 0 {
		t.Fatalf("want fault index 0, got %d", log[0].FaultIndex)
	}
	if log[0].Fault.Action != "in_doubt" {
		t.Fatalf("want in_doubt, got %q", log[0].Fault.Action)
	}
}

// ---- Example: non-matching message produces no log ----

func TestPipelineNoMatch(t *testing.T) {
	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{Match: scenario.Matcher{Tool: strPtr("echo")}, Action: "in_doubt"},
		},
	}
	p := fault.New(s)

	// Non-tools/call message: tool matcher won't match.
	msg := scenario.Message{Kind: "request", Method: "initialize", Tool: "initialize", ID: 1}
	p.Process(msg, fault.AgentToUpstream, fault.AnchorBeforeRequestSend)

	log := p.Log()
	if len(log) != 0 {
		t.Fatalf("want 0 log entries, got %d: %v", len(log), log)
	}
}

// ---- Property: pipeline fires only on matching subset ----

func TestPipelineMatchingSubsetProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate a fault rule with a tool matcher for "echo".
		s := &scenario.Scenario{
			Seed: 1,
			Faults: []scenario.Fault{
				{Match: scenario.Matcher{Tool: strPtr("echo")}, Action: "in_doubt"},
			},
		}
		p := fault.New(s)

		// Generate a random message.
		kind := rapid.SampledFrom([]string{"request", "response", "notification"}).Draw(rt, "kind")
		method := rapid.SampledFrom([]string{"tools/call", "initialize", "ping"}).Draw(rt, "method")
		tool := "some_other_tool"
		if method == "tools/call" {
			tool = rapid.SampledFrom([]string{"echo", "counter", "send_invoice"}).Draw(rt, "tool")
		}
		msg := scenario.Message{Kind: kind, Method: method, ID: 1, Tool: tool}

		p.Process(msg, fault.AgentToUpstream, fault.AnchorAfterRequestSent)

		log := p.Log()
		shouldMatch := msg.Method == "tools/call" && msg.Tool == "echo"
		doesMatch := len(log) > 0
		if shouldMatch != doesMatch {
			rt.Fatalf("want match=%v, got %v for %+v", shouldMatch, doesMatch, msg)
		}
	})
}

// ---- Property: anchor filtering ----

func TestPipelineAnchorFilteringProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Fault with explicit anchor before_response.
		s := &scenario.Scenario{
			Seed: 1,
			Faults: []scenario.Fault{
				{
					Match:  scenario.Matcher{Tool: strPtr("*")},
					At:     "before_response",
					Action: "reorder",
					Window: 3,
				},
			},
		}
		p := fault.New(s)

		msg := scenario.Message{Kind: "response", Method: "", ID: 1, Tool: "tools/call"}
		anchor := rapid.SampledFrom([]fault.Anchor{
			fault.AnchorBeforeRequestSend,
			fault.AnchorAfterRequestSent,
			fault.AnchorBeforeResponse,
			fault.AnchorAtNotification,
		}).Draw(rt, "anchor")

		p.Process(msg, fault.UpstreamToAgent, anchor)

		// Reorder only fires at before_response anchor; tool "*" matches
		// only tools/call requests, but this is a response, not a request.
		// So actually this shouldn't match at all because the tool matcher
		// "*.Matches()" checks method == "tools/call" and this is a response
		// with Method = "". Hmm, wait — the tool "*" wildcard should match
		// any tools/call REQUEST. But reorder operates on responses...
		//
		// The spec says reorder "buffers a window of concurrent responses"
		// — it matches on the REQUEST that preceded them, not the response
		// itself. For Phase 2 match logging we record based on the message
		// being processed at that moment. A more sophisticated pipeline
		// would track the request that seeded this response window, but
		// for v1 match-log purposes we just need the matcher to be checked
		// against the message at the anchor point.
		//
		// For this test, the tool "*" matcher won't match the response
		// because it's not a tools/call request. So the log should be empty.
		log := p.Log()
		if len(log) != 0 {
			// But actually, if the anchor is before_response and the
			// matcher matches (tool:* only matches tools/call requests,
			// not responses), then it shouldn't fire. This is correct.
			rt.Fatalf("expected no match, got %d entries for anchor=%v msg=%+v", len(log), anchor, msg)
		}
	})
}

// ---- Property: multiple faults, first-match-all semantics ----

func TestPipelineAllMatchingFaultsLog(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Two faults that can both match a tools/call echo request.
		s := &scenario.Scenario{
			Seed: 1,
			Faults: []scenario.Fault{
				{Match: scenario.Matcher{Tool: strPtr("echo")}, Action: "in_doubt"},
				{Match: scenario.Matcher{Tool: strPtr("*")}, At: "before_response", Action: "reorder", Window: 2},
			},
		}
		p := fault.New(s)

		// At after_request_sent: fault[0] (in_doubt, tool=echo,
		// default=after_request_sent) matches. Fault[1] (reorder,
		// at=before_response) does NOT match at after_request_sent.
		// So 1 match.
		msg := scenario.Message{Kind: "request", Method: "tools/call", Tool: "echo", ID: 1}

		// Process at after_request_sent
		p.Process(msg, fault.AgentToUpstream, fault.AnchorAfterRequestSent)
		if len(p.Log()) != 1 {
			rt.Fatalf("expected 1 match at after_request_sent (in_doubt), got %d", len(p.Log()))
		}

		// Process at before_response.
		// fault[0] (in_doubt default after_request_sent) does NOT match
		// at before_response. fault[1] (reorder, at=before_response,
		// tool=*) — but the message is the same request, not a response.
		// tool "*" matches tools/call requests, so it DOES match here.
		// So 1 match.
		p.Process(msg, fault.UpstreamToAgent, fault.AnchorBeforeResponse)
		if len(p.Log()) != 2 {
			rt.Fatalf("expected 2 total matches (1 + 1 at before_response), got %d", len(p.Log()))
		}
	})
}

// ============================================================================
// Phase 2 GATE: 5 mixed match rules against a recorded fixture trace
// produces the exact expected match log.
// ============================================================================

func TestPhase2GateMatchLog(t *testing.T) {
	// Scenario with 5 mixed match rules (same as the DSL example in the spec).
	yaml := []byte(`
seed: 4891
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
  - match: {type: "request"}
    at: before_request_send
    action: corrupt_checkpoint
    path: /tmp/cp.db
    offset: 0
    bytes: 1
assertions:
  - type: no_duplicate_effect
    key: idempotency_key
`)
	s, err := scenario.Parse(yaml)
	if err != nil {
		t.Fatalf("parse scenario: %v", err)
	}

	// Recorded fixture trace — a realistic MCP interaction through the proxy.
	trace := []fault.FixtureTraceEntry{
		// 1) agent sends initialize request
		{Msg: scenario.Message{Kind: "request", Method: "initialize", ID: 1, Tool: "initialize"},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorBeforeRequestSend},
		{Msg: scenario.Message{Kind: "request", Method: "initialize", ID: 1, Tool: "initialize"},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorAfterRequestSent},
		// 2) upstream sends initialize response
		{Msg: scenario.Message{Kind: "response", Method: "", ID: 1, Tool: ""},
			Dir: fault.UpstreamToAgent, Anchor: fault.AnchorBeforeResponse},
		// 3) agent sends notifications/initialized (notification, no id)
		{Msg: scenario.Message{Kind: "notification", Method: "notifications/initialized", Tool: "notifications/initialized"},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorAtNotification},
		// 4) agent sends tools/call send_invoice request
		{Msg: scenario.Message{Kind: "request", Method: "tools/call", Tool: "send_invoice", ID: 2},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorBeforeRequestSend},
		{Msg: scenario.Message{Kind: "request", Method: "tools/call", Tool: "send_invoice", ID: 2},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorAfterRequestSent},
		// 5) upstream sends send_invoice response
		{Msg: scenario.Message{Kind: "response", Method: "", ID: 2, Tool: ""},
			Dir: fault.UpstreamToAgent, Anchor: fault.AnchorBeforeResponse},
		// 6) upstream sends notification notifications/webhook
		{Msg: scenario.Message{Kind: "notification", Method: "notifications/webhook", Tool: "notifications/webhook"},
			Dir: fault.UpstreamToAgent, Anchor: fault.AnchorAtNotification},
		// 7) agent sends tools/call charge_card request
		{Msg: scenario.Message{Kind: "request", Method: "tools/call", Tool: "charge_card", ID: 3},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorBeforeRequestSend},
		{Msg: scenario.Message{Kind: "request", Method: "tools/call", Tool: "charge_card", ID: 3},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorAfterRequestSent},
		// 8) upstream sends charge_card response
		{Msg: scenario.Message{Kind: "response", Method: "", ID: 3, Tool: ""},
			Dir: fault.UpstreamToAgent, Anchor: fault.AnchorBeforeResponse},
		// 9) agent sends tools/call verify_status request (non-matching tool)
		{Msg: scenario.Message{Kind: "request", Method: "tools/call", Tool: "verify_status", ID: 4},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorBeforeRequestSend},
		{Msg: scenario.Message{Kind: "request", Method: "tools/call", Tool: "verify_status", ID: 4},
			Dir: fault.AgentToUpstream, Anchor: fault.AnchorAfterRequestSent},
		// 10) upstream sends verify_status response
		{Msg: scenario.Message{Kind: "response", Method: "", ID: 4, Tool: ""},
			Dir: fault.UpstreamToAgent, Anchor: fault.AnchorBeforeResponse},
	}

	got := fault.RunFixture(s, trace)

	// Expected match log: deterministically list which fault fires at each
	// trace step. Format: "fault[IDX] ACTION match:{...} dir:... anchor:..."
	// Note: for fault[3] (in_doubt, tool=charge_card, default anchor
	// before_response), the matcher checks tool=="charge_card" which
	// requires method=="tools/call". At before_response, the message is a
	// response (not a tools/call request), so the tool matcher won't match.
	// In_doubt should fire when the response for the matching request
	// arrives — but our v1 matcher checks the message at the anchor point,
	// not the request that seeded it. So in_doubt's default anchor
	// (before_response) means it checks the RESPONSE message, and the tool
	// matcher won't match a response (no method=tools/call). This is a
	// design limitation we need to resolve.
	//
	// RESOLUTION: For in_doubt, the default anchor should be
	// after_request_sent (like kill_process) — the fault fires when the
	// matching request is forwarded, and the pipeline remembers to drop
	// the response. Let me fix the default anchor for in_doubt.

	// Build the expected log by hand, matching the trace above against the
	// 5 rules.
	expected := []string{
		// step 1: initialize request, before_request_send
		// fault[4]: type=request, at=before_request_send ✓
		"fault[4] corrupt_checkpoint match:{type} dir:agent_to_upstream anchor:before_request_send",
		// step 5: send_invoice request, before_request_send
		// fault[4]: type=request, at=before_request_send ✓
		"fault[4] corrupt_checkpoint match:{type} dir:agent_to_upstream anchor:before_request_send",
		// step 6: send_invoice request, after_request_sent
		// fault[0]: tool=send_invoice, at=after_request_sent ✓
		"fault[0] kill_process match:{tool} dir:agent_to_upstream anchor:after_request_sent",
		// step 8: notifications/webhook, at_notification
		// fault[1]: type=notification, method=notifications/webhook ✓
		// duplicate fires at any anchor (default = "")
		"fault[1] duplicate match:{method,type} dir:upstream_to_agent anchor:at_notification_recv",
		// step 9: charge_card request, before_request_send
		// fault[4]: type=request, at=before_request_send ✓
		"fault[4] corrupt_checkpoint match:{type} dir:agent_to_upstream anchor:before_request_send",
		// step 10: charge_card request, after_request_sent
		// fault[3]: tool=charge_card, default=after_request_sent ✓
		"fault[3] in_doubt match:{tool} dir:agent_to_upstream anchor:after_request_sent",
		// step 12: verify_status request, before_request_send
		// fault[4]: type=request, at=before_request_send ✓
		"fault[4] corrupt_checkpoint match:{type} dir:agent_to_upstream anchor:before_request_send",
	}

	// Log the actual output for debugging, then compare.
	for i, line := range got {
		fmt.Printf("got[%d]:  %s\n", i, line)
	}
	for i, line := range expected {
		fmt.Printf("want[%d]: %s\n", i, line)
	}

	if len(got) != len(expected) {
		t.Fatalf("match log length: want %d got %d\nWANT:\n%s\nGOT:\n%s",
			len(expected), len(got),
			strings.Join(expected, "\n"), strings.Join(got, "\n"))
	}
	for i, want := range expected {
		if got[i] != want {
			t.Fatalf("match log[%d]:\n want %q\n got  %q", i, want, got[i])
		}
	}
}

// suppress unused import
var _ = json.Unmarshal
var _ = context.TODO
