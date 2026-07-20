package event_test

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/event"
)

// TestNDJSONRoundTrip writes events, then reads them back through
// AppendJSONLine and asserts structural equivalence.
func TestNDJSONRoundTrip(t *testing.T) {
	log := event.New()
	log.Record(event.Event{Kind: event.KindRequestSent, MsgID: 7, Tool: "x"})
	log.Record(event.Event{Kind: event.KindFaultFired, MsgID: 7, Action: "delay", FaultIndex: 1, Raw: []byte("payload")})

	var buf bytes.Buffer
	if err := log.WriteNDJSON(&buf); err != nil {
		t.Fatalf("WriteNDJSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"kind":"request_sent"`) {
		t.Fatalf("missing request_sent kind in output: %q", buf.String())
	}

	// Parse it back.
	round := event.New()
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		if err := round.AppendJSONLine(sc.Bytes()); err != nil {
			t.Fatalf("AppendJSONLine: %v", err)
		}
	}
	got := round.Events()
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Kind != event.KindRequestSent || got[0].MsgID != 7 || got[0].Tool != "x" {
		t.Errorf("event 0 round-trip mismatch: %+v", got[0])
	}
	if got[1].Kind != event.KindFaultFired || got[1].Action != "delay" || got[1].FaultIndex != 1 {
		t.Errorf("event 1 round-trip mismatch: %+v", got[1])
	}
	if string(got[1].Raw) != "payload" {
		t.Errorf("Raw round-trip failed: %q", got[1].Raw)
	}
}
