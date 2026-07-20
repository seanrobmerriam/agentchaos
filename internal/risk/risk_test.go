package risk

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRiskRunsSeedsAndReportsJSON(t *testing.T) {
	dir := t.TempDir()
	scenario := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(scenario, []byte("seed: 0\nfaults: []\nassertions: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Run(Options{
		ScenarioPath: scenario,
		Seeds:        4,
		Upstream:     "/bin/cat",
		Parallel:     1,
		Timeout:      5 * time.Second,
	})
	if report == nil {
		t.Fatal("nil report")
	}
	if report.SeedsRun != 4 {
		t.Fatalf("SeedsRun = %d, want 4", report.SeedsRun)
	}
	if report.SeedsFailed != 0 {
		t.Fatalf("SeedsFailed = %d, want 0; failures=%+v", report.SeedsFailed, report.Failures)
	}
	if len(report.Failures) != 0 {
		t.Fatalf("expected no failures, got %+v", report.Failures)
	}
	if report.Timing <= 0 {
		t.Fatalf("Timing = %s, want > 0", report.Timing)
	}
}

func TestRiskReportsFailureOnBadScenario(t *testing.T) {
	dir := t.TempDir()
	scenario := filepath.Join(dir, "bad.yaml")
	// Intentionally malformed: missing closing brace in faults.
	if err := os.WriteFile(scenario, []byte("seed: 0\nfaults: [ { type: bogus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Run(Options{
		ScenarioPath: scenario,
		Seeds:        2,
		Upstream:     "/bin/cat",
		Parallel:     1,
		Timeout:      5 * time.Second,
	})
	if report == nil {
		t.Fatal("nil report")
	}
	if report.SeedsRun != 2 {
		t.Fatalf("SeedsRun = %d, want 2", report.SeedsRun)
	}
	if report.SeedsFailed != 2 {
		t.Fatalf("SeedsFailed = %d, want 2", report.SeedsFailed)
	}
	if len(report.Failures) == 0 {
		t.Fatal("expected at least one failure entry")
	}
}

func TestRiskParallelRuns(t *testing.T) {
	dir := t.TempDir()
	scenario := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(scenario, []byte("seed: 0\nfaults: []\nassertions: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Run(Options{
		ScenarioPath: scenario,
		Seeds:        6,
		Upstream:     "/bin/cat",
		Parallel:     3,
		Timeout:      5 * time.Second,
	})
	if report.SeedsRun != 6 {
		t.Fatalf("SeedsRun = %d, want 6", report.SeedsRun)
	}
	if report.SeedsFailed != 0 {
		t.Fatalf("SeedsFailed = %d, want 0; failures=%+v", report.SeedsFailed, report.Failures)
	}
}

func TestWriteJUnitShape(t *testing.T) {
	rep := &Report{
		SeedsRun:    3,
		SeedsFailed: 1,
		Failures: []Failure{
			{Seed: 2, Reason: "assertion failed: oops", ExitCode: 70},
		},
		Timing: 100 * time.Millisecond,
	}
	var buf bytes.Buffer
	if err := WriteJUnit(&buf, rep); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if !bytes.Contains(out, []byte("<?xml")) {
		t.Fatalf("missing XML header: %s", out)
	}
	if !bytes.Contains(out, []byte(`<testsuites>`)) {
		t.Fatalf("missing testsuites: %s", out)
	}
	if !bytes.Contains(out, []byte(`<failure`)) {
		t.Fatalf("missing failure element: %s", out)
	}

	var parsed JUnitTestSuites
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(parsed.Suites) != 1 {
		t.Fatalf("suites = %d, want 1", len(parsed.Suites))
	}
	s := parsed.Suites[0]
	if s.Tests != 3 || s.Failures != 1 {
		t.Fatalf("suite counts = (%d,%d), want (3,1)", s.Tests, s.Failures)
	}
}
