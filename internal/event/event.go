// Package event records the event log for a fault-injection run. The event
// log is the ground truth that assertions are evaluated against after a run
// completes. It captures every message that crossed the wire, every fault
// that fired, and every dropped response the oracle can see.
//
// See SPEC.md §7.
package event

import (
	"sync"
	"time"
)

// Kind classifies an event.
type Kind string

const (
	KindRequestSent           Kind = "request_sent"       // agent sent a request to upstream
	KindResponseReceived      Kind = "response_received"  // upstream response arrived at proxy
	KindResponseDelivered     Kind = "response_delivered" // proxy delivered response to agent
	KindNotificationSent      Kind = "notification_sent"
	KindNotificationDelivered Kind = "notification_delivered"
	KindFaultFired            Kind = "fault_fired"
	KindResponseDropped       Kind = "response_dropped" // in_doubt: response dropped, logged for oracle
	KindResponseDuplicated    Kind = "response_duplicated"
	KindProcessKilled         Kind = "process_killed"
	KindCheckpointCommit      Kind = "checkpoint_commit" // a durable commit was observed
	KindTerminalState         Kind = "terminal_state"    // a terminal state was reached
)

// Event is one entry in the event log.
type Event struct {
	Kind       Kind
	Timestamp  time.Time
	Seq        int    // monotonic sequence number
	MsgID      int64  // JSON-RPC id (0 for notifications)
	Method     string // JSON-RPC method
	Tool       string // tools/call params.name
	Action     string // fault action (for KindFaultFired)
	FaultIndex int    // fault rule index (for KindFaultFired)
	Direction  string // "agent_to_upstream" or "upstream_to_agent"
	Raw        []byte // raw message bytes (for dropped/recorded messages)
	Key        string // idempotency key or checkpoint key (for assertion events)
	Source     string // origin of the event (e.g. "well-known-notification")
}

// Log is a thread-safe append-only event log.
type Log struct {
	mu     sync.Mutex
	events []Event
	seq    int
}

// New creates an empty event log.
func New() *Log {
	return &Log{}
}

// Record appends an event and returns it with the assigned sequence number.
func (l *Log) Record(e Event) Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	l.events = append(l.events, e)
	return e
}

// Events returns a copy of all events in sequence order.
func (l *Log) Events() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

// Len returns the number of events.
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

// Filter returns all events matching the given kinds. An empty kinds slice
// copies every event (preserving the historical "no filter" behaviour); a
// non-empty kinds slice iterates events and matches each Kind against the
// variadic by index, avoiding the per-call map allocation the previous
// implementation incurred.
func (l *Log) Filter(kinds ...Kind) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(kinds) == 0 {
		out := make([]Event, len(l.events))
		copy(out, l.events)
		return out
	}
	out := make([]Event, 0, len(l.events))
	for _, e := range l.events {
		for _, k := range kinds {
			if e.Kind == k {
				out = append(out, e)
				break
			}
		}
	}
	return out
}
