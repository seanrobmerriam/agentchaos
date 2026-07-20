package fault

import (
	"encoding/json"

	"github.com/seanrobmerriam/agentchaos/internal/event"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// wellKnownNotificationMethod is the JSON-RPC notification method that the
// proxy consumes (without forwarding) to record KindCheckpointCommit and
// KindTerminalState events. Agents send this notification to signal durable
// checkpoints and terminal states; the proxy translates each one into an
// event so the corresponding built-in assertions can fire.
const wellKnownNotificationMethod = "notifications/agentchaos/event"

// wellKnownSource identifies events recorded from the well-known
// notifications/agentchaos/event JSON-RPC notification method.
const wellKnownSource = "well-known-notification"

// notificationParams is the on-the-wire shape of notifications/agentchaos/event.
type notificationParams struct {
	Kind  string `json:"kind"`
	Tool  string `json:"tool"`
	MsgID int64  `json:"msg_id"`
	Key   string `json:"key"`
}

// HandleForwardMessage wraps ProcessForward with a fast-path that consumes
// (does not forward) notifications/agentchaos/event notifications. For
// these notifications, it records either a KindCheckpointCommit or
// KindTerminalState event based on params.kind and returns nil (no frames
// to forward). For any other message, it delegates to processForwardLocked.
//
// Returning nil means the pump skips writing anything upstream for this
// notification.
func (ex *Executor) HandleForwardMessage(msg scenario.Message, raw []byte, dir Direction) ([][]byte, bool) {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	if msg.Method == wellKnownNotificationMethod {
		ex.applyWellKnownNotificationLocked(raw, dir)
		return nil, false
	}
	return ex.processForwardLocked(msg, raw, dir)
}

// HandleReverseMessage mirrors HandleForwardMessage for the upstream->agent
// direction. Same well-known consumption semantics.
func (ex *Executor) HandleReverseMessage(msg scenario.Message, raw []byte, dir Direction) ([][]byte, bool) {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	if msg.Method == wellKnownNotificationMethod {
		ex.applyWellKnownNotificationLocked(raw, dir)
		return nil, false
	}
	return ex.processReverseLocked(msg, raw, dir)
}

// applyWellKnownNotificationLocked parses the well-known notification and
// records the corresponding event. The caller MUST hold ex.mu.
//
// Malformed JSON or an unrecognised kind results in no event being
// recorded; the notification is still consumed (not forwarded), on the
// principle that this is a private side-channel that the proxy owns — any
// notification on this method that we cannot interpret should be dropped
// rather than passed through to the other side.
func (ex *Executor) applyWellKnownNotificationLocked(raw []byte, dir Direction) {
	// The well-known notification's payload is inside the JSON-RPC
	// `params` object, so unmarshal the outer envelope first to extract
	// the params blob, then unmarshal that into the typed struct.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}
	var p notificationParams
	if err := json.Unmarshal(envelope["params"], &p); err != nil {
		return
	}
	switch p.Kind {
	case "checkpoint_commit":
		ex.eventLog.Record(event.Event{
			Kind:      event.KindCheckpointCommit,
			MsgID:     p.MsgID,
			Method:    wellKnownNotificationMethod,
			Tool:      p.Tool,
			Key:       p.Key,
			Source:    wellKnownSource,
			Direction: string(dir),
			Raw:       raw,
		})
	case "terminal_state":
		ex.eventLog.Record(event.Event{
			Kind:      event.KindTerminalState,
			MsgID:     p.MsgID,
			Method:    wellKnownNotificationMethod,
			Tool:      p.Tool,
			Key:       p.Key,
			Source:    wellKnownSource,
			Direction: string(dir),
			Raw:       raw,
		})
	default:
		// Unknown kind: drop. We do not forward unrecognised notifications
		// on this method because they are out of schema.
	}
}