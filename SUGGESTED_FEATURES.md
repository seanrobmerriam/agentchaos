# SUGGESTED_FEATURES.md — Next Features for AgentChaos

Derived from the code review in `ISSUES.md` and the "Honest scope" / "Non-goals"
section of `README.md`. Items are grouped by theme and ordered roughly by
impact-to-effort ratio. Each item notes **Why** (the gap it fills) and
**Sketch** (a concrete starting shape). The 🔴 Critical issues from
`ISSUES.md` are prerequisites for several of these and are referenced where
relevant.

---

## 1. Fix the foundations first (prerequisites)

Before adding surface area, the review found three 🔴 correctness issues that
undermine the project's headline guarantees. These should land before
features that rely on them:

- **C1 — Deterministic PRNG under concurrency.** Serialize `splitMix64` and
  `schedule` mutation behind `ex.mu`; add a concurrent-execution determinism
  test. Without this, every "run N seeds" feature reports unreliable results.
- **C2 — Replace in-band `os.Exit` with a kill signal.** Make the pump return
  exit code 77 normally so cleanup + assertions run and `--shrink-on-failure`
  works for `kill_process` scenarios.
- **C3 — Emit `KindCheckpointCommit` / `KindTerminalState`.** Define an
  ingestion path so `terminal_state_reached` and
  `effect_without_checkpoint_commit` can actually pass.

These unlock reliable versions of features 2, 3, 4, and 8 below.

---

## 2. `--transport http` and a fault-aware Streamable HTTP proxy

**Why.** `README.md` advertises Streamable HTTP upstreams, but the CLI is
stdio-only (`ISSUES.md` H1) and `HTTPProxy` has no executor, so HTTP
upstreams get zero fault injection. `reorder` is HTTP-only by design yet is
unreachable today.

**Sketch.**
- Flags: `--transport stdio|http`, `--upstream-url <url>`,
  `--reverse-get` (maps to `HTTPOptions.ReverseGET`).
- Refactor `HTTPProxy` to own a `*fault.Executor`; parse each POST response /
  SSE `data:` frame into a `scenario.Message`, run `ProcessReverse`, and
  write the resulting frames back to `agentOut`.
- Pre-route forward messages through `ProcessForward` before POSTing.
- New end-to-end test against an in-process `httptest.Server` speaking the
  mcptoy protocol over HTTP.

---

## 3. `agentchaos risk` — multi-seed batch runner with a structured report

**Why.** The current `run` command stops at the *first* failing seed and
only prints a one-line reason. Teams want "across 1000 seeds, how many
failed, which seeds, what did the minimal reproducer shrink to" — a Jepsen-
style summary suitable for CI artifacts.

**Sketch.**
- `agentchaos risk --scenario s.yaml --upstream … --seeds 1000 [--parallel 4]
  --report report.json [--junit report.xml]`.
- Track pass/fail per seed, the first failing seed, the shrunk reproducer,
  and aggregate assertion-failure reasons.
- Exit code: 0 if all pass, 70 if any seed failed, 78 invalid scenario.
- JSON schema: `{seeds_run, seeds_failed, failures:[{seed, reason,
  reproducer_path, original_faults, shrunk_faults}], schedule_sample}`.
- Reuses the fix from C1/C2 so results are reproducible.

---

## 4. `agentchaos inspect` that actually inspects (and dry-runs the schedule)

**Why.** Today `inspect` prints only `fault[i]: <matcher> <action>`
(`ISSUES.md` L4). Users can't see `probability`, `count`, `window`,
`path/offset/bytes`, or `at`, and can't preview *which* faults would fire for
a seed without running against a live upstream.

**Sketch.**
- Pretty-print every populated field of each fault and assertion.
- `agentchaos inspect --scenario s.yaml --dry-run --seed 4891 --messages
  messages.jsonl`: feed a recorded message trace through `fault.Pipeline`
  (already exists for exactly this — `RunFixture`) and print the expected
  match/schedule preview without spawning an upstream.
- This makes the DSL debuggable standalone, no MCP server required.

---

## 5. `agentchaos lint` — scenario validation that catches silent failures

**Why.** `Validate()` doesn't check `at` anchor names (`ISSUES.md` M1), and
unknown assertion types are only caught at run time. A dedicated linter
catches these before any subprocess is spawned and fits a CI gate.

