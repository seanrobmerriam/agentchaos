package scenario_test

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

func TestLintClean(t *testing.T) {
	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{Action: "kill_process", Match: scenario.Matcher{}, At: "before_response"},
		},
	}
	if diags := scenario.Lint(s); len(diags) != 0 {
		t.Fatalf("expected clean, got %+v", diags)
	}
}

func TestLintCatchesBadAnchor(t *testing.T) {
	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{Action: "duplicate", Match: scenario.Matcher{}, At: "after_request_snt"},
		},
	}
	diags := scenario.Lint(s)
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for bad anchor")
	}
	found := false
	for _, d := range diags {
		if d.Severity == scenario.LintError && d.Location == "fault[0].at" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error diagnostic for fault[0].at, got %+v", diags)
	}
}

func TestLintCatchesBadAction(t *testing.T) {
	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{Action: "frobnicate", Match: scenario.Matcher{}, At: "before_response"},
		},
	}
	diags := scenario.Lint(s)
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic for bad action")
	}
	found := false
	for _, d := range diags {
		if d.Severity == scenario.LintError && d.Location == "fault[0].action" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error diagnostic for fault[0].action, got %+v", diags)
	}
}
