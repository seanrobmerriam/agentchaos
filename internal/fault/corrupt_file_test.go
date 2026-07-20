package fault_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
)

// TestCorruptFileShortRead verifies that corruptFile tolerates a read that
// returns fewer bytes than requested (e.g. a checkpoint file shorter than
// offset+n). The bytes actually read must still be flipped, and the
// returned corruptResult must report Short=true with the correct counts.
func TestCorruptFileShortRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ckpt.db")
	original := []byte("ABCDEFGH") // 8 bytes total
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Ask for 32 bytes starting at offset 0; only 8 exist.
	res := fault.CorruptFileForTest(path, 0, 32)
	if !res.Short {
		t.Fatalf("expected Short=true, got %+v", res)
	}
	if res.Requested != 32 {
		t.Fatalf("Requested: want 32 got %d", res.Requested)
	}
	if res.Corrupted != len(original) {
		t.Fatalf("Corrupted: want %d got %d", len(original), res.Corrupted)
	}
	if res.Error != nil {
		t.Fatalf("Error: want nil got %v", res.Error)
	}

	// The bytes that were read must be flipped (XOR 0xFF).
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for i := range got {
		if got[i] == original[i] {
			t.Fatalf("byte %d unchanged (0x%02x); original=0x%02x", i, got[i], original[i])
		}
	}
}