**Sketch.**
- `agentchaos lint --scenario s.yaml` → exit 0 clean, 78 with diagnostics.
- Checks: valid `action`, valid `at` against the anchor table from
  `pipeline.go`, valid `match.type`, `duplicate.count>=1`,
  `reorder.window>=1` (and that `reorder` is not used with `--transport
  stdio`), `corrupt_checkpoint.path`/`bytes` presence, `probability∈[0,1]`,
  known assertion `type` (built-in or `RegisterCustom`-registered),
  assertion/action compatibility (e.g. `in_doubt` matcher must target a
  `tools/call` request).
- Reuse the same `validActions`/new `validAnchors` maps from `scenario.go`.

---

## 6. Event-source adapters for checkpoint/terminal-state (unblocks C3)

**Why.** The proxy only sees JSON-RPC; agent-internal "checkpoint committed"
and "terminal state reached" signals never enter the log, so two built-in
assertions are unusable (`ISSUES.md` C3).

**Sketch.** Pick one well-defined ingestion contract and document it:
- **Option A (recommended): well-known notifications.** The agent emits
  `notifications/agentchaos/checkpoint_commit` (params: `{tool, msg_id, key}`)
  and `notifications/agentchaos/terminal_state` (params: `{key}`). The proxy's
  reverse pump recognises these and `event.Log.Record`s the corresponding
  `KindCheckpointCommit`/`KindTerminalState` events (and *does not* forward
  them upstream). Zero new transport; works over both stdio and HTTP.
- **Option B:** a sidecar file/pipe the agent writes to, tailed by the proxy.
- Ship a reference implementation in `testutil/mcptoy` so example scenarios
  pass out of the box.

---

## 7. Structured event-log export (`--event-log run NdJSON`)

**Why.** The event log is the oracle's ground truth but is currently
in-memory only (`internal/event/event.go`). For CI artifacts and post-hoc
analysis, teams want to dump it.

**Sketch.**
- `--event-log path.jsonl` writes every `event.Event` (with `Seq`,
  `Kind`, `MsgID`, `Method`, `Tool`, `Action`, `FaultIndex`, `Direction`,
  `Timestamp`, `Raw` as base64) one per line.
- `agentchaos explain --event-log run.jsonl` renders a human-readable
  timeline interleaving `request_sent`/`response_delivered`/`fault_fired`/
  `response_dropped` with the `[dropped]` annotation the README already
  promises for `in_doubt`.
- Enables external oracles and custom assertion authoring without Go code.

---

## 8. Robust `--shrink-on-failure` UX

**Why.** Shrinking is a marquee feature but today it silently terminates on
`kill_process` (C2), discards its statistics (`ISSUES.md` H3), and only
writes a YAML reproducer.

**Sketch.**
- Return `shrink.Result` with `Iterations`, `OriginalN`, `FinalN` and print
  them (H3).
- `--reproducer reproducer.yaml` *and* `--reproducer-seed N` so the
  reproducer is replayable without copying the seed by hand.
- `--shrink-strategy greedy|bisect` (greedy is current single-fault-removal;
  add bisect for larger schedules). Stop hiding the iteration cap behind a
  magic 200 in `main.go` — surface it as `--shrink-max-iter`.
- Print the diff between original and shrunk fault lists.

---

## 9. `--timeout` and CI-friendly run controls

**Why.** A hung upstream hangs the CLI forever today (`ISSUES.md` M2), which
is especially bad for a tool whose value proposition is CI determinism.

**Sketch.**
- `--timeout 60s` via `context.WithTimeout` around the pump; on timeout emit
  a new exit code (e.g. 75) and record a `fault_fired`-style timeout event.
- `--stop-on first|all` (first failing seed vs. continue-and-summarize;
  pairs with feature 3).
- `--no-shrink` shorthand for the common "just tell me if it fails" path.

---

## 10. Scenario composition (`extends` / `include`)

**Why.** The shipped scenarios duplicate near-identical fault lists
(`duplicate.yaml`, `in_doubt.yaml`, `kill_process.yaml` all match one tool).
Teams will want a base "idempotency harness" plus per-fault variations.

**Sketch.**
- `extends: base.yaml` at the top of a scenario: faults and assertions are
  merged (later wins on conflicts; arrays concatenated with dedupe).
