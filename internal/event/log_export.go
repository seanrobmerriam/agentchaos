package event

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"time"
)

type eventRecord struct {
	Kind       Kind   `json:"kind"`
	Seq        int    `json:"seq"`
	Timestamp  string `json:"timestamp"`
	MsgID      int64  `json:"msg_id,omitempty"`
	Method     string `json:"method,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Action     string `json:"action,omitempty"`
	FaultIndex int    `json:"fault_index,omitempty"`
	Direction  string `json:"direction,omitempty"`
	Key        string `json:"key,omitempty"`
	Source     string `json:"source,omitempty"`
	RawB64     string `json:"raw_b64,omitempty"`
}

// WriteNDJSON streams the event log to w, one event per line.
func (l *Log) WriteNDJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, e := range l.Events() {
		rec := eventRecord{
			Kind: e.Kind, Seq: e.Seq, Timestamp: e.Timestamp.Format(time.RFC3339Nano),
			MsgID: e.MsgID, Method: e.Method, Tool: e.Tool, Action: e.Action,
			FaultIndex: e.FaultIndex, Direction: e.Direction, Key: e.Key, Source: e.Source,
		}
		if len(e.Raw) > 0 {
			rec.RawB64 = base64.StdEncoding.EncodeToString(e.Raw)
		}
		if err := enc.Encode(rec); err != nil {
			return err
		}
	}
	return nil
}

// AppendJSONLine parses one NDJSON record into an Event and appends.
func (l *Log) AppendJSONLine(b []byte) error {
	var r eventRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	ev := Event{
		Kind: r.Kind, Seq: r.Seq, MsgID: r.MsgID, Method: r.Method,
		Tool: r.Tool, Action: r.Action, FaultIndex: r.FaultIndex,
		Direction: r.Direction, Key: r.Key, Source: r.Source,
	}
	if r.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, r.Timestamp); err == nil {
			ev.Timestamp = t
		}
	}
	if r.RawB64 != "" {
		if data, err := base64.StdEncoding.DecodeString(r.RawB64); err == nil {
			ev.Raw = data
		}
	}
	l.Record(ev)
	return nil
}
