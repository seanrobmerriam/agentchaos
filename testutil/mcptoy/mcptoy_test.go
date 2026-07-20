package mcptoy

import (
	"bytes"
	"testing"
)

// TestHandleMessageUnknownNotificationNoReply verifies that the toy server
// never writes a response to a JSON-RPC notification (no id), even when the
// notification's method is unknown. Per JSON-RPC 2.0, servers MUST NOT
// reply to notifications.
func TestHandleMessageUnknownNotificationNoReply(t *testing.T) {
	var w bytes.Buffer
	s := New()
	// Notification: no "id" field, unknown method. JSON-RPC servers must not reply.
	s.handleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/unknown"}`+"\n"), &w)
	if w.Len() != 0 {
		t.Fatalf("expected no response to notification, got %d bytes: %q", w.Len(), w.String())
	}
}
