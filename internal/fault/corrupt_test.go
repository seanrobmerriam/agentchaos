package fault_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

func TestExecuteCorruptCheckpoint(t *testing.T) {
	// Create a temp file with known contents.
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.db")
	original := []byte("Hello, World! This is a checkpoint file.")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{
				Match:  scenario.Matcher{Tool: strPtrn("charge_card")},
				Action: "corrupt_checkpoint",
				Path:   path,
				Offset: 7,
				Bytes:  5,
			},
		},
	}
	ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)

	msg := scenario.Message{Kind: "request", Method: "tools/call", Tool: "charge_card", ID: 1}
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"charge_card"}}`)
	forward, killed := ex.ProcessForward(msg, raw, fault.AgentToUpstream)
	if killed {
		t.Fatal("corrupt_checkpoint should not kill")
	}
	if len(forward) != 1 {
		t.Fatalf("want 1 forward, got %d", len(forward))
	}

	// Read the file back and verify bytes were corrupted.
	corrupted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Bytes 0-6 should be unchanged.
	for i := 0; i < 7; i++ {
		if corrupted[i] != original[i] {
			t.Fatalf("byte %d should be unchanged: want %q got %q", i, original[i], corrupted[i])
		}
	}

	// Bytes 7-11 should be flipped (XOR 0xFF).
	for i := 7; i < 12; i++ {
		if corrupted[i] == original[i] {
			// All-zero bytes XOR 0xFF = 0xFF, which is different. But if the
			// original byte was 0xFF, it'd XOR to 0x00. So the check should
			// be: the byte changed. But actually XOR 0xFF always changes the
			// byte unless the byte is 0x00... wait, 0x00 XOR 0xFF = 0xFF
			// (changed). 0xFF XOR 0xFF = 0x00 (changed). So XOR 0xFF always
			// changes the byte. Therefore the corrupted byte must differ.
			t.Fatalf("byte %d should have been corrupted: want changed, got same (0x%02x)", i, corrupted[i])
		}
	}

	// Bytes 12+ should be unchanged.
	for i := 12; i < len(original); i++ {
		if corrupted[i] != original[i] {
			t.Fatalf("byte %d should be unchanged: want 0x%02x got 0x%02x", i, original[i], corrupted[i])
		}
	}

	t.Logf("[corrupt_checkpoint] bytes 7-11 corrupted, rest unchanged")
}

func TestExecuteCorruptCheckpointNoFile(t *testing.T) {
	// Test that corrupting a nonexistent file doesn't crash the executor.
	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{
				Match:  scenario.Matcher{Tool: strPtrn("charge_card")},
				Action: "corrupt_checkpoint",
				Path:   "/nonexistent/path/to/file.db",
				Offset: 0,
				Bytes:  4,
			},
		},
	}
	ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)

	msg := scenario.Message{Kind: "request", Method: "tools/call", Tool: "charge_card", ID: 1}
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"charge_card"}}`)
	forward, killed := ex.ProcessForward(msg, raw, fault.AgentToUpstream)
	if killed {
		t.Fatal("should not kill on missing file")
	}
	if len(forward) != 1 {
		t.Fatalf("want 1 forward, got %d", len(forward))
	}
	t.Logf("[corrupt_checkpoint] missing file handled gracefully")
}