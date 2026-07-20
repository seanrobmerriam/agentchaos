package scenario_test

import (
	"encoding/json"
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/scenario"

	"pgregory.net/rapid"
)

// ============================================================================
// Phase 2 TDD: Match rules — property-test that each matcher fires on exactly
// the intended message subset and never on others.
//
// A "message" in matcher context is a decoded JSON-RPC frame with the
// following canonical fields extracted:
//   - kind:  "request" | "response" | "notification"
//   - method: string (from JSON-RPC "method")
//   - id:    int64 (from JSON-RPC "id" if present)
//   - tool:  string (from tools/call params.name, or method if not a tool call)
// ============================================================================

// genMessage generates an arbitrary scenario.Message.
func genMessage(t *rapid.T) scenario.Message {
	kind := rapid.SampledFrom([]string{"request", "response", "notification"}).Draw(t, "kind")
	method := rapid.SampledFrom([]string{
		"tools/call", "initialize", "notifications/initialized",
		"notifications/webhook", "tools/list", "ping", "charge_card",
	}).Draw(t, "method")
	idVal := rapid.Int64Range(0, 1000).Draw(t, "id")

	var tool string
	if method == "tools/call" {
		tool = rapid.SampledFrom([]string{"send_invoice", "charge_card", "echo", "counter", "get_sum"}).Draw(t, "tool")
	} else {
		tool = method
	}
	return scenario.Message{
		Kind:   kind,
		Method: method,
		ID:     idVal,
		Tool:   tool,
	}
}

// ---- Property: empty matcher matches ALL messages ----

func TestMatcherEmptyMatchesAll(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := scenario.Matcher{} // empty — matches everything
		msg := genMessage(rt)
		if !m.Matches(msg) {
			rt.Fatalf("empty matcher should match all messages, failed on %+v", msg)
		}
	})
}

// ---- Property: tool matcher "X" matches only tools/call with tool=X ----

func TestMatcherToolExact(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		targetTool := rapid.SampledFrom([]string{"send_invoice", "charge_card"}).Draw(rt, "target_tool")
		tv := targetTool
		m := scenario.Matcher{Tool: &tv}

		msg := genMessage(rt)
		shouldMatch := msg.Method == "tools/call" && msg.Tool == targetTool
		doesMatch := m.Matches(msg)
		if shouldMatch != doesMatch {
			rt.Fatalf("tool matcher %q on msg %+v: want match=%v got %v",
				targetTool, msg, shouldMatch, doesMatch)
		}
	})
}

// ---- Property: tool wildcard "*" matches all tools/call ----

func TestMatcherToolWildcard(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		tv := "*"
		m := scenario.Matcher{Tool: &tv}
		msg := genMessage(rt)
		shouldMatch := msg.Method == "tools/call"
		doesMatch := m.Matches(msg)
		if shouldMatch != doesMatch {
			rt.Fatalf("wildcard tool matcher on msg %+v: want %v got %v",
				msg, shouldMatch, doesMatch)
		}
	})
}

// ---- Property: method matcher matches only that method ----

func TestMatcherMethodExact(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		targetMethod := rapid.SampledFrom([]string{"initialize", "notifications/initialized", "tools/list"}).Draw(rt, "target_method")
		mv := targetMethod
		m := scenario.Matcher{Method: &mv}

		msg := genMessage(rt)
		shouldMatch := msg.Method == targetMethod
		doesMatch := m.Matches(msg)
		if shouldMatch != doesMatch {
			rt.Fatalf("method matcher %q on msg %+v: want %v got %v",
				targetMethod, msg, shouldMatch, doesMatch)
		}
	})
}

// ---- Property: method wildcard "*" matches any method ----

func TestMatcherMethodWildcard(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		mv := "*"
		m := scenario.Matcher{Method: &mv}
		msg := genMessage(rt)
		if !m.Matches(msg) {
			rt.Fatalf("wildcard method should match any message, failed on %+v", msg)
		}
	})
}

// ---- Property: type matcher matches only that kind ----

