# AgentChaos

> Transparent MCP fault-injection proxy for testing agent recovery logic.

AgentChaos sits between an agent runtime (the MCP client) and one or more
upstream MCP servers. It intercepts every JSON-RPC message crossing the wire
and injects failures according to a declarative scenario: killing the process
mid-tool-call, duplicating a notification, reordering concurrent tool
results, forcing an `IN_DOUBT` outcome, or corrupting a durable checkpoint
file on disk.

Teams claiming durable execution need a way to prove their recovery logic
actually survives real failure conditions — AgentChaos is that proof,
runnable in CI, with a Jepsen-style shrinking step that reduces a failing
seed down to the minimal fault schedule that reproduces the bug.


# What it does

AgentChaos proves recovery logic breaks under **these fault classes** — it is
not an exhaustive correctness proof. Specifically:

- It covers **message-level** faults (drop, duplicate, reorder, kill,
  in_doubt) at the JSON-RPC layer.
- It does **not** cover network-level faults (packet loss, latency below the
  MCP message layer). Pair with `tc` or [Toxiproxy](https://github.com/Shopify/toxiproxy) for that.
- It is **single-proxy, single-upstream-connection** in the current version. Multi-node
  distributed scenarios (faults coordinated across several proxies) will be a
  future addition.
- `corrupt_checkpoint` fidelity depends on knowing the target's on-disk
  format. The primitive is a generic "flip N bytes at offset X in file Y at
  time T" — format-awareness is left to the scenario author.

## Install

```bash
go install github.com/seanrobmerriam/agentchaos/cmd/agentchaos@latest
```

Or build from source:

```bash
git clone https://github.com/seanrobmerriam/agentchaos.git
cd agentchaos
go build -o agentchaos ./cmd/agentchaos
```

## Quick start

```bash
# Run a scenario with 100 random seeds, shrink on failure,
# and write a minimal reproducer:
agentchaos run \
  --scenario scenarios/idempotency.yaml \
  --upstream "npx -y @modelcontextprotocol/server-everything stdio" \
  --seeds 100 \
  --shrink-on-failure \
  --reproducer reproducer.yaml

# Replay a specific seed:
agentchaos replay --seed 4891 \
  --scenario scenarios/idempotency.yaml \
  --upstream "npx -y @modelcontextprotocol/server-everything stdio"

# Validate a scenario file:
agentchaos validate --scenario scenarios/idempotency.yaml

# Inspect a scenario:
agentchaos inspect --scenario scenarios/idempotency.yaml
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | All assertions passed |
| 70 | Assertion failure detected |
| 77 | Process killed by `kill_process` fault |
| 78 | Invalid scenario file |

## Scenario DSL

A scenario is a YAML file with a `seed`, a list of `faults`, and a list of
`assertions`.

```yaml
seed: 4891
faults:
  - match: {tool: "send_invoice"}
    at: after_request_sent
    action: kill_process
    probability: 1.0
  - match: {type: "notification", method: "notifications/webhook"}
    action: duplicate
    count: 2
  - match: {tool: "*"}
    at: before_response
    action: reorder
    window: 3
  - match: {tool: "charge_card"}
    action: in_doubt
assertions:
  - type: no_duplicate_effect
    key: idempotency_key
  - type: terminal_state_reached
    within_retries: 5
```

### Match fields

All specified fields must match (AND semantics). Unspecified fields are
ignored.

| Field | Type | Description |
|-------|------|-------------|
| `tool` | string | Matches `tools/call` requests with `params.name` equal to this value. `"*"` matches any `tools/call` request. Non-`tools/call` messages never match unless the matcher is `"*"` (which only matches `tools/call` requests). |
| `method` | string | Matches the JSON-RPC `method` field. `"*"` matches any method. |
| `type` | string | Matches the message kind: `"request"`, `"response"`, or `"notification"`. |
| `id` | number or `"*"` | Matches the JSON-RPC `id` field. `"*"` matches any non-notification message. |

### Temporal anchors (`at`)

The `at` field specifies _when_ in the message lifecycle the fault fires.
Each action has a default anchor if `at` is omitted:

| Action | Default anchor |
|--------|---------------|
| `kill_process` | `after_request_sent` |
| `duplicate` | any anchor (fires whenever the matcher matches) |
| `reorder` | `before_response` |
| `in_doubt` | `after_request_sent` |
| `corrupt_checkpoint` | `after_request_sent` |

Available anchors:

| Anchor | Description |
|--------|-------------|
| `before_request_send` | Before the proxy forwards a request to upstream |
| `after_request_sent` | After the request has been sent to upstream |
| `before_response` | Before the proxy delivers a response to the agent |
| `at_notification_recv` | When a notification arrives (either direction) |

### Fault primitives

#### `kill_process`

After the matching request is forwarded upstream but before its response
returns, the proxy process exits with code 77. This simulates a crash
mid-tool-call.

```yaml
- match: {tool: "send_invoice"}
  at: after_request_sent
  action: kill_process
  probability: 1.0
```

| Field | Default | Description |
|-------|---------|-------------|
| `probability` | 1.0 | Probability of firing (0.0–1.0). Driven by the seed. |

#### `duplicate`

A notification or response is delivered N times.

```yaml
- match: {type: "notification", method: "notifications/webhook"}
  action: duplicate
  count: 2
```

| Field | Default | Description |
|-------|---------|-------------|
| `count` | 2 | Number of deliveries. |

#### `reorder`

Buffers a window of concurrent responses and releases them out of arrival
order. Only meaningful where multiple requests can be in flight at once
(Streamable HTTP). Under stdio, the transport is inherently ordered, and
`reorder` is an explicit error at executor construction time.

```yaml
- match: {type: "response"}
  at: before_response
  action: reorder
  window: 3
```

| Field | Default | Description |
|-------|---------|-------------|
| `window` | (required) | Number of responses to buffer before releasing in permuted order. |

#### `in_doubt`

The request is forwarded upstream and a response is awaited, but the proxy
silently drops it before relaying it back. The caller is left unable to tell
whether the effect occurred. The dropped response is recorded internally in
the event log (annotated as `[dropped]`) so the oracle has visibility the
agent lacks.

```yaml
- match: {tool: "charge_card"}
  action: in_doubt
```

#### `corrupt_checkpoint`

Filesystem-level, not JSON-RPC-level: flips N bytes at a specified offset in
a specified file at a specified point in the timeline. Format-awareness is
left to the scenario author.

```yaml
- match: {}
  action: corrupt_checkpoint
  path: /tmp/checkpoint.sqlite-wal
  offset: 100
  bytes: 4
```

| Field | Default | Description |
|-------|---------|-------------|
| `path` | (required) | Path to the file to corrupt. |
| `offset` | 0 | Byte offset to start corrupting. |
| `bytes` | (required) | Number of bytes to flip. |

### Assertions

Assertions are evaluated against the recorded event log after the run
completes.

#### `no_duplicate_effect`

No two requests with the same idempotency key produced an observed effect
(response delivered to the agent). A dropped response (via `in_doubt`) does
_not_ count as an observed effect — the agent never saw it.

```yaml
- type: no_duplicate_effect
  key: idempotency_key
```

#### `terminal_state_reached`

A terminal state event was recorded, and the number of request retries
before it is within `within_retries`.

```yaml
- type: terminal_state_reached
  within_retries: 5
```

#### `effect_without_checkpoint_commit`

Every delivered effect (response to a `tools/call`) must have a matching
checkpoint commit event. This catches cases where an effect was observed but
no durable commit was recorded.

```yaml
- type: effect_without_checkpoint_commit
```

#### Custom assertions

User-supplied verifiers can be registered programmatically via
`assert.RegisterCustom`:

```go
import "github.com/seanrobmerriam/agentchaos/internal/assert"

assert.RegisterCustom("my_custom_check", func(a scenario.Assertion, log *event.Log) assert.Result {
    // Custom logic here
    return assert.Result{Failed: false}
})
```

## Determinism

All injected randomness — which fault fires under `probability: <1.0`,
reorder window permutation, timing — is driven by a single seed (SplitMix64
PRNG). Same seed and scenario produce a byte-identical fault schedule.

When a seed produces an assertion failure, `--shrink-on-failure` searches for
a smaller fault schedule — fewer faults, earlier in the run — that still
reproduces the failure. The shrunk scenario is written to the path specified
by `--reproducer`.

## Transports

AgentChaos speaks MCP's two current standard transports:

- **stdio** — the agent connects to the proxy via stdin/stdout; the proxy
  connects to the upstream server via stdin/stdout (subprocess).
- **Streamable HTTP** — the proxy connects to an upstream HTTP server. SSE
  reverse-channel supported via `HTTPOptions.ReverseGET`.

In v1, the agent always speaks stdio to the proxy; the proxy speaks stdio or
Streamable HTTP to the upstream.

## Architecture

```
Agent (MCP client)
   ↕ stdio (newline-delimited JSON-RPC)
AgentChaos proxy
   ↕ stdio or Streamable HTTP
Upstream MCP server
```

The proxy reads each newline-delimited JSON-RPC line, parses it into a
`Message` (kind, method, id, tool), runs it through the fault executor, and
forwards the result(s). Faults can modify, drop, duplicate, or terminate on
matched messages. All events are recorded in an append-only event log for
assertion checking.

## Development

```bash
# Run all tests (including property tests and subprocess integration):
go test -race -count=1 ./...

# Run just the unit tests (skip slow subprocess tests):
go test -short ./...

# Debug a specific seed (emits the fault schedule to stderr):
AGENTCHAOS_DEBUG=1 agentchaos replay --seed 4891 --scenario scenarios/example.yaml \
  --upstream "npx -y @modelcontextprotocol/server-everything stdio"
```

## License

MIT
