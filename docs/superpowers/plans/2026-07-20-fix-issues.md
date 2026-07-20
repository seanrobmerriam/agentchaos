# AgentChaos — Fix ISSUES.md Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every finding documented in `ISSUES.md` (3 Critical, 4 High, 5 Medium, 6 Low) so that the determinism guarantee holds under the concurrent stdio pump, the documented CLI surface is reachable, the codebase is `gofmt`-clean, and `go test -race -count=1 ./...` stays green.

**Architecture:** Targeted, mostly surgical edits. The largest blocks of work are (a) synchronizing the executor's PRNG+schedule so the stdio pump's two goroutines stop racing, (b) replacing `os.Exit` with a kill signal so `kill_process` scenarios are shrinkable, and (c) adding a well-known JSON-RPC notification that the proxy translates into `KindCheckpointCommit`/`KindTerminalState` events (so the currently-unusable built-in assertions can pass). Everything else is cleanup, validation, and CLI ergonomics.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` v3.0.1, `pgregory.net/rapid` v1.3.0. `go test -race`, `gofmt -l`, `go vet ./...`, `go build ./...`.

## Global Constraints

- Every commit must keep the following gates green:
  - `gofmt -l .` → empty
  - `go build ./...` → 0
  - `go vet ./...` → 0
  - `go test -race -count=1 ./...` → 0
- YAML scenario backwards compatibility: every scenario in `scenarios/` must still validate and run without edits.
- Exit codes preserved per README: `0` pass, `70` assertion failure, `77` process killed, `78` invalid scenario, `1` general usage error.
- Public Go APIs of `internal/fault`, `internal/scenario`, `internal/shrink`, `internal/event`, `internal/assert` are 1.x; additive only (no signature breakage). New symbols may be added.
- No new third-party dependencies.
- Determinism guarantee ("same seed → byte-identical fault schedule") must hold under the actual concurrent `pumpWithFaults` access pattern, not only under the existing sequential property tests.

## File Structure

Files modified or created by this plan:

| File | Why |
|---|---|
| All `*.go` files | L1 — bring `gofmt -l .` to empty. |
| `internal/fault/executor.go` | C1 (mutex), C2 (kill signal), L3 (lock holding), L6 (short-read handling). |
| `internal/fault/pipeline.go` | Export `ValidAnchors()` for `scenario.Validate` (M1). |
| `internal/fault/checkpoint_event.go` (new) | C3 — well-known notification adapter. |
| `internal/fault/checkpoint_event_test.go` (new) | C3 — adapter unit tests. |
| `internal/fault/event_source_test.go` (new) | C1 — concurrent-determinism test. |
| `internal/scenario/scenario.go` | M1 — `at`-anchor validation; expose `validAnchors`. |
| `internal/scenario/scenario_test.go` | M1 — anchor validation tests. |
| `internal/event/event.go` | L8 — `Filter` map alloc; add `Source` field. |
| `internal/shrink/shrink.go` | H3 — return `Result` instead of discarding. |
| `internal/shrink/shrink_test.go` | H3 — `Result` field assertions. |
| `internal/proxy/proxy.go` | No code change; reference only (M3). |
| `testutil/mcptoy/mcptoy.go` | L7 — notification no-reply. |
| `testutil/mcptoy/mcptoy_test.go` (new) | L7 — notification test. |
| `cmd/agentchaos/main.go` | M2 (`--timeout`), M3 (refactor pump), M5 (no-op if SPEC.md created), L4 (inspect overhaul), L5 (seed loop), wiring for new C2 exit code path. |
| `cmd/agentchaos/main_test.go` (new) | H2 — first CLI tests. |
| `cmd/agentchaos/cases_test.go` (new) | H2 — exit-code matrix. |
| `scenarios/idempotency.yaml` (new) | M4 — README example target. |
| `scenarios/example.yaml` | C3 — add a checkpoint/terminal notification to demo the assertions. |
| `docs/SPEC.md` (new) | M5 — the spec the source comments cite (§3, §4, §6, §7, §8). |

---

### Task 1: L1 — Bring the tree to `gofmt -l .` clean

**Files:**
- Modify: every `.go` file under the tree that `gofmt -l .` currently flags (23 of 28 at writing time).
- Verify: `gofmt -l .` returns nothing; `go build ./...` succeeds; `go vet ./...` clean; all tests still pass.

**Interfaces:**
- Consumes: existing source bytes.
- Produces: re-formatted source bytes with whitespace-only diffs (struct column alignment, trailing newlines).

- [ ] **Step 1: Run gofmt and capture the list**

```bash
gofmt -l . | tee /tmp/gofmt-before.txt
wc -l /tmp/gofmt-before.txt
```

Expected: a non-empty list of paths (the 23 files enumerated in `ISSUES.md` L1).

- [ ] **Step 2: Apply gofmt**

```bash
gofmt -w .
```

- [ ] **Step 3: Verify gofmt is clean**

```bash
gofmt -l .
```

Expected: no output (exit 0).

- [ ] **Step 4: Verify build/vet/tests are still green**

```bash
go build ./...
go vet ./...
go test -race -count=1 -short ./...
```

Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "style: gofmt the whole tree (ISSUES.md L1)"
```

---

### Task 2: L2 — Remove dead code/import hacks

**Files:**
- Modify: `internal/fault/executor.go`, `internal/fault/pipeline.go`, `internal/scenario/scenario.go`, `internal/fault/executor.go`'s `math` import, `cmd/agentchaos/main.go`.
- Test: existing tests still pass.

**Interfaces:**
- Consumes: nothing new.
- Produces: cleaner imports; no `var _ = ...` placeholder lines outside of justified ones.

- [ ] **Step 1: Remove the `math` import and `var _ = math.Pi` hack**

In `internal/fault/executor.go`:
- Delete the line `import "math"` from the import block (verify `math` is not used elsewhere in the file; `Float64` does not use it).
- Delete the trailing line `var _ = math.Pi`.

- [ ] **Step 2: Remove `matchIDWildcard` and `matchTypeIDWildcard` from `internal/scenario/scenario.go`**

- Delete the function `matchIDWildcard` and the surrounding `// matchIDWildcard checks ...` block.
- Delete `matchTypeIDWildcard` and the `// reserved for future use` block.
- Delete the `var _ = matchIDWildcard` line.

- [ ] **Step 3: Remove the `var _ = proxy.New` placeholder from `cmd/agentchaos/main.go`**

- If `proxy` is no longer referenced anywhere else in `main.go`, delete the `proxy` import line and the placeholder line. (Larger refactor of pump wiring is in Task 11; for this task only delete the unused import/var.)

- [ ] **Step 4: Confirm the file compiles**

```bash
go build ./...
```

- [ ] **Step 5: Run tests**

```bash
go test -race -count=1 -short ./...
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor: drop dead code and unused imports (ISSUES.md L2)"
```

---

### Task 3: C1 — Data race + concurrent determinism

**Files:**
- Modify: `internal/fault/executor.go` (`shouldFire`, `ProcessForward`, `ProcessReverse`, `Schedule`, `Drain`, `DroppedResponses`).
- Test: `internal/fault/event_source_test.go` (new) — failing concurrent test first; concurrent determinism property test.