func TestMatcherTypeExact(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		targetType := rapid.SampledFrom([]string{"request", "response", "notification"}).Draw(rt, "target_type")
		tv := targetType
		m := scenario.Matcher{Type: &tv}

		msg := genMessage(rt)
		shouldMatch := msg.Kind == targetType
		doesMatch := m.Matches(msg)
		if shouldMatch != doesMatch {
			rt.Fatalf("type matcher %q on msg %+v: want %v got %v",
				targetType, msg, shouldMatch, doesMatch)
		}
	})
}

// ---- Property: id matcher matches only messages with that id ----

func TestMatcherIDExact(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		targetID := rapid.Int64Range(0, 1000).Draw(rt, "target_id")
		m := scenario.Matcher{ID: targetID}

		msg := genMessage(rt)
		// id matching only applies to requests and responses (which have
		// ids in JSON-RPC). Notifications have no id conceptually.
		var shouldMatch bool
		if msg.Kind == "notification" {
			shouldMatch = false
		} else {
			shouldMatch = msg.ID == targetID
		}
		doesMatch := m.Matches(msg)
		if shouldMatch != doesMatch {
			rt.Fatalf("id matcher %d on msg %+v: want %v got %v",
				targetID, msg, shouldMatch, doesMatch)
		}
	})
}

// ---- Property: combined AND semantics ----

func TestMatcherAndCombinator(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		mv := "tools/call"
		tv := "*"
		m := scenario.Matcher{
			Method: &mv,
			Tool:   &tv,
		}
		msg := genMessage(rt)
		shouldMatch := msg.Method == "tools/call"
		doesMatch := m.Matches(msg)
		if shouldMatch != doesMatch {
			rt.Fatalf("AND matcher on msg %+v: want %v got %v",
				msg, shouldMatch, doesMatch)
		}
	})
}

// ---- Property: combined tool+type AND semantics ----

func TestMatcherToolAndType(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		tv := "send_invoice"
		tpv := "request"
		m := scenario.Matcher{Tool: &tv, Type: &tpv}
		msg := genMessage(rt)
		shouldMatch := msg.Kind == "request" && msg.Method == "tools/call" && msg.Tool == "send_invoice"
		doesMatch := m.Matches(msg)
		if shouldMatch != doesMatch {
			rt.Fatalf("tool+type matcher on msg %+v: want %v got %v",
				msg, shouldMatch, doesMatch)
		}
	})
}

// ---- Property: id wildcard "*" matches any non-notification ----

func TestMatcherIDWildcard(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := scenario.Matcher{ID: "*"}
		msg := genMessage(rt)
		shouldMatch := msg.Kind != "notification"
		doesMatch := m.Matches(msg)
		if shouldMatch != doesMatch {
			rt.Fatalf("id wildcard on msg %+v: want %v got %v",
				msg, shouldMatch, doesMatch)
		}
	})
}

// ---- Defensive: malformed JSON is tolerated (returns zero Message, false) ----

func TestParseMessageFromJSON(t *testing.T) {
	cases := []struct {
		name   string
		json   string
		kind   string
		method string
		id     int64
		tool   string
	}{
		{
			name:   "request with tool",
			json:   `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`,
			kind:   "request",
			method: "tools/call",
			id:     1,
			tool:   "echo",
		},
		{
			name:   "notification",
			json:   `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			kind:   "notification",
			method: "notifications/initialized",
		},
		{
			name:   "response",
			json:   `{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`,
			kind:   "response",
			method: "", // responses don't have method
			id:     2,
		},
		{
			name: "malformed",
			json: `{bad json}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := scenario.ParseMessage([]byte(c.json))
			if msg.Kind != c.kind {
				t.Fatalf("kind: want %q got %q", c.kind, msg.Kind)
			}
			if msg.Method != c.method {
				t.Fatalf("method: want %q got %q", c.method, msg.Method)
			}
			if msg.ID != c.id {
				t.Fatalf("id: want %d got %d", c.id, msg.ID)
			}
			if c.tool != "" && msg.Tool != c.tool {
				t.Fatalf("tool: want %q got %q", c.tool, msg.Tool)
			}
		})
	}
}

// suppress unused imports if a sub-set of tests is skipped during dev
var _ = json.Unmarshal
