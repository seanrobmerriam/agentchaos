# Well-Known Notifications

The agent can emit a JSON-RPC notification with method
`notifications/agentchaos/event` to record events into the proxy's
event log. The proxy consumes the notification (does not forward it)
and translates the `params.kind` into the corresponding event log
entry. This is the only built-in path for producing
`KindCheckpointCommit` and `KindTerminalState` events.

## Schema

```jsonc
{
  "jsonrpc": "2.0",
  "method": "notifications/agentchaos/event",
  "params": {
    "kind": "<kind>",
    /* ... per-kind fields below ... */
  }
}
```

## Kinds

| `kind`              | Recorded event       | Extra params                                  |
|---------------------|----------------------|------------------------------------------------|
| `checkpoint_commit` | `KindCheckpointCommit` | `tool`, `msg_id`, `key`                       |
| `terminal_state`    | `KindTerminalState`    | `tool`, `key`                                 |

## Example

```jsonc
// Agent emits once a durable checkpoint has been committed.
{
  "jsonrpc": "2.0",
  "method": "notifications/agentchaos/event",
  "params": {
    "kind": "checkpoint_commit",
    "tool": "charge_card",
    "msg_id": 7,
    "key": "idk-1"
  }
}
```

## Semantics

- The proxy NEVER forwards the notification in either direction.
- An unknown `kind` is silently dropped (no event, no forward) — the
  proxy only recognises the kinds above.
- A malformed notification (JSON parse error or no recognisable
  `kind`) is silently dropped.
- The event log entry carries `Source = "well-known-notification"`
  to distinguish it from fault-internal events.

## Wire topology

```
Agent ── notifications/agentchaos/event ──> Proxy (consumed, logged)
                                          │
                                          ▼
                              event.Log entries (KindCheckpointCommit,
                                                  KindTerminalState)
```

The agent side is unaffected: no bytes cross the proxy to the upstream,
and no reply is generated (notifications never receive replies per
JSON-RPC 2.0 §5).
