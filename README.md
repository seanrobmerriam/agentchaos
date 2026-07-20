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

## Honest scope

AgentChaos proves recovery logic breaks under **these fault classes** — it is
not an exhaustive correctness proof. Specifically:

- It covers **message-level** faults (drop, duplicate, reorder, kill,
  in_doubt) at the JSON-RPC layer.
- It does **not** cover network-level faults (packet loss, latency below the
  MCP message layer). Pair with `tc` or [Toxiproxy](https://github.com/Shopify/toxiproxy) for that.
- It is **single-proxy, single-upstream-connection** in v1. Multi-node
  distributed scenarios (faults coordinated across several proxies) are a
  non-goal.
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

# Run all seeds even after failure; export the event log for each:
agentchaos run \
  --scenario scenarios/idempotency.yaml \
  --upstream "npx -y @modelcontextprotocol/server-everything stdio" \
  --seeds 20 --stop-on all \
  --event-log events.ndjson

# Replay a specific seed (seed read from the file when --seed is omitted):
agentchaos replay \
  --scenario reproducer.yaml \
  --upstream "npx -y @modelcontextprotocol/server-everything stdio"

# Validate a scenario file:
agentchaos validate --scenario scenarios/idempotency.yaml

# Lint a scenario for configuration problems:
agentchaos lint --scenario scenarios/idempotency.yaml

# Inspect a scenario (pretty-print faults and assertions):
agentchaos inspect --scenario scenarios/idempotency.yaml

# Preview which faults would fire on a recorded trace:
agentchaos inspect --scenario scenarios/idempotency.yaml \
  --dry-run --messages trace.jsonl

# Show the post-composition scenario (after extends/include):
agentchaos inspect --scenario scenarios/child.yaml --resolved

# Render a human-readable timeline from an event log:
agentchaos explain --event-log events.ndjson

# Batch multi-seed CI run with JSON and JUnit reports:
agentchaos risk \
  --scenario scenarios/idempotency.yaml \
  --upstream "npx -y @modelcontextprotocol/server-everything stdio" \
  --seeds 500 --parallel 8 \
  --report risk.json --junit risk.xml

# Fuzz: generate random fault schedules and find failures:
agentchaos fuzz \
  --scenario scenarios/idempotency.yaml \
  --upstream "npx -y @modelcontextprotocol/server-everything stdio" \
  --runs 200 --max-faults 6 --shrink-on-failure
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | All assertions passed |
| 70 | Assertion failure detected |
| 75 | Run timed out (`--timeout` exceeded) |
| 77 | Process killed by `kill_process` fault |
| 78 | Invalid scenario file |
| 1 | General usage error |

## Subcommands

### `run`

Run a scenario against an upstream for one or more seeds.

```
agentchaos run --scenario <path> --upstream <cmd> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--scenario` | (required) | Path to scenario YAML |
| `--upstream` | (required) | Upstream command (`stdio` transport) |
| `--transport` | `stdio` | Upstream transport: `stdio` or `http` |
| `--upstream-url` | | Upstream URL for `--transport http` |
| `--seeds` | 1 | Number of seeds to try |
| `--timeout` | 60s | Per-seed wall-clock deadline; exit 75 on expiry |
| `--stop-on` | `first` | Stop after first failure (`first`) or run all seeds (`all`) |
| `--shrink-on-failure` | false | Shrink the fault schedule when a seed fails |
| `--no-shrink` | false | Disable shrinking even if `--shrink-on-failure` is set |
| `--shrink-strategy` | `greedy` | Shrink strategy: `greedy` or `bisect` |
| `--shrink-max-iter` | 200 | Max shrink iterations |
| `--reproducer` | | Write the minimal reproducer scenario to this path |
| `--event-log` | | Write per-seed event log as NDJSON to this path |

### `replay`

Replay a specific seed from a scenario file. When `--seed` is not provided,
the seed is read from the scenario file itself — making reproducer files
self-contained.

```
agentchaos replay --scenario <path> --upstream <cmd> [--seed N]
```

### `validate`

Parse and validate a scenario file. Exits 0 on success, 78 on error.

```
agentchaos validate --scenario <path>
```

### `lint`

Run deeper static checks on a scenario: validates anchor names, action
parameters, probability ranges, and assertion types. Exits 78 on any error
diagnostic; warnings do not affect the exit code.

```
agentchaos lint --scenario <path>
```

### `inspect`

Pretty-print a scenario's faults and assertions.

```
agentchaos inspect --scenario <path> [--dry-run --messages <trace>] [--resolved]
```

| Flag | Description |
|------|-------------|
| `--dry-run` | Replay a JSON-RPC trace through the executor and print the resulting fault schedule |
| `--messages` | Path to a newline-delimited JSON-RPC trace file (required with `--dry-run`) |
| `--resolved` | Resolve `extends`/`include` directives and show the merged scenario |

### `explain`

Render a human-readable timeline from an NDJSON event log produced by
`run --event-log`.

```
agentchaos explain --event-log <path>
```

### `risk`

Batch multi-seed runner with parallel execution and structured reports.
Exits 70 if any seeds fail, 0 otherwise.

```
agentchaos risk --scenario <path> --upstream <cmd> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--seeds` | 100 | Number of seeds to run |
| `--parallel` | 1 | Max concurrent scenario runs |
| `--timeout` | 60s | Per-seed wall-clock deadline |
| `--shrink-on-failure` | false | Shrink failing seeds to minimal reproducers |
| `--report` | | Write a JSON report to this path |
| `--junit` | | Write a JUnit XML report to this path |

### `fuzz`

Generate random fault schedules derived from a base scenario, run each
against the upstream, and report unique failure classes. When
`--shrink-on-failure` is set, each distinct failure class is shrunk to a
minimal reproducer. Exits 70 if any class is found, 0 otherwise.

```
agentchaos fuzz --upstream <cmd> [--scenario <path>] [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--scenario` | | Base scenario (assertions are reused) |
| `--runs` | 200 | Number of generated scenarios to execute |
| `--max-faults` | 8 | Max faults per generated scenario |
| `--timeout` | 30s | Per-run wall-clock deadline |
| `--shrink-on-failure` | false | Shrink each unique failure class |
| `--shrink-max-iter` | 200 | Max shrink iterations per class |
| `--report` | | Write a JSON report to this path |

## Scenario DSL

A scenario is a YAML file with a `seed`, a list of `faults`, and a list of
`assertions`. Scenarios can inherit from or include other scenarios via
`extends` and `include`.

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

### Scenario composition

A scenario can inherit from a base scenario using `extends`, or merge in
additional faults/assertions from sibling files using `include`. Paths are
resolved relative to the scenario file.

```yaml
# child.yaml
seed: 100
extends: base.yaml       # inherits base faults/assertions; own faults appended after
include:
  - extra-faults.yaml    # faults/assertions from this file are appended last
faults:
  - match: {tool: "send_invoice"}
    action: duplicate
    count: 2
```

Composition is resolved by `agentchaos inspect --resolved` and by
`scenario.Load` in the Go API. Direct `scenario.Parse` does **not** resolve
composition.

### Match fields

All specified fields must match (AND semantics). Unspecified fields are
ignored.

| Field | Type | Description |
|-------|------|-------------|
| `tool` | string | Matches `tools/call` requests with `params.name` equal to this value. `"*"` matches any `tools/call` request. |
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

#### `expr` — custom DSL assertion

Evaluate an arbitrary expression over the event log. Supports
`count(kind)`, `count(kind where field==value)`, comparison operators
(`>=`, `<=`, `>`, `<`, `==`, `!=`), and boolean operators (`and`, `or`,
`not`).

```yaml
- type: expr
  expr: "count(response_delivered) >= 1"

- type: expr
  expr: "count(fault_fired where action==duplicate) <= 1 and count(terminal_state) == 1"

- type: expr
  expr: "not (count(response_dropped) > 0)"
```

Supported event kinds: `request_sent`, `response_received`,
`response_delivered`, `notification_sent`, `notification_delivered`,
`fault_fired`, `response_dropped`, `response_duplicated`, `process_killed`,
`checkpoint_commit`, `terminal_state`.

Supported `where` fields: `tool`, `method`, `action`, `key`, `source`,
`direction`.

See `internal/assert/dsl_grammar.md` for the full grammar.

#### Programmatic custom assertions

User-supplied verifiers can be registered via `assert.RegisterCustom`:

```go
import "github.com/seanrobmerriam/agentchaos/internal/assert"

assert.RegisterCustom("my_custom_check", func(a scenario.Assertion, log *event.Log) assert.Result {
    // Custom logic here
    return assert.Result{Failed: false}
})
```

## Well-known notifications

Agents can emit structured events to the proxy by sending a JSON-RPC
notification with method `notifications/agentchaos/event`. The proxy
translates it into an event log entry and does **not** forward it upstream.

```jsonc
// Record a durable checkpoint commit:
{"jsonrpc":"2.0","method":"notifications/agentchaos/event","params":{"kind":"checkpoint_commit","tool":"charge_card","msg_id":7,"key":"idk-1"}}

// Record that a terminal state was reached:
{"jsonrpc":"2.0","method":"notifications/agentchaos/event","params":{"kind":"terminal_state","key":"end"}}

// Record an arbitrary span (useful for custom assertions):
{"jsonrpc":"2.0","method":"notifications/agentchaos/event","params":{"kind":"span","name":"retry_attempt"}}
```

These notifications enable built-in assertions (`terminal_state_reached`,
`effect_without_checkpoint_commit`) and can be queried in `expr` assertions.
See `docs/well-known-notifications.md` for the full contract.

## Determinism

All injected randomness — which fault fires under `probability: <1.0`,
reorder window permutation, timing — is driven by a single seed (SplitMix64
PRNG). Same seed and scenario produce a byte-identical fault schedule.

When a seed produces an assertion failure, `--shrink-on-failure` searches for
a smaller fault schedule — fewer faults, earlier in the run — that still
reproduces the failure. Two strategies are available:

| Strategy | Description |
|----------|-------------|
| `greedy` (default) | Remove one fault at a time; keep the scenario if it still fails |
| `bisect` | Halve the fault list repeatedly, then fall through to greedy |

The shrunk scenario is written to `--reproducer`. The file includes a header
comment with the original seed and iteration count, so it is self-contained
for `agentchaos replay`.

## Transports

The agent always speaks stdio to the proxy. The proxy supports two upstream
transports:

- **stdio** (default) — the proxy spawns the upstream as a subprocess and
  communicates via stdin/stdout. Pass `--upstream '<cmd>'`.
- **Streamable HTTP** — the proxy connects to an upstream HTTP server. SSE
  reverse-channel supported via `--reverse-get`. Pass
  `--transport http --upstream-url <url>`.

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
AGENTCHAOS_DEBUG=1 agentchaos replay \
  --scenario scenarios/example.yaml \
  --upstream "npx -y @modelcontextprotocol/server-everything stdio"
```

## License

MIT
