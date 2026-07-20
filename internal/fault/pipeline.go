// Package fault implements the fault pipeline: a chain of handlers that
// intercept JSON-RPC messages and can (in Phase 3) inject failures.
//
// Phase 2: no fault execution yet. The pipeline records which fault WOULD
// fire for each message in a match log, driven by the scenario's fault rules.
package fault

import (
	"fmt"
	"strings"

	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// Direction is which way a message is travelling through the proxy.
type Direction string

const (
	// AgentToUpstream — message from the agent runtime to the MCP server.
	AgentToUpstream Direction = "agent_to_upstream"
	// UpstreamToAgent — message from the MCP server to the agent runtime.
	UpstreamToAgent Direction = "upstream_to_agent"
)

// Anchor is the temporal anchor (SPEC.md §4.3) for a fault firing.
type Anchor string

const (
	AnchorBeforeRequestSend Anchor = "before_request_send"
	AnchorAfterRequestSent  Anchor = "after_request_sent"
	AnchorBeforeResponse    Anchor = "before_response"
	AnchorAtNotification    Anchor = "at_notification_recv"
)

// Entry is one record in the match log: a fault rule matched at a specific
// point in the message flow.
type Entry struct {
	FaultIndex int            // index into scenario.Faults
	Fault      scenario.Fault // the fault rule that matched
	Message    scenario.Message
	Direction  Direction
	Anchor     Anchor
}

// String renders a match log entry in a compact, deterministic format.
func (e Entry) String() string {
	return fmt.Sprintf("fault[%d] %s %s dir:%s anchor:%s",
		e.FaultIndex, e.Fault.Action, e.Fault.Match, e.Direction, e.Anchor)
}

// Pipeline is the chain of fault rules that intercept messages. Phase 2 only
// records the match log.
type Pipeline struct {
	scenario *scenario.Scenario
	log      []Entry
}

// New constructs a Pipeline from a parsed scenario.
func New(s *scenario.Scenario) *Pipeline {
	return &Pipeline{scenario: s}
}

// Process feeds one message through the pipeline and records which faults
// would fire. The direction and anchor determine which temporal positions
// are active for this message.
func (p *Pipeline) Process(msg scenario.Message, dir Direction, anchor Anchor) {
	for i, f := range p.scenario.Faults {
		f := f // capture for pointer safety
		if !f.Match.Matches(msg) {
			continue
		}
		// Check temporal anchor compatibility.
		if f.At != "" && f.At != string(anchor) {
			continue
		}
		// If no explicit at, apply the default anchor for the action.
		// An empty default anchor means the action fires at any anchor
		// the message matches (e.g. duplicate can fire on requests,
		// responses, or notifications).
		if f.At == "" {
			def := defaultAnchor(f.Action)
			if def != "" && def != anchor {
				continue
			}
		}
		p.log = append(p.log, Entry{
			FaultIndex: i,
			Fault:      f,
			Message:    msg,
			Direction:  dir,
			Anchor:     anchor,
		})
	}
}

// Log returns the accumulated match log entries.
func (p *Pipeline) Log() []Entry {
	return p.log
}

// LogStrings returns the match log rendered as a slice of single-line strings.
func (p *Pipeline) LogStrings() []string {
	out := make([]string, len(p.log))
	for i, e := range p.log {
		out[i] = e.String()
	}
	return out
}

// defaultAnchor returns the default temporal anchor for an action per SPEC §4.3.
func defaultAnchor(action string) Anchor {
	switch action {
	case "kill_process":
		return AnchorAfterRequestSent
	case "duplicate":
		return "" // fires at any anchor (notifications, responses, requests)
	case "reorder":
		return AnchorBeforeResponse
	case "in_doubt":
		// in_doubt fires when the request is forwarded: the matcher needs
		// to see the request message (method=tools/call, tool=X) to match.
		// The execution layer (Phase 3) remembers to drop the response.
		return AnchorAfterRequestSent
	case "corrupt_checkpoint":
		return AnchorAfterRequestSent
	default:
		return AnchorAfterRequestSent
	}
}

// FixtureTrace is a recorded sequence of Message + Direction + Anchor tuples
// representing a message flow through the proxy. Used by Phase 2 gate tests
// for deterministic "expected match log" comparison.
type FixtureTraceEntry struct {
	Msg    scenario.Message
	Dir    Direction
	Anchor Anchor
}

// RunFixture processes a fixture trace through the pipeline and returns the
// match log strings.
func RunFixture(s *scenario.Scenario, trace []FixtureTraceEntry) []string {
	p := New(s)
	for _, e := range trace {
		p.Process(e.Msg, e.Dir, e.Anchor)
	}
	return p.LogStrings()
}

// String for debugging.
func (p *Pipeline) String() string {
	if len(p.log) == 0 {
		return "Pipeline(empty)"
	}
	return "Pipeline(\n  " + strings.Join(p.LogStrings(), "\n  ") + "\n)"
}