- `include: [dup.yaml, indoubt.yaml]` for fault-bag composition.
- Resolved at `scenario.Parse` time so `lint`/`inspect`/`validate` see the
  flattened result; emit the flattened doc with `inspect --resolved`.

---

## 11. Property-based scenario fuzzing harness

**Why.** The package already uses `pgregory.net/rapid` in tests
(`determinism_test.go`); exposing that as a CLI command lets CI discover
*scenario* bugs (not just code bugs) across many seeds and matcher
combinations.

**Sketch.**
- `agentchaos fuzz --scenario s.yaml --upstream … --runs 500 --max-faults 8`:
  rapid-generates matcher/action combinations within the scenario's
  declared fault grammar, runs each, collapses failures into a minimal
  reproducer using the existing `shrink.Shrink`.
- Output a markdown table of distinct failure classes.

---

## 12. Network-level fault integration (documented/paired, then native)

**Why.** `README.md` "Honest scope" explicitly says network-level faults
(packet loss, latency) are out of scope for v1 and should pair with `tc` or
[Toxiproxy](https://github.com/Shopify/toxiproxy). A first-class integration
is the natural v-next.

**Sketch.**
- `--toxiproxy addr` wraps the upstream dial in Toxiproxy toxic
  specifications generated from a new fault action family
  (`add_latency`, `add_jitter`, `drop_connection`), reusing the same
  matcher/anchor/at model so the DSL stays uniform.
- Keep the boundary clean: message-level faults stay in AgentChaos;
  network-level faults delegate to Toxiproxy via its API.

---

## 13. Custom assertions via config, not just Go

**Why.** `assert.RegisterCustom` (`internal/assert/assert.go`) requires
writing and recompiling Go. Most operators want to express checks declaratively.

**Sketch.**
- A `custom` assertion type with a tiny expression DSL over the event log:
  ```yaml
  - type: custom
    expr: "count(response_delivered where tool=='charge_card') <=
           count(checkpoint_commit where tool=='charge_card')"
  ```
- Implement with a small interpreter over `event.Log.Filter`; gate unsafe
  operations; document the available event fields. Falls back to
  `RegisterCustom` for anything the DSL can't express.

---

## 14. `replay` that reproduces a shrunk scenario end-to-end

**Why.** After shrinking, you want a one-command reproduction, not "edit the
seed by hand". Today `replay --seed N` and `--reproducer reproducer.yaml`
are separate and the reproducer file's seed is what you'd want replayed.

**Sketch.**
- `agentchaos replay --scenario reproducer.yaml --upstream …` reads the seed
  *from the file* (no `--seed` required), and `--reproducer`’s shrunk YAML
  is directly replayable.
- Add a `# shrunk from <original_seed> via <iterations> iters` header comment
  when writing the reproducer so the lineage is auditable.

---

## 15. Observability: `--metrics :9090` and structured logs

**Why.** Fault-injection runs in CI benefit from counters (messages
forwarded, faults fired by action, dropped responses) and trace logs
correlated by `Seq`.

**Sketch.**
- Prometheus counters: `agentchaos_messages_total{direction,kind}`,
  `agentchaos_faults_fired_total{action}`, `agentchaos_responses_dropped_total`.
- `--log-format json|text` with per-event trace IDs = `Event.Seq`.
- Optional; behind a flag so the default CLI stays quiet.

---

## Suggested ordering

1. **Foundation fixes** (ISSUES C1, C2, C3) + **L1 gofmt** + **L2 dead code** — one
   housekeeping PR that makes everything else trustworthy.
2. **Feature 5 (`lint`)** + **Feature 4 (`inspect` overhaul / `--dry-run`)** — small,
   high value, no subprocess needed.
3. **Feature 9 (`--timeout`)** + **Feature 8 (shrink UX + H3)** — make the existing
   `run`/`replay` reliable and informative.
4. **Feature 6 (event-source adapters)** — unblocks C3 and makes the example
   scenarios pass; pairs with **Feature 7 (event-log export)**.
5. **Feature 2 (`--transport http`)** — delivers the documented HTTP path and
   unlocks `reorder` for real.
6. **Feature 3 (`risk` batch runner)** + **Feature 11 (`fuzz`)** — scale-up CI
   workflows now that single runs are trustworthy.
7. **Features 10, 13, 14, 12, 15** — ergonomic and scale features, ordered by
   team need.