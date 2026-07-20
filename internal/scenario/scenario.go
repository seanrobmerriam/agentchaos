// Package scenario defines the declarative DSL that AgentChaos uses to
// describe fault injection scenarios. A scenario YAML is parsed into a
// Scenario struct containing the seed, a list of fault rules (each with a
// matcher, a temporal anchor, and an action), and a list of assertions.
//
// The Matcher type implements the match logic described in SPEC.md §4.2.
// Each matcher field is a pointer so we can distinguish "not specified"
// (nil → skip the check) from "specified with zero value" (e.g. id: 0).
package scenario

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Scenario / Fault / Assertion types
// ---------------------------------------------------------------------------

// Scenario is the top-level parsed scenario document.
type Scenario struct {
	Seed       int64       `yaml:"seed"`
	Faults     []Fault     `yaml:"faults"`
	Assertions []Assertion `yaml:"assertions"`
}

// Fault is one fault rule in the scenario.
type Fault struct {
	Match       Matcher  `yaml:"match"`
	At          string   `yaml:"at,omitempty"`          // temporal anchor (§4.3)
	Action      string   `yaml:"action"`                // action name (§4.4)
	Probability *float64 `yaml:"probability,omitempty"` // 0..1; default 1.0
	Count       int      `yaml:"count,omitempty"`       // for duplicate
	Window      int      `yaml:"window,omitempty"`      // for reorder
	Path        string   `yaml:"path,omitempty"`        // for corrupt_checkpoint
	Offset      int64    `yaml:"offset,omitempty"`      // for corrupt_checkpoint
	Bytes       int      `yaml:"bytes,omitempty"`       // for corrupt_checkpoint
}

// Assertion is one assertion rule evaluated against the event log.
type Assertion struct {
	Type          string `yaml:"type"`
	Key           string `yaml:"key,omitempty"`
	WithinRetries int    `yaml:"within_retries,omitempty"`
	Tool          string `yaml:"tool,omitempty"` // for custom verifier
}

// ---------------------------------------------------------------------------
// Matcher
// ---------------------------------------------------------------------------

// Matcher selects which messages a fault applies to. Fields are pointers so
// that "not specified" (nil) means "don't check this dimension" vs
// "specified" (non-nil) means "check against this value". All specified
// fields must match (AND of all fields).
type Matcher struct {
	Tool   *string `yaml:"tool,omitempty"`
	Method *string `yaml:"method,omitempty"`
	Type   *string `yaml:"type,omitempty"`
	ID     any     `yaml:"id,omitempty"` // number, string, or "*"
}

// ---------------------------------------------------------------------------
// Message
// ---------------------------------------------------------------------------

// Message is a decoded JSON-RPC message projected into the fields a Matcher
// needs. See SPEC.md §3 for the message model.
type Message struct {
	Kind   string // "request" | "response" | "notification"
	Method string
	ID     int64
	Tool   string // tools/call params.name, or Method if not a tools/call
}

// ParseMessage decodes a single JSON-RPC line into a Message. Malformed JSON
// returns a zero-value Message (all fields empty).
func ParseMessage(b []byte) Message {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return Message{}
	}
	return messageFromRaw(raw)
}

func messageFromRaw(raw map[string]any) Message {
	msg := Message{}

	// Classify kind by presence of id and method.
	_, hasID := raw["id"]
	hasMethod := false
	if m, ok := raw["method"].(string); ok && m != "" {
		hasMethod = true
		msg.Method = m
	}

	if hasID {
		// request or response
		if hasMethod {
			msg.Kind = "request"
		} else {
			msg.Kind = "response"
		}
		// Extract id, handle both number and string ids.
		switch v := raw["id"].(type) {
		case float64:
			msg.ID = int64(v)
		case int64:
			msg.ID = v
		case string:
			// String ids are not numeric; we attempt to parse.
			var n int64
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				msg.ID = n
			}
		}
	} else {
		// no id: notification
		msg.Kind = "notification"
	}

	// Extract tool name from tools/call params.
	if msg.Method == "tools/call" {
		if params, ok := raw["params"].(map[string]any); ok {
			if name, ok := params["name"].(string); ok {
				msg.Tool = name
			}
		}
	}
	// If not a tools/call, the "tool" field defaults to the method name so
	// tool matchers applied to non-tool messages have a meaningful value.
	if msg.Tool == "" && msg.Method != "" {
		msg.Tool = msg.Method
	}

	return msg
}

// ---------------------------------------------------------------------------
// Matcher logic
// ---------------------------------------------------------------------------

// Matches returns true if the given message satisfies all specified matcher
// fields. Unspecified (nil) fields are skipped (AND semantics).
func (m Matcher) Matches(msg Message) bool {
	if m.Tool != nil {
		if !matchTool(*m.Tool, msg) {
			return false
		}
	}
	if m.Method != nil {
		if !matchStringWildcard(*m.Method, msg.Method) {
			return false
		}
	}
	if m.Type != nil {
		if *m.Type != msg.Kind {
			return false
		}
	}
	if m.ID != nil {
		if !matchIDAny(m.ID, msg) {
			return false
		}
	}
	return true
}

