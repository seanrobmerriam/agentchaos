package scenario

import "fmt"

// LintSeverity classifies a lint diagnostic.
type LintSeverity string

const (
	// LintError indicates a configuration problem that prevents the
	// scenario from running correctly (exit code 78 from the CLI).
	LintError LintSeverity = "error"
	// LintWarning indicates a stylistic or suspicious-but-valid issue.
	// Warnings do not cause a non-zero exit code from `agentchaos lint`.
	LintWarning LintSeverity = "warning"
)

// LintDiagnostic is one finding from Lint.
//
// Location identifies the offending field using dotted bracket notation,
// e.g. "fault[2].at" or "assertion[0].type".
type LintDiagnostic struct {
	Severity LintSeverity
	Location string
	Message  string
}

// Lint walks a Scenario and reports configuration problems that would not
// be surfaced by yaml unmarshalling but would either be caught at execute
// time or silently misbehave.
//
// Lint never returns an error — it always returns a (possibly empty)
// diagnostic slice. Callers can iterate the slice and decide how to format
// or aggregate the findings.
//
// The set of valid actions, anchors, and match types mirrors the inlined
// constants in scenario.Validate() and fault.ValidAnchors(). Keeping the
// sets inline avoids an import cycle with internal/fault (which already
// depends on this package).
func Lint(s *Scenario) []LintDiagnostic {
	var diags []LintDiagnostic

	if s == nil {
		return diags
	}

	// Canonical anchor set; keep in sync with fault.ValidAnchors().
	validAnchors := map[string]bool{
		"before_request_send":  true,
		"after_request_sent":   true,
		"before_response":      true,
		"at_notification_recv": true,
	}

	for i, f := range s.Faults {
		loc := func(suffix string) string {
			return fmt.Sprintf("fault[%d].%s", i, suffix)
		}

		// Action must be one of the well-known actions.
		if !validActions[f.Action] {
			diags = append(diags, LintDiagnostic{
				Severity: LintError,
				Location: loc("action"),
				Message:  fmt.Sprintf("unknown action %q", f.Action),
			})
		}

		// Anchor (if specified) must be in the known set.
		if f.At != "" && !validAnchors[f.At] {
			diags = append(diags, LintDiagnostic{
				Severity: LintError,
				Location: loc("at"),
				Message:  fmt.Sprintf("unknown anchor %q", f.At),
			})
		}

		// Action-specific field requirements.
		if f.Action == "duplicate" && f.Count < 1 {
			diags = append(diags, LintDiagnostic{
				Severity: LintError,
				Location: loc("count"),
				Message:  "duplicate.count must be >= 1",
			})
		}
		if f.Action == "reorder" && f.Window < 1 {
			diags = append(diags, LintDiagnostic{
				Severity: LintError,
				Location: loc("window"),
				Message:  "reorder.window must be >= 1",
			})
		}
		if f.Action == "corrupt_checkpoint" {
			if f.Path == "" {
				diags = append(diags, LintDiagnostic{
					Severity: LintError,
					Location: loc("path"),
					Message:  "required",
				})
			}
			if f.Bytes < 1 {
				diags = append(diags, LintDiagnostic{
					Severity: LintError,
					Location: loc("bytes"),
					Message:  "must be >= 1",
				})
			}
		}

		// Probability, when specified, must be in [0, 1].
		if f.Probability != nil {
			p := *f.Probability
			if p < 0 || p > 1 {
				diags = append(diags, LintDiagnostic{
					Severity: LintError,
					Location: loc("probability"),
					Message:  "must be in [0,1]",
				})
			}
		}

		// Matcher dimensions.
		if f.Match.Type != nil {
			if !validTypes[*f.Match.Type] {
				diags = append(diags, LintDiagnostic{
					Severity: LintError,
					Location: loc("match.type"),
					Message: fmt.Sprintf("unknown match.type %q (valid: request, response, notification)",
						*f.Match.Type),
				})
			}
		}
		if f.Match.Method != nil && *f.Match.Method == "" {
			diags = append(diags, LintDiagnostic{
				Severity: LintError,
				Location: loc("match.method"),
				Message:  "empty string not allowed",
			})
		}
	}

	for i, a := range s.Assertions {
		if a.Type == "" {
			diags = append(diags, LintDiagnostic{
				Severity: LintError,
				Location: fmt.Sprintf("assertion[%d].type", i),
				Message:  "required",
			})
		}
	}

	return diags
}