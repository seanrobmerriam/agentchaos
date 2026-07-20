package fault

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/event"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

func TestCheckpointEventFromNotification(t *testing.T) {
	s := &scenario.Scenario{Seed: 1}
	ex, err := NewExecutorForTransport(s, func(int) {}, TransportStdio)
	if err != nil {
		t.Fatalf("NewExecutorForTransport: %v", err)
	}
	raw := []byte(`{"jsonrpc":"2.0","method":"notifications/agentchaos/event","params":{"kind":"checkpoint_commit","tool":"x","msg_id":7,"key":"k1"}}`)
	msg := scenario.ParseMessage(raw)
	out, _ := ex.HandleForwardMessage(msg, raw, AgentToUpstream)
	if len(out) != 0 {
		t.Fatalf("expected notification to be consumed (no forwards); got %d", len(out))
	}
	log := ex.EventLog()
	found := false
	for _, e := range log.Filter(event.KindCheckpointCommit) {
		if e.Tool == "x" && e.MsgID == 7 && e.Key == "k1" && e.Source == "well-known-notification" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a KindCheckpointCommit event with tool=x, msg_id=7, key=k1, source=well-known-notification; got %d events", len(log.Filter(event.KindCheckpointCommit)))
	}
}

func TestTerminalStateEventFromNotification(t *testing.T) {
	s := &scenario.Scenario{Seed: 2}
	ex, err := NewExecutorForTransport(s, func(int) {}, TransportStdio)
	if err != nil {
		t.Fatalf("NewExecutorForTransport: %v", err)
	}
	raw := []byte(`{"jsonrpc":"2.0","method":"notifications/agentchaos/event","params":{"kind":"terminal_state","tool":"y","msg_id":3,"key":"t1"}}`)
	msg := scenario.ParseMessage(raw)
	out, _ := ex.HandleReverseMessage(msg, raw, UpstreamToAgent)
	if len(out) != 0 {
		t.Fatalf("expected notification to be consumed (no forwards); got %d", len(out))
	}
	found := false
	for _, e := range ex.EventLog().Filter(event.KindTerminalState) {
		if e.Tool == "y" && e.MsgID == 3 && e.Key == "t1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected KindTerminalState event with tool=y, msg_id=3, key=t1")
	}
}

func TestUnknownKindDropsSilently(t *testing.T) {
	s := &scenario.Scenario{Seed: 3}
	ex, err := NewExecutorForTransport(s, func(int) {}, TransportStdio)
	if err != nil {
		t.Fatalf("NewExecutorForTransport: %v", err)
	}
	raw := []byte(`{"jsonrpc":"2.0","method":"notifications/agentchaos/event","params":{"kind":"something_else","tool":"z","msg_id":1,"key":"k"}}`)
	out, _ := ex.HandleForwardMessage(scenario.ParseMessage(raw), raw, AgentToUpstream)
	if len(out) != 0 {
		t.Fatalf("expected unknown-kind notification to be dropped (no forwards); got %d", len(out))
	}
	if n := len(ex.EventLog().Filter(event.KindCheckpointCommit, event.KindTerminalState)); n != 0 {
		t.Fatalf("expected no checkpoint/terminal events; got %d", n)
	}
}

func TestMalformedNotificationDropsSilently(t *testing.T) {
	s := &scenario.Scenario{Seed: 4}
	ex, err := NewExecutorForTransport(s, func(int) {}, TransportStdio)
	if err != nil {
		t.Fatalf("NewExecutorForTransport: %v", err)
	}
	// Well-known method but unparseable params payload — the
	// notification should still be dropped (no forwards) without an
	// event being recorded.
	raw := []byte(`{"jsonrpc":"2.0","method":"notifications/agentchaos/event","params":"not-an-object"}`)
	out, _ := ex.HandleForwardMessage(scenario.ParseMessage(raw), raw, AgentToUpstream)
	if len(out) != 0 {
		t.Fatalf("expected malformed well-known notification to be dropped; got %d forwards", len(out))
	}
	if n := len(ex.EventLog().Filter(event.KindCheckpointCommit, event.KindTerminalState)); n != 0 {
		t.Fatalf("expected no events; got %d", n)
	}
}

func TestNonWellKnownMethodStillProcessed(t *testing.T) {
	// Sanity check: a notification whose method is NOT the well-known one
	// must still flow through ProcessForward (i.e. it is forwarded normally).
	s := &scenario.Scenario{Seed: 5}
	ex, err := NewExecutorForTransport(s, func(int) {}, TransportStdio)
	if err != nil {
		t.Fatalf("NewExecutorForTransport: %v", err)
	}
	raw := []byte(`{"jsonrpc":"2.0","method":"notifications/something","params":{}}`)
	out, _ := ex.HandleForwardMessage(scenario.ParseMessage(raw), raw, AgentToUpstream)
	if len(out) != 1 {
		t.Fatalf("expected non-well-known notification to pass through; got %d forwards", len(out))
	}
}
