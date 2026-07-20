# AgentChaos — Specification (v1)

> Canonical reference. Source-file comments cite this doc by section
> (e.g. "See SPEC.md §7"). When the implementation drifts from this
> spec, update both.

---

## §1. Overview

AgentChaos is a transparent MCP fault-injection proxy. The agent
runtime (an MCP client) connects to the proxy; the proxy connects to
one or more upstream MCP servers. The proxy intercepts every
JSON-RPC message crossing the wire and injects failures according to
a declarative scenario file.

### §1.1 Honest scope (v1)

- Message-level faults only (drop, duplicate, reorder, kill, in_doubt,
  corrupt_checkpoint). Network-level faults (packet loss, latency
  below the MCP message layer) are out of scope; pair with `tc` or
  [Toxiproxy](https://github.com/Shopify/toxiproxy).
- Single-proxy, single-upstream-connection. Multi-node distributed
  scenarios are a non-goal for v1.
- `corrupt_checkpoint` fidelity depends on knowing the target's
  on-disk format.

---

## §3. Message model

Every JSON-RPC message is parsed into a `Message`:

| Field  | Type   | Description                                       |
|--------|--------|---------------------------------------------------|
| Kind   | string | `"request"`, `"response"`, or `"notification"`    |
| Method | string | JSON-RPC `method`                                 |
| ID     | int64  | numeric JSON-RPC `id` (0 for string or absent)    |
| Tool   | string | `tools/call` `params.name`; else `Method`         |

The kind is inferred from the JSON envelope: presence of both `id` and
`method` → request, `id` without `method` → response, neither → notification.

---

## §4. Fault DSL

A scenario YAML contains a `seed`, a list of `faults`, and a list of
`assertions`. Each fault has a `match`, an `at`, and an `action`.

### §4.2 Matcher fields

| Field   | Type                | Description                                       |
|---------|---------------------|---------------------------------------------------|
| tool    | string              | matches `tools/call` `params.name`; `"*"` matches any `tools/call` request |
| method  | string              | matches JSON-RPC `method`; `"*"` matches any     |
| type    | string              | one of `request`, `response`, `notification`      |
| id      | number or `"*"`     | matches JSON-RPC `id`; `"*"` matches any non-notification |

All specified fields must match (AND semantics). Unspecified fields
are skipped.

### §4.3 Temporal anchors

Each action may specify an `at` anchor:

| Anchor                  | Meaning                                                              |
|-------------------------|----------------------------------------------------------------------|
| `before_request_send`   | Before the proxy forwards the request to upstream                    |
| `after_request_sent`    | After the request has been sent to upstream                          |
| `before_response`       | Before the proxy delivers a response to the agent                    |
| `at_notification_recv`  | When a notification arrives (either direction)                       |

Default anchor per action:

| Action               | Default anchor         |
|----------------------|------------------------|
| `kill_process`       | `after_request_sent`   |
| `duplicate`          | (any)                  |
| `reorder`            | `before_response`      |
| `in_doubt`           | `after_request_sent`   |
| `corrupt_checkpoint` | `after_request_sent`   |

The parser rejects `at` values that are not in the canonical set with
exit code 78 (see §8).

### §4.4 Fault primitives

| Action               | Effect                                                                  |
|----------------------|-------------------------------------------------------------------------|
| `kill_process`       | After request forwarding, exit signal set; proxy returns normally.      |
| `duplicate`          | Delivery repeated N times (`count` param, default 2).                   |
| `reorder`            | Window of N responses buffered, released in a permuted order.           |
| `in_doubt`           | Request is forwarded; response is dropped before delivery.             |
| `corrupt_checkpoint` | Flip N bytes at offset in a file at the firing time.                    |

---

## §6. Shrinking

When a scenario produces an assertion failure, `--shrink-on-failure`
performs a Jepsen-style bisect: try removing each fault one at a time;
if the predicate still holds (failure reproduces), keep the smaller
version. Repeat until no further reduction is possible or the
iteration budget is exhausted.

### §6.1 Seed handling

- `seed` is an int64. Zero is a valid seed value.
- `--seeds N` runs seeds `[seed, seed+N)`; the run stops on the first
  failing seed by default.
- All injected randomness (probability rolls, reorder permutations)
  is driven by a single SplitMix64 PRNG seeded from `scenario.seed`.
  Same seed + same scenario + same call sequence produces a
  byte-identical fault schedule.

---

## §7. Event log and assertions

The proxy records every message crossing the wire, every fault that
fired, and every dropped response into an append-only event log.
Built-in assertions evaluate against this log:

| Assertion type                       | What it checks                                       |
|--------------------------------------|------------------------------------------------------|
| `no_duplicate_effect`                | Two requests with the same `key` did not both produce a delivered response |
| `terminal_state_reached`             | A `KindTerminalState` event was recorded within `within_retries` |
| `effect_without_checkpoint_commit`   | Every delivered effect has a matching `KindCheckpointCommit` commit |

The two events `KindCheckpointCommit` and `KindTerminalState` are
produced when the agent (or the test harness) emits a JSON-RPC
notification of method `notifications/agentchaos/event` with
`params.kind ∈ {"checkpoint_commit", "terminal_state"}`. The proxy
consumes these notifications (they are NOT forwarded) and records
the corresponding event. Without an emitter, the assertions
`terminal_state_reached` and `effect_without_checkpoint_commit`
will always fail.

---

## §8. Exit codes

| Code | Meaning                              |
|------|--------------------------------------|
| 0    | All assertions passed                |
| 70   | Assertion failure detected           |
| 75   | Run deadline exceeded (`--timeout`)  |
| 77   | Process killed by `kill_process` fault |
| 78   | Invalid scenario file                |
| 1    | General usage error                  |