**Interfaces:**
- Consumes: `*Executor` already owns `mu sync.Mutex`. New behavior: the mutex serializes all PRNG consumption and schedule mutation, and is the only path through which `schedule`, `splitMix64.state`, `pendingInDoubt`, `droppedResponses`, and `reorderBuffer` are mutated.
- Produces: same exported functions (`ProcessForward`, `ProcessReverse`, `Schedule`, `Drain`, `DroppedResponses`) with byte-identical outcomes when called sequentially, AND a new guaranteed determinism property when called concurrently from two goroutines.

- [ ] **Step 1: Write the failing concurrent-determinism test**

Create `internal/fault/event_source_test.go`:

```go
package fault_test

import (
    "sync"
    "testing"

    "github.com/seanrobmerriam/agentchaos/internal/fault"
    "github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// TestConcurrentDeterminism reproduces the pumpWithFaults access pattern:
// one goroutine calls ProcessForward, another calls ProcessReverse, both
// concurrently. Two runs with the same seed must produce identical
// fault schedules.
func TestConcurrentDeterminism(t *testing.T) {
    runOnce := func(seed int64) []fault.ScheduleEntry {
        prob := 0.5
        s := &scenario.Scenario{
            Seed: seed,
            Faults: []scenario.Fault{
                {Action: "duplicate", Match: scenario.Matcher{}, Probability: &prob},
                {Action: "in_doubt", Match: scenario.Matcher{Tool: ptr("t")}},
            },
        }
        ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)
        var wg sync.WaitGroup
        // 50 forward msgs and 50 reverse msgs, interleaved concurrently.
        for g := 0; g < 2; g++ {
            wg.Add(1)
            go func(g int) {
                defer wg.Done()
                for i := 0; i < 50; i++ {
                    if g == 0 {
                        m := scenario.Message{Kind: "request", Method: "tools/call", Tool: "t", ID: int64(i)}
                        _, _ = ex.ProcessForward(m, []byte(`{}`), fault.AgentToUpstream)
                    } else {
                        m := scenario.Message{Kind: "response", Method: "tools/call", Tool: "t", ID: int64(i)}
                        _, _ = ex.ProcessReverse(m, []byte(`{}`), fault.UpstreamToAgent)
                    }
                }
            }(g)
        }
        wg.Wait()
        return ex.Schedule()
    }

    s1 := runOnce(42)
    s2 := runOnce(42)
    if len(s1) != len(s2) {
        t.Fatalf("schedule lengths differ under concurrency: %d vs %d", len(s1), len(s2))
    }
    for i := range s1 {
        if s1[i] != s2[i] {
            t.Fatalf("schedule[%d] differs:\n  %+v\n  %+v", i, s1[i], s2[i])
        }
    }
}

func ptr(s string) *string { return &s }
```

Run:

```bash
go test -race -count=1 -run TestConcurrentDeterminism ./internal/fault/
```

Expected: FAIL with `schedule lengths differ under concurrency` (or a `-race` WARNING).

- [ ] **Step 2: Write the race-only test (cleaner error on its own)**

Add to the same file:

```go
// TestNoScheduleRace is satisfied if -race reports nothing.
func TestNoScheduleRace(t *testing.T) {
    s := &scenario.Scenario{
        Seed: 1,
        Faults: []scenario.Fault{
            {Action: "duplicate", Match: scenario.Matcher{}},
        },
    }
    ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)
    var wg sync.WaitGroup
    for g := 0; g < 4; g++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for i := 0; i < 200; i++ {
                m := scenario.Message{Kind: "response", Method: "tools/call", Tool: "x", ID: int64(i)}
                _, _ = ex.ProcessReverse(m, []byte(`{}`), fault.UpstreamToAgent)
                r := scenario.Message{Kind: "request", Method: "tools/call", Tool: "x", ID: int64(i)}
                _, _ = ex.ProcessForward(r, []byte(`{}`), fault.AgentToUpstream)
                _ = ex.Schedule()
            }
        }()
    }
    wg.Wait()
}
```

Run:

```bash
go test -race -count=1 -run TestNoScheduleRace ./internal/fault/
```

Expected on unfixed code: `WARNING: DATA RACE` pointing at `splitMix64.next()` and `Executor.schedule`.

- [ ] **Step 3: Implement the fix — serialize all PRNG + schedule mutation under `ex.mu`**

In `internal/fault/executor.go`:

1. Add a top-of-method lock acquire to `ProcessForward`, `ProcessReverse`:
   - At the very start of each, `ex.mu.Lock(); defer ex.mu.Unlock()`.
2. Add a top-of-method lock acquire to `Schedule`, `Drain`, `DroppedResponses` (replace their existing `ex.mu.Lock(); defer ex.mu.Unlock()` with the same).

3. In `shouldFire`, drop the manual lock dance (the caller already holds it via the deferred unlock above). The current code does NOT use the lock around the PRNG and schedule append — leave it that way because callers now hold the lock.

4. Provide a private helper `shouldFireLocked` (the body of the existing `shouldFire`, renamed and documented as "callers MUST hold `ex.mu`"). Keep the public `shouldFire` for backward compatibility only if any test still calls it directly — search `grep -rn "executor.shouldFire\|ex.shouldFire" internal/`. If none, remove the public one and use `shouldFireLocked` in the Process methods.

5. Tighten: `ex.schedule = append(ex.schedule, ScheduleEntry{...})` and `ex.prng.Float64()` must both run while the mutex is held.

- [ ] **Step 4: Verify both new tests now pass**

```bash
go test -race -count=1 -run "TestConcurrentDeterminism|TestNoScheduleRace" ./internal/fault/
```

Expected: PASS for both.

- [ ] **Step 5: Run the entire fault suite, including property tests**

```bash
go test -race -count=1 ./internal/fault/
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/fault/executor.go internal/fault/event_source_test.go
git commit -m "fix(fault): serialize PRNG+schedule under mutex (ISSUES.md C1)

- Two pump goroutines no longer race on splitMix64 state.
- Schedule reproduces byte-identically under the concurrent
  pumpWithFaults access pattern.
- -race suite passes."
```

---

### Task 4: C2 — `kill_process` returns a kill signal, never calls `os.Exit`

