package event

import (
	"fmt"
	"io"
)

// PrintTimeline writes a human-readable timeline of events to w.
func PrintTimeline(w io.Writer, events []Event) {
	fmt.Fprintln(w, "seq  timestamp            kind                       msg_id tool     detail")
	for _, e := range events {
		fmt.Fprintf(w, "%-4d %-20s %-25s %-7d %-8s action=%s source=%s\n",
			e.Seq, e.Timestamp.Format("15:04:05.000"), e.Kind, e.MsgID, e.Tool, e.Action, e.Source)
	}
}
