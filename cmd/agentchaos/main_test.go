package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the absolute path to the repository root by walking up
// from the test's working directory (cmd/agentchaos) until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root from %s", wd)
	return ""
}

func exampleScenario(t *testing.T) string {
	return filepath.Join(repoRoot(t), "scenarios", "example.yaml")
}

func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agentchaos")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, string(out))
	}
	return bin
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func runCLI(t *testing.T, bin string, args ...string) (string, int, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), exitCode(err), err
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

func TestValidate(t *testing.T) {
	bin := buildCLI(t)

	// Happy path: scenarios/example.yaml.
	out, code, err := runCLI(t, bin, "validate", "--scenario", exampleScenario(t))
	if err != nil {
		t.Fatalf("validate: unexpected error: %v\n%s", err, out)
	}
	if code != 0 {
		t.Fatalf("validate happy path: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "valid") {
		t.Fatalf("validate happy path: missing 'valid' in output:\n%s", out)
	}

	// Sad path: garbage input via a temp file.
	dir := t.TempDir()
	scenario := filepath.Join(dir, "s.yaml")
	mustWrite(t, scenario, "this is not yaml: : :")
	out, code, err = runCLI(t, bin, "validate", "--scenario", scenario)
	if err == nil {
		t.Fatalf("validate garbage: expected error, got nil\n%s", out)
	}
	if code != 78 {
		t.Fatalf("validate garbage: expected exit 78, got %d\n%s", code, out)
	}
}

func TestRunExitsZeroOnValid(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	scenario := filepath.Join(dir, "s.yaml")
	mustWrite(t, scenario, "seed: 1\nfaults: []\nassertions: []\n")
	_, code, _ := runCLI(t, bin, "run", "--scenario", scenario, "--upstream", "/bin/cat")
	if code != 0 {
		t.Fatalf("run no-faults/cat: expected exit 0, got %d", code)
	}
}

func TestRunInvalidScenarioExits78(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	scenario := filepath.Join(dir, "s.yaml")
	mustWrite(t, scenario, "this is not yaml: : :")
	_, code, _ := runCLI(t, bin, "run", "--scenario", scenario, "--upstream", "/bin/cat")
	if code != 78 {
		t.Fatalf("run with invalid scenario: expected exit 78, got %d", code)
	}
}

func TestInspectDryRun(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	scenario := filepath.Join(dir, "s.yaml")
	mustWrite(t, scenario, "seed: 1\nfaults:\n  - {action: duplicate, match: {tool: t}, count: 2}\n")
	msgs := filepath.Join(dir, "msgs.jsonl")
	mustWrite(t, msgs, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t"}}
`)
	out, code, err := runCLI(t, bin, "inspect", "--scenario", scenario, "--dry-run", "--messages", msgs)
	if err != nil {
		t.Fatalf("inspect --dry-run: unexpected error: %v\n%s", err, out)
	}
	if code != 0 {
		t.Fatalf("inspect --dry-run: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "duplicate") {
		t.Fatalf("inspect --dry-run: expected schedule line mentioning 'duplicate':\n%s", out)
	}
	if !strings.Contains(out, "forward=1") {
		t.Fatalf("inspect --dry-run: expected forward=1 counter:\n%s", out)
	}
}

func TestInspectDryRunRequiresMessages(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	scenario := filepath.Join(dir, "s.yaml")
	mustWrite(t, scenario, "seed: 1\nfaults: []\n")
	_, code, _ := runCLI(t, bin, "inspect", "--scenario", scenario, "--dry-run")
	if code == 0 {
		t.Fatalf("inspect --dry-run without --messages: expected non-zero exit, got 0")
	}
}

func TestReplayReadsSeedFromFile(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "repro.yaml")
	// The scenario carries seed 4891; replay should use it without --seed flag.
	mustWrite(t, path, "seed: 4891\nfaults: []\nassertions: []\n")
	_, code, _ := runCLI(t, bin, "replay", "--scenario", path, "--upstream", "/bin/cat")
	if code != 0 {
		t.Fatalf("replay without --seed: expected 0, got %d", code)
	}
}

func TestInspectOutputs(t *testing.T) {
	bin := buildCLI(t)
	out, code, err := runCLI(t, bin, "inspect", "--scenario", exampleScenario(t))
	if err != nil {
		t.Fatalf("inspect: unexpected error: %v\n%s", err, out)
	}
	if code != 0 {
		t.Fatalf("inspect: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "fault[") {
		t.Fatalf("inspect output missing 'fault[' line:\n%s", out)
	}
}
