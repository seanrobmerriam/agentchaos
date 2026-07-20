package event_test

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/event"

	"pgregory.net/rapid"
)

// ---- Example: record and retrieve ----

func TestLogRecordAndRetrieve(t *testing.T) {
	l := event.New()
	e1 := l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1, Method: "tools/call"})
	if e1.Seq != 1 {
		t.Fatalf("seq: want 1 got %d", e1.Seq)
	}
	e2 := l.Record(event.Event{Kind: event.KindResponseReceived, MsgID: 1})
	if e2.Seq != 2 {
		t.Fatalf("seq: want 2 got %d", e2.Seq)
	}
	if l.Len() != 2 {
		t.Fatalf("len: want 2 got %d", l.Len())
	}
	events := l.Events()
	if len(events) != 2 {
		t.Fatalf("events: want 2 got %d", len(events))
	}
	if events[0].Kind != event.KindRequestSent {
		t.Fatalf("events[0].kind: want request_sent got %s", events[0].Kind)
	}
}

// ---- Example: filter ----

func TestLogFilter(t *testing.T) {
	l := event.New()
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 1})
	l.Record(event.Event{Kind: event.KindResponseReceived, MsgID: 1})
	l.Record(event.Event{Kind: event.KindResponseDropped, MsgID: 1})
	l.Record(event.Event{Kind: event.KindRequestSent, MsgID: 2})
	l.Record(event.Event{Kind: event.KindResponseReceived, MsgID: 2})

	dropped := l.Filter(event.KindResponseDropped)
	if len(dropped) != 1 {
		t.Fatalf("dropped: want 1 got %d", len(dropped))
	}

	requests := l.Filter(event.KindRequestSent)
	if len(requests) != 2 {
		t.Fatalf("requests: want 2 got %d", len(requests))
	}

	both := l.Filter(event.KindRequestSent, event.KindResponseReceived)
	if len(both) != 4 {
		t.Fatalf("both: want 4 got %d", len(both))
	}
}

// ---- Property: sequence numbers are monotonic ----

func TestLogSeqMonotonicProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 100).Draw(rt, "n")
		l := event.New()
		for i := 0; i < n; i++ {
			e := l.Record(event.Event{Kind: event.KindRequestSent, MsgID: int64(i)})
			if e.Seq != i+1 {
				rt.Fatalf("seq[%d]: want %d got %d", i, i+1, e.Seq)
			}
		}
		events := l.Events()
		for i := 1; i < len(events); i++ {
			if events[i].Seq <= events[i-1].Seq {
				rt.Fatalf("non-monotonic seq at %d: %d <= %d", i, events[i].Seq, events[i-1].Seq)
			}
		}
	})
}