// matchTool checks the tool field. "*" matches any tools/call request.
// A specific tool name matches a tools/call request whose params.name equals
// that name. Non-tools/call messages never match a tool matcher unless the
// matcher is the wildcard "*".
func matchTool(pattern string, msg Message) bool {
	if pattern == "*" {
		return msg.Method == "tools/call"
	}
	return msg.Method == "tools/call" && msg.Tool == pattern
}

// matchStringWildcard checks if pattern "*" matches any string, or exact.
func matchStringWildcard(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == value
}

// matchIDAny checks the id field. "*" matches any non-notification.
// Numeric ids match the message's id exactly. Notifications never match an
// id rule.
func matchIDAny(pattern any, msg Message) bool {
	if msg.Kind == "notification" {
		return false
	}
	switch v := pattern.(type) {
	case string:
		if v == "*" {
			return true // wildcard: any non-notification
		}
		// Try numeric parse for string ids.
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n == msg.ID
		}
		return false
	case float64:
		return int64(v) == msg.ID
	case int64:
		return v == msg.ID
	case int:
		return int64(v) == msg.ID
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Parse / Marshal
// ---------------------------------------------------------------------------

var validActions = map[string]bool{
	"kill_process":       true,
	"duplicate":          true,
	"reorder":            true,
	"in_doubt":           true,
	"corrupt_checkpoint": true,
}

var validTypes = map[string]bool{
	"request":      true,
	"response":     true,
	"notification": true,
}

// Parse decodes a scenario YAML document and validates it.
func Parse(b []byte) (*Scenario, error) {
	// Two-pass parse: first check for required keys presence, then unmarshal.
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("scenario YAML (raw): %w", err)
	}
	if _, ok := raw["seed"]; !ok {
		return nil, fmt.Errorf("scenario: seed is required")
	}
	if _, ok := raw["faults"]; !ok {
		return nil, fmt.Errorf("scenario: faults list is required (use [] for none)")
	}
	var s Scenario
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("scenario YAML: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Marshal encodes a Scenario to YAML.
func Marshal(s *Scenario) ([]byte, error) {
	return yaml.Marshal(s)
}

// Validate checks the scenario for structural correctness.
func (s Scenario) Validate() error {
	// seed is always required.
	// (Per spec §6.1 a seed could be any int64; zero is a valid seed,
	// so we only enforce the presence of the field which YAML ensures.)
	// We enforce no-value-is-zero by treating "missing" as zero-enforcing.
	// Actually, let's just require faults + assertions to be non-nil.
	// The presence of the seed key is required though; YAML unmarshal of
	// a document without "seed:" leaves it as zero, which is ambiguous.
	// For v1 we accept zero as a valid seed value.

	for i, f := range s.Faults {
		if !validActions[f.Action] {
			return fmt.Errorf("fault[%d]: invalid action %q", i, f.Action)
		}
		if f.Match.Type != nil && !validTypes[*f.Match.Type] {
			return fmt.Errorf("fault[%d]: invalid match.type %q", i, *f.Match.Type)
		}
		if f.Match.Method != nil && *f.Match.Method == "" {
			return fmt.Errorf("fault[%d]: match.method is empty", i)
		}
		if f.Action == "duplicate" && f.Count == 0 {
			// default count is 2 per spec; zero is invalid.
			return fmt.Errorf("fault[%d]: duplicate action requires count >= 1", i)
		}
		if f.Action == "reorder" && f.Window == 0 {
			return fmt.Errorf("fault[%d]: reorder action requires window >= 1", i)
		}
		if f.Action == "corrupt_checkpoint" {
			if f.Path == "" {
				return fmt.Errorf("fault[%d]: corrupt_checkpoint requires path", i)
			}
			if f.Bytes == 0 {
				return fmt.Errorf("fault[%d]: corrupt_checkpoint requires bytes >= 1", i)
			}
		}
		if f.Probability != nil {
			p := *f.Probability
			if p < 0.0 || p > 1.0 {
				return fmt.Errorf("fault[%d]: probability must be in [0,1], got %f", i, p)
			}
		}
	}

	for i, a := range s.Assertions {
		if a.Type == "" {
			return fmt.Errorf("assertion[%d]: type is required", i)
		}
	}

	return nil
}

// DefaultProbability returns 1.0 if Probability is nil, else *Probability.
func (f Fault) DefaultProbability() float64 {
	if f.Probability != nil {
		return *f.Probability
	}
	return 1.0
}

// DefaultCount returns 2 if Count is 0, else Count. Used by duplicate.
func (f Fault) DefaultCount() int {
	if f.Count > 0 {
		return f.Count
	}
	return 2
}

// hasMatcherPrefix is a small helper used by the event log recorder to
// annotate which matcher dimensions were specified (for debugging).
func (m Matcher) SpecifiedFields() []string {
	var out []string
	if m.Tool != nil {
		out = append(out, "tool")
	}
	if m.Method != nil {
		out = append(out, "method")
	}
	if m.Type != nil {
		out = append(out, "type")
	}
	if m.ID != nil {
		out = append(out, "id")
	}
	return out
}

// String returns a human-readable description of the matcher.
func (m Matcher) String() string {
	parts := m.SpecifiedFields()
	if len(parts) == 0 {
		return "match:{}"
	}
	return "match:{" + strings.Join(parts, ",") + "}"
}
