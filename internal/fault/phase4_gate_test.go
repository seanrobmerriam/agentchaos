package fault_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// ============================================================================
// Phase 4 GATE: `agentchaos replay --seed N --scenario X` reproduces a prior
// run's fault log exactly.
//
// Strategy: run the proxy twice with the same seed — once via `run` and once
// via `replay` — and compare the fault schedules they emit to stderr. Both
// must be byte-identical.
//
// The CLI emits the fault schedule to stderr when AGENTCHAOS_DEBUG=1 is set,
// prefixed with "[schedule]". This provides deterministic observability for
// CI replay verification.
// ============================================================================

func TestPhase4GateReplayReproducesSchedule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}

	binPath, err := buildProxyBinary(t)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	scenarioFile := t.TempDir() + "/det.yaml"
	writeFile(t, scenarioFile, []byte(`
seed: 4891
faults:
  - match: {tool: "counter"}
    action: in_doubt
    probability: 0.5
  - match: {type: "response"}
    at: before_response
    action: duplicate
    count: 2
assertions: []
`))

	// Input: initialize + 5 counter calls
	input := []byte(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n",
	)
	for i := 2; i <= 6; i++ {
		input = append(input, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"counter"}}`+"\n", i))...)
	}

	// Run 1: `run` with default seed
	cmd1 := exec.Command(binPath, "run", "--scenario", scenarioFile,
		"--upstream", "npx -y @modelcontextprotocol/server-everything stdio")
	cmd1.Stdin = bytes.NewReader(input)
	cmd1.Env = append(os.Environ(), "AGENTCHAOS_DEBUG=1")
	var stderr1 bytes.Buffer
	cmd1.Stderr = &stderr1
	var stdout1 bytes.Buffer
	cmd1.Stdout = &stdout1
	_ = cmd1.Run()

	schedule1 := extractSchedule(stderr1.String())

	// Run 2: `replay` with seed=4891
	cmd2 := exec.Command(binPath, "replay", "--seed", "4891", "--scenario", scenarioFile,
		"--upstream", "npx -y @modelcontextprotocol/server-everything stdio")
	cmd2.Stdin = bytes.NewReader(input)
	cmd2.Env = append(os.Environ(), "AGENTCHAOS_DEBUG=1")
	var stderr2 bytes.Buffer
	cmd2.Stderr = &stderr2
	var stdout2 bytes.Buffer
	cmd2.Stdout = &stdout2
	_ = cmd2.Run()

	schedule2 := extractSchedule(stderr2.String())

	if len(schedule1) == 0 {
		t.Fatalf("run produced empty schedule. stderr: %s", stderr1.String())
	}
	if len(schedule2) == 0 {
		t.Fatalf("replay produced empty schedule. stderr: %s", stderr2.String())
	}
	if len(schedule1) != len(schedule2) {
		t.Fatalf("schedule lengths: run=%d replay=%d\nRUN: %s\nREPLAY: %s",
			len(schedule1), len(schedule2), schedule1, schedule2)
	}
	for i := range schedule1 {
		if schedule1[i] != schedule2[i] {
			t.Fatalf("schedule[%d] differs:\n run   =%s\n replay=%s", i, schedule1[i], schedule2[i])
		}
	}
	t.Logf("[phase4 gate] run and replay produce identical %d-entry schedule", len(schedule1))
}

// extractSchedule pulls [schedule] lines from stderr.
func extractSchedule(stderr string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(stderr))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "[schedule]") {
			out = append(out, line)
		}
	}
	return out
}

// suppress unused
var _ = context.TODO
var _ = time.Second
var _ = io.EOF
var _ = json.Unmarshal
var _ = fault.NewExecutor
var _ = scenario.Parse