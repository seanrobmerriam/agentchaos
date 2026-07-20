package event_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/event"
)

// TestPrintTimelineSnapshot ensures the timeline format is stable.
func TestPrintTimelineSnapshot(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	events := []event.Event{
		{Kind: event.KindRequestSent, Seq: 1, Timestamp: ts, MsgID: 42, Tool: "search", Action: "", Source: "agent"},
		{Kind: event.KindFaultFired, Seq: 2, Timestamp: ts, MsgID: 42, Action: "delay", Source: "fault"},
	}
	var buf bytes.Buffer
	event.PrintTimeline(&buf, events)
	out := buf.String()
	if !strings.Contains(out, "seq  timestamp") {
		t.Errorf("missing header in output:\n%s", out)
	}
	if !strings.Contains(out, "request_sent") {
		t.Errorf("missing request_sent row:\n%s", out)
	}
	if !strings.Contains(out, "fault_fired") {
		t.Errorf("missing fault_fired row:\n%s", out)
	}
	if !strings.Contains(out, "action=delay") {
		t.Errorf("missing action=delay detail:\n%s", out)
	}
}
