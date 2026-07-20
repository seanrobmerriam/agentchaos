package assert_test

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/assert"
	"github.com/seanrobmerriam/agentchaos/internal/event"
)

func makeLog(events ...event.Event) *event.Log {
	l := event.New()
	for _, e := range events {
		l.Record(e)
	}
	return l
}

func TestDSLCountKind(t *testing.T) {
	log := makeLog(
		event.Event{Kind: event.KindResponseDelivered, MsgID: 1},
		event.Event{Kind: event.KindResponseDelivered, MsgID: 2},
	)
	ok, err := assert.Evaluate("count(response_delivered) >= 2", log)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected true")
	}
}

func TestDSLCountKindFalse(t *testing.T) {
	log := makeLog(event.Event{Kind: event.KindResponseDelivered, MsgID: 1})
	ok, err := assert.Evaluate("count(response_delivered) >= 2", log)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected false (only 1 event, need >=2)")
	}
}

func TestDSLCountWhereClause(t *testing.T) {
	log := makeLog(
		event.Event{Kind: event.KindFaultFired, Action: "duplicate"},
		event.Event{Kind: event.KindFaultFired, Action: "reorder"},
	)
	ok, err := assert.Evaluate("count(fault_fired where action==duplicate) == 1", log)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected true (1 duplicate fault_fired)")
	}
}

func TestDSLAndOperator(t *testing.T) {
	log := makeLog(
		event.Event{Kind: event.KindResponseDelivered},
		event.Event{Kind: event.KindTerminalState},
	)
	ok, err := assert.Evaluate("count(response_delivered) >= 1 and count(terminal_state) == 1", log)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected true")
	}
}

func TestDSLOrOperator(t *testing.T) {
	log := makeLog(event.Event{Kind: event.KindTerminalState})
	ok, err := assert.Evaluate("count(response_delivered) >= 5 or count(terminal_state) == 1", log)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected true (second branch)")
	}
}

func TestDSLNotOperator(t *testing.T) {
	log := makeLog()
	ok, err := assert.Evaluate("not (count(response_dropped) > 0)", log)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected true (no dropped responses)")
	}
}

func TestDSLParentheses(t *testing.T) {
	log := makeLog(event.Event{Kind: event.KindFaultFired, Action: "duplicate"})
	ok, err := assert.Evaluate("(count(fault_fired) == 1)", log)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected true")
	}
}

func TestDSLIntLiteral(t *testing.T) {
	log := makeLog()
	ok, err := assert.Evaluate("count(fault_fired) == 0", log)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected true (empty log)")
	}
}

func TestDSLUnknownFunctionError(t *testing.T) {
	log := makeLog()
	_, err := assert.Evaluate("foo(response_delivered)", log)
	if err == nil {
		t.Fatal("expected error for unknown function")
	}
}

func TestDSLSyntaxError(t *testing.T) {
	log := makeLog()
	_, err := assert.Evaluate("count(response_delivered", log)
	if err == nil {
		t.Fatal("expected error for missing ')'")
	}
}

func TestDSLChecksDuplicateEffect(t *testing.T) {
	log := makeLog(
		event.Event{Kind: event.KindResponseDelivered, MsgID: 1, Tool: "x"},
		event.Event{Kind: event.KindResponseDelivered, MsgID: 2, Tool: "x"},
	)
	ok, err := assert.Evaluate("count(response_delivered where tool==x) <= 1", log)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected false (2 delivered, limit is 1)")
	}
}