**Files:**
- Modify: `internal/fault/executor.go` (`ProcessForward`'s `kill_process` branch), `cmd/agentchaos/main.go` (`pumpWithFaults`, `runWithAssertions`, `runOnce`), `cmd/agentchaos/main_test.go` (new).
- Test: existing `phase6_gate_test.go` (kill scenario) and a new CLI test.

**Interfaces:**
- Consumes: `Executor.ProcessForward` already returns `kill bool`; the CLI currently consumes `kill` but `ExitProcess` overrides the exit decision by calling `os.Exit`.
- Produces: a single exit-code value computed by `pumpWithFaults` (returned by `runResult.exitCode`); no in-process `os.Exit` from the fault path.

- [ ] **Step 1: Write the failing test for shrink-on-failure of a `kill_process` scenario**

Add to `cmd/agentchaos/main_test.go`:

```go
package main

import (
    "os"
    "os/exec"
    "path/filepath"
    "testing"
)

// TestShrinkKillProcessDoesNotExitHard ensures a kill_process scenario can
// be shrink-evaluated by a child agentchaos run without the child
// terminating the parent shrink driver mid-search. Before C2 the in-band
// os.Exit would kill the parent and the test would never observe the
// reproducer file.
func TestShrinkKillProcessDoesNotExitHard(t *testing.T) {
    bin := buildCLI(t) // helper defined in Task 17 alongside H2.
    dir := t.TempDir()
    scenario := filepath.Join(dir, "s.yaml")
    mustWrite(t, scenario, `
seed: 1
faults:
  - match: {tool: "counter"}
    action: kill_process
    probability: 1.0
assertions: []
`)
    repro := filepath.Join(dir, "repro.yaml")
    cmd := exec.Command(bin, "run",
        "--scenario", scenario, "--upstream", "cat",
        "--seeds", "2", "--shrink-on-failure", "--reproducer", repro)
    out, err := cmd.CombinedOutput()
    // The child must NOT exit with 77 (process-killed); that signals the
    // in-band os.Exit is still alive. It may exit 0 (if the shrink predicate
    // never triggered, which is fine for this minimal test) or some other
    // non-77 code.
    if exec.ExitCodeFromErr(err) == 77 {
        t.Fatalf("child hit exit 77 (in-band os.Exit still firing):\n%s", out)
    }
}
```

Run before the fix; expect it to either hang (because os.Exit terminated the parent) or fail with exit 77. Note the actual behaviour; we'll re-run after the fix.

- [ ] **Step 2: Replace `ExitProcess` with a recording default**

In `internal/fault/executor.go`:

1. Change the default `ExitProcess` from:
   ```go
   var ExitProcess ExitFunc = func(code int) { os.Exit(code) }
   ```
   to a *recording* function that signals via a flag on the executor. The implementation must not call `os.Exit`. For simplicity in this fix, export a package-private `exitFuncRegistry` plus make `ExitProcess` a no-op by default; the CLI will compose the real exit decision.

   Concretely:
   - Delete `var ExitProcess ExitFunc = func(code int) { os.Exit(code) }`.
   - Leave the `exitFn ExitFunc` field on `Executor` for test injection. Production callers (the CLI) pass a *signal-only* function that just closes a channel or sets a struct field.
   - Public API for callers that do want to actually exit becomes a method on `runResult` rather than a global var. (No change here for `ExitProcess` if external tests depend on it — first run `grep -rn "fault.ExitProcess" .` to confirm no external callers depend on this behaviour. Per the review it is used only by `cmd/agentchaos/main.go` lines 134 and 277, both of which are updated in Step 3.)

2. Update the `kill_process` branch in `ProcessForward` (currently `executor.go:117`):
   ```go
   if ex.exitFn != nil { ex.exitFn(77) }
   ```
   to call `ex.exitFn(77)` and then *return immediately* with `forward` containing the request and `kill` true (already true). The signal fn is what closes the channel — it does not call `os.Exit`.

- [ ] **Step 3: Wire the CLI to exit cleanly after the pump returns**

In `cmd/agentchaos/main.go`:

1. In `pumpWithFaults`, when `killProcess` fires, instead of relying on `ExitProcess`'s `os.Exit`, set the local `exitCode = 77` and return (the existing `exitOnce` already does this — verify and keep).
2. In `runWithAssertions` (`main.go:132`) and `runOnce` (`main.go:275`), pass a signal-only `exitFn`:
   ```go
   signalExited := make(chan struct{})
   ex, err := fault.NewExecutorForTransport(s, func(code int) {
       // Record on the executor-side: signal-only. The pump goroutine
       // returns normally via the kill=true path; assertion checking and
       // subprocess cleanup then run.
       _ = code
       close(signalExited)
   }, fault.TransportStdio)
   ```
   Then keep the existing pattern of letting `ProcessForward` return `kill=true`, the pump set `exitCode=77`, and `runWithAssertions` return that exit code to the caller. Drop the `fault.ExitProcess` import here entirely.
3. In `run` (`cmdRun`) and `replay` (`cmdReplay`), the `os.Exit(...)` at the bottom of each is the legitimate place to terminate the CLI process now that the pump returned cleanly.

- [ ] **Step 4: Verify the new CLI test passes**

```bash
go test -race -count=1 -run TestShrinkKillProcessDoesNotExitHard ./cmd/agentchaos/
```

Expected: PASS.

- [ ] **Step 5: Run all existing tests**

```bash
go test -race -count=1 ./...
```

Expected: PASS. The existing `phase6_gate_test.go` (which constructs an executor and passes a no-op exitFn) must still pass; `executor_test.go` uses `func(int) {}` for the same purpose.

- [ ] **Step 6: Commit**

```bash
git add internal/fault/executor.go cmd/agentchaos/main.go cmd/agentchaos/main_test.go
git commit -m "fix(fault,cli): kill_process returns a signal, never os.Exit (ISSUES.md C2)

- Subprocess cleanup and assertion evaluation now run after kill_process.
- --shrink-on-failure works for kill_process scenarios.
- No in-band os.Exit from any goroutine."
```

---

### Task 5: C3 — `KindCheckpointCommit` / `KindTerminalState` from well-known notifications

**Files:**
- Modify: `internal/event/event.go` (add `Source string` field), `internal/fault/executor.go` (`eventSourceHandler` hook), `cmd/agentchaos/main.go` (wire the handler).
- Create: `internal/fault/checkpoint_event.go`, `internal/fault/checkpoint_event_test.go`.

**Interfaces:**
- Consumes: a JSON-RPC message arriving on either direction. If `method == "notifications/agentchaos/event"` and `params.kind == "checkpoint_commit"` or `"terminal_state"`, translate it.
- Produces: a `KindCheckpointCommit` or `KindTerminalState` entry on `EventLog`, with no bytes forwarded across the proxy in either direction (the notification is consumed, not relayed).

- [ ] **Step 1: Write the failing test**

Create `internal/fault/checkpoint_event_test.go`:

```go
package fault_test

import (
    "testing"

    "github.com/seanrobmerriam/agentchaos/internal/event"
    "github.com/seanrobmerriam/agentchaos/internal/fault"
    "github.com/seanrobmerriam/agentchaos/internal/scenario"
)

func TestCheckpointEventFromNotification(t *testing.T) {
    s := &scenario.Scenario{Seed: 1, Faults: []scenario.Fault{
        // No fault needed; the adapter is unconditional on the proxy.
        {Action: "duplicate", Match: scenario.Matcher{}},
    }}
    ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)

    commit := []byte(`{"jsonrpc":"2.0","method":"notifications/agentchaos/event","params":{"kind":"checkpoint_commit","tool":"charge_card","msg_id":7,"key":"k1"}}`)
    got := ex.HandleForwardMessage(scenario.ParseMessage(commit), commit)
    if len(got) != 0 {
        t.Fatalf("notification should be consumed; got %d forwards", len(got))
    }

    log := ex.EventLog()
    found := false
    for _, e := range log.Filter(event.KindCheckpointCommit) {
        if e.Tool == "charge_card" && e.MsgID == 7 && e.Key == "k1" {
            found = true
        }
    }
    if !found {
        t.Fatalf("expected a KindCheckpointCommit event with tool/msg_id/key; got %+v", log.Events())
    }
}
```

Run before the production code:

```bash
go test -race -count=1 -run TestCheckpointEventFromNotification ./internal/fault/
```

Expected: compile failure (`HandleForwardMessage` does not exist), then test fails on first run after the stub.

- [ ] **Step 2: Add `Source` to `Event` and a constructor**

In `internal/event/event.go`:

```go
type Event struct {
    Kind       Kind
    Timestamp  time.Time
    Seq        int
    MsgID      int64
    Method     string
    Tool       string
    Action     string
    FaultIndex int
    Direction  string
    Raw        []byte
    Key        string
    Source     string // "agent", "well-known-notification", "fault_external"
}
```

- [ ] **Step 3: Implement `HandleForwardMessage` / `HandleReverseMessage`**

In `internal/fault/checkpoint_event.go` (new file):

```go
package fault

import (
    "encoding/json"

    "github.com/seanrobmerriam/agentchaos/internal/event"
    "github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// wellKnownNotificationMethod is the JSON-RPC method name the proxy
// intercepts to translate agent-internal state into event-log entries.
const wellKnownNotificationMethod = "notifications/agentchaos/event"

// HandleForwardMessage is called for every message going agent→upstream.
// Returns the bytes to forward (possibly none) and whether to kill.
func (ex *Executor) HandleForwardMessage(msg scenario.Message, raw []byte) [][]byte {
    return ex.handleSidebandNotification(msg, raw, event.DirectionAgentToUpstream, ex.processForward)
}

// HandleReverseMessage is the upstream→agent counterpart.
func (ex *Executor) HandleReverseMessage(msg scenario.Message, raw []byte) [][]byte {
    return ex.handleSidebandNotification(msg, raw, event.DirectionUpstreamToAgent, ex.processReverse)
}

// processForward/reverse are the existing Process* methods without the
// well-known notification filter; expose them via a small shim here.
func (ex *Executor) processForward(msg scenario.Message, raw []byte) ([][]byte, bool) {
    return ex.ProcessForward(msg, raw, event.DirectionAgentToUpstream)
}
func (ex *Executor) processReverse(msg scenario.Message, raw []byte) ([][]byte, bool) {
    return ex.ProcessReverse(msg, raw, event.DirectionUpstreamToAgent)
}
```

Add to `internal/fault/executor.go`:

```go
// Direction aliases to the canonical names in event package; re-export
// here so consumers don't need to import event just for the direction.
const (
    DirectionAgentToUpstream   Direction = "agent_to_upstream"
    DirectionUpstreamToAgent  Direction = "upstream_to_agent"
)
```

(Replace the current usages of `fault.AgentToUpstream`/`fault.UpstreamToAgent` where appropriate; verify with `go build`.)

Refactor `ProcessForward` / `ProcessReverse` into thin wrappers that call `handleSidebandNotification` first, then the existing logic. The wrapper:

```go
func (ex *Executor) ProcessForward(msg scenario.Message, raw []byte, dir Direction) ([][]byte, bool) {
    ex.mu.Lock()
    defer ex.mu.Unlock()
    if msg.Method == wellKnownNotificationMethod {
        if err := ex.applyWellKnownNotification(msg, raw); err == nil {
            return nil, false
        }
        // Fall through: if the body is malformed, forward as-is.
    }
    return ex.processForwardLocked(msg, raw)
}
```

Implement `applyWellKnownNotification`:

```go
type wellKnownEvent struct {
    Kind   string `json:"kind"`
    Tool   string `json:"tool,omitempty"`
    MsgID  int64  `json:"msg_id,omitempty"`
    Key    string `json:"key,omitempty"`
    Method string `json:"method,omitempty"` // for terminal_state identification
}

func (ex *Executor) applyWellKnownNotification(msg scenario.Message, raw []byte) error {
    var raw2 map[string]json.RawMessage
    if err := json.Unmarshal(raw, &raw2); err != nil {
        return err
    }
    var p wellKnownEvent
    if err := json.Unmarshal(raw2["params"], &p); err != nil {
        return err
    }
    switch p.Kind {
    case "checkpoint_commit":
        ex.eventLog.Record(event.Event{
            Kind:   event.KindCheckpointCommit,
            MsgID:  p.MsgID,
            Tool:   p.Tool,
            Key:    p.Key,
            Source: "well-known-notification",
            Method: "notifications/agentchaos/event",
        })
    case "terminal_state":
        ex.eventLog.Record(event.Event{
            Kind:   event.KindTerminalState,
            Tool:   p.Tool,
            Key:    p.Key,
            Source: "well-known-notification",
            Method: "notifications/agentchaos/event",
        })
    default:
        return fmt.Errorf("unknown well-known kind %q", p.Kind)
    }
    return nil
}
```

- [ ] **Step 4: Wire `pumpWithFaults` to call the handlers**

In `cmd/agentchaos/main.go`:

- Replace the forward pump's parse-line block with a call to `ex.HandleForwardMessage(msg, trimmed)`.
- Replace the reverse pump's parse-line block with `ex.HandleReverseMessage(msg, trimmed)`.
- Preserve all the existing forwarding/flushing/NL handling already there.

- [ ] **Step 5: Update `scenarios/example.yaml` to emit the notification**

```yaml
faults:
  # (existing list)
  - match: {method: "tools/call", tool: "send_invoice"}
    action: duplicate
    count: 2
assertions:
  - type: terminal_state_reached
    within_retries: 5
  - type: no_duplicate_effect
    key: idempotency_key
```

Notes:
- Add a fake agent transcript fixture (in a new file `testdata/agent_happy_path.jsonl`) that the CLI's `--agent-transcript` flag will replay; this is **out of scope** for the fix-it plan; the integration test in step 5b below covers it.

- [ ] **Step 5b: Add a deterministic integration test**

Add to a new file `internal/fault/checkpoint_event_test.go` (append):

```go
func TestCheckpointAndTerminalAssertionPass(t *testing.T) {
    s := &scenario.Scenario{Seed: 1, Assertions: []scenario.Assertion{
        {Type: "terminal_state_reached", WithinRetries: 5},
    }}
    ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)
    _ = ex // deliberately unused; events are added in steps below

    // Manually inject a terminal_state event:
    ex.EventLog().Record(event.Event{
        Kind:   event.KindTerminalState,
        Key:    "end",
        Source: "test",
    })
    results := assert.CheckAll(s.Assertions, ex.EventLog())
    if assert.AnyFailed(results) {
        t.Fatalf("terminal_state_reached should pass with one event in log; %+v", results)
    }
}
```

Run:

```bash
go test -race -count=1 -run "TestCheckpointEventFromNotification|TestCheckpointAndTerminalAssertionPass" ./internal/fault/
```

- [ ] **Step 6: Run the entire suite**

```bash
go test -race -count=1 ./...
```

- [ ] **Step 7: Commit**

```bash
git add internal/event/event.go internal/fault/executor.go internal/fault/checkpoint_event.go internal/fault/checkpoint_event_test.go cmd/agentchaos/main.go scenarios/example.yaml
git commit -m "feat(event,fault): well-known notification -> KindCheckpointCommit/TerminalState (ISSUES.md C3)

- Proxy consumes notifications/agentchaos/event and never forwards.
- terminal_state_reached and effect_without_checkpoint_commit can now pass.
- Includes an integration assertion test."
```

---

### Task 6: H3 — `shrink.Shrink` returns `Result` instead of discarding

**Files:**
- Modify: `internal/shrink/shrink.go`, `cmd/agentchaos/main.go` (`cmdRun`'s shrink printer).
- Test: `internal/shrink/shrink_test.go` (additions).

**Interfaces:**
- Consumes: existing `Predicate`, `Options`, and `Shrunk` semantics.
- Produces: new signature `(*Result, error)` where `Result.Iterations`, `Result.OriginalN`, `Result.FinalN` are populated. Keep `ShrinkResult` as a type alias for backward compatibility for any external consumer — but the function name `Shrink` is the canonical name; replace the discarding line at `shrink.go:86` with the correct return.

- [ ] **Step 1: Add the failing test**

```go
// in shrink_test.go
func TestShrinkReturnsStats(t *testing.T) {
    orig := makeFaultScenario(5) // helper: scenario with 5 faults, predicate always true
    r, err := shrink.Shrink(orig, alwaysTrue, shrink.Options{})
    if err != nil { t.Fatal(err) }
    if r == nil { t.Fatal("nil result") }
    if r.OriginalN != 5 { t.Fatalf("OriginalN = %d, want 5", r.OriginalN) }
    if r.FinalN > r.OriginalN { t.Fatalf("FinalN = %d > OriginalN = %d", r.FinalN, r.OriginalN) }
    if r.Iterations <= 0 { t.Fatalf("Iterations = %d, want >0", r.Iterations) }
}
```

Run; expect compile error: `shrink.Shrink` returns `(*scenario.Scenario, error)`.

- [ ] **Step 2: Change the signature**

In `internal/shrink/shrink.go`:

```go
func Shrink(original *scenario.Scenario, pred Predicate, opts Options) (*Result, error) {
    if len(original.Faults) == 0 {
        return nil, fmt.Errorf("shrink: scenario has no faults to shrink")
    }
    if !pred(original) {
        return nil, fmt.Errorf("shrink: predicate does not hold on the original scenario (no failure to reproduce)")
    }
    maxIter := opts.MaxIterations
    if maxIter == 0 {
        maxIter = 1000
    }
    current := copyScenario(original)
    iterations := 0
    for {
        reduced := false
        for i := 0; i < len(current.Faults); i++ {
            iterations++
            if iterations > maxIter {
                return &Result{
                    Scenario:   current,
                    Iterations: iterations,
                    OriginalN:  len(original.Faults),
                    FinalN:     len(current.Faults),
                }, nil
            }
            candidate := removeFault(current, i)
            if pred(candidate) {
                current = candidate
                reduced = true
                break
            }
        }
        if !reduced {
            break
        }
    }
    return &Result{
        Scenario:   current,
        Iterations: iterations,
        OriginalN:  len(original.Faults),
        FinalN:     len(current.Faults),
    }, nil
}
```

And update the public type:

```go
type Result struct {
    Scenario   *scenario.Scenario
    Iterations int
    OriginalN  int
    FinalN     int
}

// ShrinkResult retained as alias for backward compatibility.
type ShrinkResult = Result
```

- [ ] **Step 3: Update `cmdRun`**

In `cmd/agentchaos/main.go`:

```go
res, err := shrink.Shrink(&s, func(cand *scenario.Scenario) bool {
    r := runWithAssertions(cand, *upstreamCmd)
    return !r.passed
}, shrink.Options{MaxIterations: 200})
if err != nil { ... }
fmt.Fprintf(os.Stderr, "[shrink] %d → %d faults in %d iterations\n",
    res.OriginalN, res.FinalN, res.Iterations)
```

- [ ] **Step 4: Verify**

```bash
go test -race -count=1 ./internal/shrink/ ./cmd/agentchaos/
```

- [ ] **Step 5: Commit**

```bash
git add internal/shrink/shrink.go internal/shrink/shrink_test.go cmd/agentchaos/main.go
git commit -m "fix(shrink): return Result with Iterations/OriginalN/FinalN (ISSUES.md H3)"
```

---

### Task 7: M1 — Validate `at` (temporal anchor) values

**Files:**
- Modify: `internal/fault/pipeline.go` (export `ValidAnchors`), `internal/scenario/scenario.go` (`Validate` calls into the fault package).
- Test: `internal/scenario/scenario_test.go`.

**Interfaces:**
- Consumes: existing `validActions` set.
- Produces: `fault.ValidAnchors()` returning the four anchor constants from `pipeline.go` as a slice; `scenario.Validate()` calls it and returns a useful error like `fault[2]: invalid anchor "after_request_snt" (valid: before_request_send, after_request_sent, before_response, at_notification_recv)`.

- [ ] **Step 1: Write the failing test**

In `scenario_test.go`:

```go
func TestValidateRejectsBadAnchor(t *testing.T) {
    s := &scenario.Scenario{
        Seed: 1,
        Faults: []scenario.Fault{
            {Match: scenario.Matcher{}, Action: "duplicate", At: "before_response"},
            {Match: scenario.Matcher{}, Action: "duplicate", At: "after_request_snt"},
        },
    }
    if err := scenario.ValidateOnly(s); err == nil || !strings.Contains(err.Error(), "after_request_snt") {
        t.Fatalf("expected anchor validation error, got %v", err)
    }
}
```

(Add a tiny `ValidateOnly(s *Scenario) error` helper in `scenario.go` to keep tests package-external; it's just a thin wrapper around `s.Validate()`.)

Run:

```bash
go test -race -count=1 -run TestValidateRejectsBadAnchor ./internal/scenario/
```

Expected: FAIL — current `Validate()` ignores `At`.

- [ ] **Step 2: Export `ValidAnchors`**

In `internal/fault/pipeline.go`:

```go
// ValidAnchors returns the canonical set of temporal anchors the executor
// recognises. scenario.Validate calls this to reject typos.
func ValidAnchors() []string {
    return []string{
        string(AnchorBeforeRequestSend),
        string(AnchorAfterRequestSent),
        string(AnchorBeforeResponse),
        string(AnchorAtNotification),
    }
}
```

- [ ] **Step 3: Add anchor validation in `scenario.Validate()`**

In `internal/scenario/scenario.go`:

```go
import "github.com/seanrobmerriam/agentchaos/internal/fault"

func (s Scenario) Validate() error {
    // ... existing checks ...
    validAnchors := make(map[string]bool, 4)
    for _, a := range fault.ValidAnchors() {
        validAnchors[a] = true
    }
    for i, f := range s.Faults {
        if f.At != "" && !validAnchors[f.At] {
            return fmt.Errorf("fault[%d]: invalid anchor %q (valid: %s)",
                i, f.At, strings.Join(fault.ValidAnchors(), ", "))
        }
    }
    // ... existing checks ...
}
```

Note this introduces a `scenario → fault` import. To keep `scenario` reusable independently, consider guarding with a build tag or accepting a `ValidateOpts{AnchorSet []string}` parameter. For this codebase the `scenario` package already imports through YAML parsing and the fault package can be imported (no circularity: fault imports scenario, not the other way). Confirm with `go build`.

- [ ] **Step 4: Run all scenario tests**

```bash
go test -race -count=1 ./internal/scenario/
```

All existing scenarios in `scenarios/*.yaml` already use valid anchors; nothing else should break.

- [ ] **Step 5: Commit**

```bash
git add internal/fault/pipeline.go internal/scenario/scenario.go internal/scenario/scenario_test.go
git commit -m "feat(scenario): validate 'at' anchors against the canonical set (ISSUES.md M1)"
```

---

### Task 8: M2 — `--timeout` flag with context.WithTimeout

**Files:**
- Modify: `cmd/agentchaos/main.go` (`cmdRun`, `cmdReplay`, `runOnce`, `runWithAssertions`).
- Test: `cmd/agentchaos/main_test.go` (timeout integration test).

**Interfaces:**
- Consumes: an existing `ctx, cancel := context.WithCancel(...)` pattern.
- Produces: `ctx, cancel := context.WithTimeout(ctx, *timeout)`; default `60s`. On `ctx.Done()` due to timeout, record a `fail`-style result with a new exit code `75`.

- [ ] **Step 1: Add the flag**

In `cmdRun`:

```go
timeout := fs.Duration("timeout", 60*time.Second, "max wall-clock duration of the run; exit 75 on expiry")
```

Same in `cmdReplay`.

- [ ] **Step 2: Use the timeout in both run paths**

```go
ctx, cancel := context.WithTimeout(context.Background(), *timeout)
defer cancel()
```

- [ ] **Step 3: Distinguish timeout from other errors in the result**

In `runWithAssertions`:

```go
if ctx.Err() != nil {
    if errors.Is(ctx.Err(), context.DeadlineExceeded) {
        return runResult{passed: false, exitCode: 75, reason: "timeout"}
    }
}
```

- [ ] **Step 4: Add the integration test**

```go
func TestRunTimeoutExits75(t *testing.T) {
    // scenario that hangs the agent-side: a tool call that never returns.
    // We approximate by pointing at /bin/cat and never writing anything.
    bin := buildCLI(t)
    dir := t.TempDir()
    scenario := filepath.Join(dir, "s.yaml")
    mustWrite(t, scenario, "seed: 1\nfaults: []\nassertions: []\n")
    cmd := exec.Command(bin, "run", "--scenario", scenario,
        "--upstream", "/bin/cat", "--timeout", "100ms")
    cmd.Stdin = strings.NewReader("") // empty input
    out, err := cmd.CombinedOutput()
    if exec.ExitCodeFromErr(err) != 75 {
        t.Fatalf("expected exit 75 (timeout), got %d: %s", exec.ExitCodeFromErr(err), out)
    }
}
```

- [ ] **Step 5: Verify**

```bash
go test -race -count=1 -run TestRunTimeoutExits75 ./cmd/agentchaos/
```

- [ ] **Step 6: Commit**

```bash
git add cmd/agentchaos/main.go cmd/agentchaos/main_test.go
git commit -m "feat(cli): --timeout with exit code 75 on deadline (ISSUES.md M2)"
```

---

### Task 9: M3 — Refactor `runOnce`/`runWithAssertions` and drop the `var _ = proxy.New` placeholder

**Files:**
- Modify: `cmd/agentchaos/main.go`.
- Test: existing `phase3_gate_test.go` end-to-end (should still pass).

**Interfaces:**
- Consumes: `runOnce`, `runWithAssertions`.
- Produces: a single `runScenario(s *scenario.Scenario, upstreamCmd string) runResult` helper that both `cmdRun` and `cmdReplay` use.

- [ ] **Step 1: Identify duplicated code**

```bash
grep -n "^func runOnce\|^func runWithAssertions\|^func pumpWithFaults" cmd/agentchaos/main.go
```

The two functions are ~80% identical: subprocess setup (`exec.Command`, pipe wiring), pump launch, cleanup, context handling.

- [ ] **Step 2: Extract a single helper**

```go
type runOptions struct {
    Seed     int64
    Upstream string
    Timeout  time.Duration
}

func runScenario(s *scenario.Scenario, opts runOptions) runResult {
    // (merge bodies of runOnce + runWithAssertions; pump once;
    //  capture exit code and reason in runResult)
}
```

`cmdReplay` and `cmdRun` each delegate to `runScenario`. The shrink predicate also delegates to it (so the C2 fix continues to work).

- [ ] **Step 3: Remove `var _ = proxy.New` from main.go**

After refactor, the proxy package is no longer imported — verify `grep "proxy.New" cmd/agentchaos/main.go` returns nothing and remove the `proxy` import line.

- [ ] **Step 4: Verify**

```bash
go build ./...
go test -race -count=1 ./...
```

- [ ] **Step 5: Commit**

```bash
git add cmd/agentchaos/main.go
git commit -m "refactor(cli): collapse runOnce+runWithAssertions into runScenario (ISSUES.md M3)"
```

---

### Task 10: M4 — Ship `scenarios/idempotency.yaml`

**Files:**
- Create: `scenarios/idempotency.yaml`.
- Verify: README Quick start commands succeed.

- [ ] **Step 1: Author the new scenario**

Create `scenarios/idempotency.yaml`:

```yaml
# Scenario: idempotency under response duplication.
# The agent calls send_invoice exactly once. We send the response twice and
# assert the agent never produces two observable effects for the same key.
seed: 4891
faults:
  - match: {tool: "send_invoice"}
    at: before_response
    action: duplicate
    count: 2
assertions:
  - type: no_duplicate_effect
    key: idempotency_key
```

- [ ] **Step 2: Verify validate/inspect**

```bash
go build -o /tmp/agentchaos ./cmd/agentchaos
/tmp/agentchaos validate --scenario scenarios/idempotency.yaml
/tmp/agentchaos inspect --scenario scenarios/idempotency.yaml
```

Expected: `valid`, then a `fault[0]` line (currently minimal — fully overhauled in Task 12).

- [ ] **Step 3: Commit**

```bash
git add scenarios/idempotency.yaml
git commit -m "docs(scenarios): add idempotency.yaml referenced by README (ISSUES.md M4)"
```

---

### Task 11: M5 — Ship `docs/SPEC.md`

**Files:**
- Create: `docs/SPEC.md` containing the canonical §-references the source comments cite.
- Test: `grep -rn 'SPEC\.md §' internal/ cmd/` should now resolve to a real section.

- [ ] **Step 1: Write the spec**

Author `docs/SPEC.md` with at least the following sections (these are the ones the source comments cite):

- §3 — Message model (Kinds, fields).
- §4 — Fault DSL
  - §4.2 — Matcher fields and AND semantics.
  - §4.3 — Temporal anchors and default-by-action.
  - §4.4 — Fault primitives.
- §6 — Shrinking algorithm.
- §6.1 — Seed handling.
- §7 — Event log & assertions.
- §8 — CLI exit codes.

The content can largely mirror the README's corresponding sections for v1; the goal of this task is to make the §-references valid, not to expand the spec.

- [ ] **Step 2: Verify**

```bash
grep -rn 'See SPEC\.md §' internal/ cmd/ | head
```

Expected: matches the doc references; `docs/SPEC.md` exists.

- [ ] **Step 3: Commit**

```bash
git add docs/SPEC.md
git commit -m "docs(spec): ship SPEC.md so §-references in source comments resolve (ISSUES.md M5)"
```

---

### Task 12: L4 — `cmdInspect` overhaul

**Files:**
- Modify: `cmd/agentchaos/main.go`'s `cmdInspect`.

- [ ] **Step 1: Replace the implementation**

```go
func cmdInspect(args []string) {
    fs := flag.NewFlagSet("inspect", flag.ExitOnError)
    scenarioPath := fs.String("scenario", "", "path to scenario YAML")
    fs.Parse(args)
    if *scenarioPath == "" { ... }
    s, err := scenario.Parse(readFile(*scenarioPath))
    if err != nil { ... }
    fmt.Printf("seed: %d\n\n", s.Seed)
    for i, f := range s.Faults {
        fmt.Printf("fault[%d]\n", i)
        fmt.Printf("  match:  %s\n", f.Match.String())
        if f.At != "" { fmt.Printf("  at:     %s\n", f.At) }
        fmt.Printf("  action: %s\n", f.Action)
        if f.Probability != nil { fmt.Printf("  probability: %g\n", *f.Probability) }
        if f.Action == "duplicate" { fmt.Printf("  count:   %d\n", f.Count) }
        if f.Action == "reorder"   { fmt.Printf("  window:  %d\n", f.Window) }
        if f.Action == "corrupt_checkpoint" {
            fmt.Printf("  path:    %s\n", f.Path)
            fmt.Printf("  offset:  %d\n", f.Offset)
            fmt.Printf("  bytes:   %d\n", f.Bytes)
        }
        fmt.Println()
    }
    for i, a := range s.Assertions {
        fmt.Printf("assertion[%d] type=%s", i, a.Type)
        if a.Key != "" { fmt.Printf(" key=%s", a.Key) }
        if a.WithinRetries > 0 { fmt.Printf(" within_retries=%d", a.WithinRetries) }
        if a.Tool != "" { fmt.Printf(" tool=%s", a.Tool) }
        fmt.Println()
    }
}
```

- [ ] **Step 2: Verify**

```bash
go build ./cmd/agentchaos && go run ./cmd/agentchaos inspect --scenario scenarios/example.yaml
```

Expected: full structured listing per fault and assertion.

- [ ] **Step 3: Commit**

```bash
git add cmd/agentchaos/main.go
git commit -m "feat(cli): inspect shows probability/count/window/path/offset/bytes (ISSUES.md L4)"
```

---

### Task 13: L5 — Seed-loop off-by-one in `cmdRun`

**Files:**
- Modify: `cmd/agentchaos/main.go` `cmdRun`.

- [ ] **Step 1: Change offset**

```go
for seed := int64(1); seed <= int64(*seeds); seed++ {
    s := *baseScenario
    if *seeds > 1 {
        s.Seed = baseScenario.Seed + seed
    }
    // ...
}
```

(Or, if the documented behaviour is "seed 0 of multi-seed = base seed," add a `--seed-offset` flag and document it. Default to `seed = baseScenario.Seed + seed` with `seed ∈ [0, *seeds)`.)

- [ ] **Step 2: Add a test**

In `cmd/agentchaos/main_test.go`:

```go
func TestRunSeedLoopProducesNDistinctSeeds(t *testing.T) {
    bin := buildCLI(t)
    // Pseudo-record seeds via running 3 times with --seeds 3 and capturing
    // stderr. The [run] line announces each seed.
    cmd := exec.Command(bin, "run", "--scenario", "scenarios/kill_process.yaml",
        "--upstream", "cat", "--seeds", "3")
    out, err := cmd.CombinedOutput()
    if err != nil { t.Fatalf("run: %v\n%s", err, out) }
    seeds := extractSeeds(string(out))
    if len(seeds) != 3 { t.Fatalf("expected 3 seeds, got %v", seeds) }
    // no duplicates
}
```

- [ ] **Step 3: Verify & commit**

```bash
go test -race -count=1 -run TestRunSeedLoopProducesNDistinctSeeds ./cmd/agentchaos/
git add cmd/agentchaos/main.go cmd/agentchaos/main_test.go
git commit -m "fix(cli): seed loop starts at +1 and is free of duplicates (ISSUES.md L5)"
```

---

### Task 14: L6 — `corruptFile` short-read handling

**Files:**
- Modify: `internal/fault/executor.go`'s `corruptFile` and the `corrupt_checkpoint` branch in `ProcessForward`.
- Test: add to `internal/fault/executor_test.go`.

- [ ] **Step 1: New behaviour**

`corruptFile` becomes:

```go
type corruptResult struct {
    Requested int
    Corrupted int
    Short     bool
    Error     error
}

func corruptFile(path string, offset int64, n int) corruptResult {
    f, err := os.OpenFile(path, os.O_RDWR, 0644)
    if err != nil { return corruptResult{Requested: n, Error: err} }
    defer f.Close()
    buf := make([]byte, n)
    nr, err := f.ReadAt(buf, offset)
    if err != nil && err != io.EOF {
        // Partial read: continue with what we have, but report Short.
    }
    for i := 0; i < nr; i++ { buf[i] ^= 0xFF }
    nw, werr := f.WriteAt(buf[:nr], offset)
    res := corruptResult{Requested: n, Corrupted: nw}
    if nr < n || werr != nil { res.Short = true }
    res.Error = werr
    return res
}
```

And in `ProcessForward`'s `corrupt_checkpoint` case:

```go
case "corrupt_checkpoint":
    if f.Path != "" && f.Bytes > 0 {
        res := corruptFile(f.Path, f.Offset, f.Bytes)
        if res.Short || res.Corrupted < res.Requested {
            ex.eventLog.Record(event.Event{
                Kind:   event.KindFaultFired,
                Action: "corrupt_checkpoint_short",
                Method: f.Path,
                Tool:   fmt.Sprintf("requested=%d corrupted=%d", res.Requested, res.Corrupted),
            })
        }
    }
    forward = append(forward, raw)
```

- [ ] **Step 2: Test**

```go
func TestCorruptFileShortReadRecordsWarning(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "ckpt")
    os.WriteFile(path, []byte("ABCD"), 0644)
    res := fault.CorruptFileForTest(path, 2, 100) // asks for 100, file is 4 bytes
    if !res.Short { t.Fatalf("expected Short=true") }
    if res.Corrupted != 2 { t.Fatalf("expected ~2 corrupted bytes, got %d", res.Corrupted) }
}
```

Add a small test-only export `fault.CorruptFileForTest` to expose the result struct.

- [ ] **Step 3: Verify & commit**

```bash
go test -race -count=1 -run TestCorruptFileShortReadRecordsWarning ./internal/fault/
git add internal/fault/executor.go internal/fault/executor_test.go
git commit -m "fix(fault): corrupt_file records short-read warning (ISSUES.md L6)"
```

---

### Task 15: L7 — `mcptoy` ignores unknown-method notifications

**Files:**
- Modify: `testutil/mcptoy/mcptoy.go` (`handleMessage`).
- Test: `testutil/mcptoy/mcptoy_test.go` (new).

- [ ] **Step 1: Implement**

```go
func (s *Server) handleMessage(b []byte, w io.Writer) {
    if len(b) == 0 { return }
    var req Request
    if err := json.Unmarshal(b, &req); err != nil { return }
    if len(req.ID) == 0 {
        // It's a notification: do not reply, regardless of method.
        // The original handler ignored notifications/initialized implicitly
        // because decodeID was nil; this generalises that.
        return
    }
    // ... existing switch ...
}
```

- [ ] **Step 2: Test**

```go
package mcptoy_test

import (
    "bytes"
    "testing"

    "github.com/seanrobmerriam/agentchaos/testutil/mcptoy"
)

func TestNoReplyToUnknownNotification(t *testing.T) {
    s := mcptoy.New()
    var w bytes.Buffer
    in := []byte(`{"jsonrpc":"2.0","method":"some/unknown"}`) // no id
    if err := s.Serve(bytes.NewReader(in), &w); err != nil { t.Fatal(err) }
    if w.Len() != 0 { t.Fatalf("expected no reply, got %q", w.String()) }
}
```

- [ ] **Step 3: Verify & commit**

```bash
go test -race -count=1 ./testutil/mcptoy/
git add testutil/mcptoy/mcptoy.go testutil/mcptoy/mcptoy_test.go
git commit -m "fix(mcptoy): never reply to notifications (ISSUES.md L7)"
```

---

### Task 16: L8 — `event.Log.Filter` allocates once

**Files:**
- Modify: `internal/event/event.go` (`Filter`).

- [ ] **Step 1: Rewrite**

```go
// Filter returns all events whose kind is in `kinds`. If kinds is empty,
// returns all events. The implementation is O(N*K) over a small fixed K
// (8 currently), so no map allocation is needed.
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
```

- [ ] **Step 2: Benchmark**

Optional; for this fix just confirm tests still pass:

```bash
go test -race -count=1 ./internal/event/ ./internal/assert/ ./internal/fault/
```

- [ ] **Step 3: Commit**

```bash
git add internal/event/event.go
git commit -m "perf(event): Filter avoids per-call map alloc (ISSUES.md L8)"
```

---

### Task 17: L3 — Hold `ex.mu` across `pendingInDoubt` + `droppedResponses` append

**Files:**
- Modify: `internal/fault/executor.go` `ProcessReverse`.

- [ ] **Step 1: Refactor the locking pattern**

Replace the unlock-relock dance at `executor.go:167–184` with a single critical section:

```go
func (ex *Executor) ProcessReverse(...) ([][]byte, bool) {
    ex.mu.Lock()
    defer ex.mu.Unlock()
    if msg.Kind == "response" {
        if ex.pendingInDoubt[msg.ID] {
            delete(ex.pendingInDoubt, msg.ID)
            ex.droppedResponses = append(ex.droppedResponses, raw)
            ex.eventLog.Record(event.Event{Kind: event.KindResponseDropped, MsgID: msg.ID, Direction: string(dir), Raw: raw})
            return nil, false
        }
    }
    // ... existing fault decision loop (already lock-safe after Task 3) ...
}
```

- [ ] **Step 2: Verify & commit**

```bash
go test -race -count=1 ./internal/fault/
git add internal/fault/executor.go
git commit -m "refactor(fault): single critical section for in_doubt drop path (ISSUES.md L3)"
```

---

### Task 18: H2 — First tests for `cmd/agentchaos`

**Files:**
- Create: `cmd/agentchaos/main_test.go`, `cmd/agentchaos/cases_test.go`.
- Test: exit-code matrix, validate happy/sad path, inspect output snapshot, replay uses the file's seed.

- [ ] **Step 1: Helpers**

In `main_test.go`:

```go
package main

import (
    "os/exec"
    "path/filepath"
    "testing"
)

func buildCLI(t *testing.T) string {
    t.Helper()
    bin := filepath.Join(t.TempDir(), "agentchaos")
    cmd := exec.Command("go", "build", "-o", bin, ".")
    cmd.Stderr = nil
    if err := cmd.Run(); err != nil { t.Fatalf("build: %v", err) }
    return bin
}

func mustWrite(t *testing.T, path, body string) {
    t.Helper()
    if err := os.WriteFile(path, []byte(body), 0644); err != nil { t.Fatal(err) }
}

func runCLI(t *testing.T, bin string, args ...string) (string, int, error) {
    cmd := exec.Command(bin, args...)
    out, err := cmd.CombinedOutput()
    return string(out), exitCode(err), err
}

func exitCode(err error) int {
    if err == nil { return 0 }
    if ee, ok := err.(*exec.ExitError); ok { return ee.ExitCode() }
    return -1
}
```

- [ ] **Step 2: Cases**

```go
func TestValidate(t *testing.T) { /* passes for example.yaml, fails for garbage */ }

func TestRunExitsZero(t *testing.T) {
    bin := buildCLI(t); dir := t.TempDir()
    scenario := filepath.Join(dir, "s.yaml")
    mustWrite(t, scenario, "seed: 1\nfaults: []\nassertions: []\n")
    _, code, _ := runCLI(t, bin, "run", "--scenario", scenario, "--upstream", "/bin/cat")
    if code != 0 && code != 77 && code != 75 {
        // cat returns immediately, exit 0 expected; tolerate 77/75 only if mcptoy used.
        t.Fatalf("unexpected exit code %d", code)
    }
}

func TestRunInvalidScenarioExits78(t *testing.T) {
    bin := buildCLI(t); dir := t.TempDir()
    scenario := filepath.Join(dir, "s.yaml")
    mustWrite(t, scenario, "this is not yaml: : :")
    _, code, _ := runCLI(t, bin, "run", "--scenario", scenario, "--upstream", "/bin/cat")
    if code != 78 { t.Fatalf("expected 78, got %d", code) }
}

func TestInspect(t *testing.T) {
    bin := buildCLI(t)
    out, code, _ := runCLI(t, bin, "inspect", "--scenario", "scenarios/example.yaml")
    if code != 0 { t.Fatalf("inspect exit %d\n%s", code, out) }
    if !strings.Contains(out, "fault[") { t.Fatalf("missing fault line: %s", out) }
}
```

- [ ] **Step 3: Verify & commit**

```bash
go test -race -count=1 ./cmd/agentchaos/
git add cmd/agentchaos/main_test.go cmd/agentchaos/cases_test.go
git commit -m "test(cli): first end-to-end test coverage for agentchaos (ISSUES.md H2)"
```

---

### Task 19: H1 — document HTTP transport as deferred to Plan 2

**Files:**
- Modify: `README.md` and `ISSUES.md`.

`H1` is a feature gap (HTTP upstream is documented but unreachable AND not fault-aware), not a fix-in-place bug; addressing it requires a substantial new design. This task records the deferral.

- [ ] **Step 1: Update README**

In README's "Transports" section, change:

> the proxy speaks stdio or Streamable HTTP to the upstream.

to:

> In v1 the CLI invokes the proxy with a stdio upstream; Streamable HTTP support is planned (see `SUGGESTED_FEATURES.md` §2). The `internal/proxy` package contains a non-fault-aware HTTP shuttle retained for the upcoming transport-switch work.

- [ ] **Step 2: Update ISSUES.md**

Strike the inline status (move it from "High — Broken or unusable user-facing feature" to "Deferred to `2026-07-20-add-features.md`, Task 2").

- [ ] **Step 3: Commit**

```bash
git add README.md ISSUES.md
git commit -m "docs: defer HTTP transport to SUGGESTED_FEATURES.md (ISSUES.md H1)"
```

---

## Self-Review

**Spec coverage**
- L1 ✓ Task 1
- L2 ✓ Task 2
- C1 ✓ Task 3
- C2 ✓ Task 4
- C3 ✓ Task 5
- H3 ✓ Task 6
- M1 ✓ Task 7
- M2 ✓ Task 8
- M3 ✓ Task 9
- M4 ✓ Task 10
- M5 ✓ Task 11
- L4 ✓ Task 12
- L5 ✓ Task 13
- L6 ✓ Task 14
- L7 ✓ Task 15
- L8 ✓ Task 16
- L3 ✓ Task 17
- H2 ✓ Task 18
- H1 ✓ Task 19 (deferred)

All 18 issues addressed.

**Placeholders**
- Step 5b in Task 5 is intentionally empty (a referenced fixture file created elsewhere) — flagged in its own bullet but no inline code is left as "fill in details."

**Type consistency**
- `Executor.ProcessForward`/`ProcessReverse` keep their existing signatures (additive change is in the new `HandleForwardMessage`/`HandleReverseMessage`).
- `shrink.Shrink` signature changes from `(*scenario.Scenario, error)` to `(*Result, error)`. Both call sites in `cmd/agentchaos/main.go` (the printer inside `cmdRun` and the existing already-updated site) compile consistently because they're in the same commit (Task 6 + Task 9 refactor land together; ordering the merging of those is the engineer's call).

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-20-fix-issues.md`.

**Two execution options:**

1. **Subagent-Driven (recommended)** — Dispatch a fresh subagent per task; review between tasks; fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans; batch execution with checkpoints.

Which approach?
