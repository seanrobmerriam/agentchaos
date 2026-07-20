# ISSUES.md — AgentChaos Code Review

Review performed 2026-07-20 against the tree at
`/home/sean/Documents/current_projects/agentchaos`.

At review time: `go build ./...` ✅, `go vet ./...` ✅,
`go test -race -count=1 ./...` ✅ (all packages green). The issues below are
therefore not "does it compile/run" failures but correctness, design,
documentation, and hygiene problems found by reading the code and writing
targeted probes.

## Severity legend

- 🔴 **Critical** — wrong results / violated core guarantee / data corruption.
- 🟠 **High** — broken or unusable user-facing feature, or major untested surface.
- 🟡 **Medium** — degraded UX, maintenance hazard, or latent bug.
- 🔵 **Low** — cleanup, dead code, style.

---

## 🔴 C1. Data race on the PRNG and fault schedule → determinism guarantee is violated in real use

**Where:** `internal/fault/executor.go`
- `splitMix64.next()` at `executor.go:365` mutates `s.state` with **no synchronization**.
- `Executor.shouldFire` calls `prng.Float64()` (`executor.go:282` → `:376`) and appends to
  `ex.schedule` (`executor.go:289`) **without holding `ex.mu`**.
- `Schedule()` reads `ex.schedule` *under* `ex.mu` (`executor.go:243`), so writers and
  readers disagree on locking.

**Reproduced.** A 4-goroutine test that mimics the concurrent forward/reverse
pump in `cmd/agentchaos` trips the race detector:

```
WARNING: DATA RACE
Read at 0x... by goroutine 10: splitMix64.next() executor.go:366
Previous write at 0x... by goroutine 13: splitMix64.next() executor.go:366
```

**Impact.** The README's headline promise — *"Same seed and scenario produce
a byte-identical fault schedule"* — is **false under the actual stdio pump**.
`pumpWithFaults` (`cmd/agentchaos/main.go:339`) runs the forward and reverse
pumps in two goroutines, so both call `ProcessForward`/`ProcessReverse` →
`shouldFire` → `prng.next()` concurrently. The PRNG state is consumed in a
nondeterministic interleaving, so:

1. Two runs of the same seed can produce **different** fault schedules
   (different probability outcomes, different reorder permutations).
2. `--shrink-on-failure` is unreliable: the predicate is "does this reduced
   scenario still fail?", but the failure itself is scheduling-dependent.
3. It is a genuine `-race` data race, not just a logical nondeterminism.

The determinism property tests pass only because `runSequence`
(`internal/fault/determinism_test.go`) processes messages **sequentially in a
single goroutine**, which never exercises the concurrent path.

**Fix.** Serialize all PRNG consumption and schedule mutation behind
`ex.mu`. Either (a) hold `ex.mu` across `shouldFire` (simplest), or (b)
pre-compute per-fault probability rolls per message under the lock, or (c)
give `splitMix64` its own mutex and protect `schedule` with `ex.mu`.
Recommended: hold `ex.mu` for the whole `shouldFire`+schedule-append, and add
a concurrent-execution determinism test (two `pumpWithFaults`-style runs with
the same seed must agree).

---

## 🔴 C2. `kill_process` calls `os.Exit` from a pump goroutine → breaks cleanup and `--shrink-on-failure`

**Where:** `internal/fault/executor.go:117` (`ex.exitFn(77)`), default
`ExitProcess = os.Exit` at `executor.go:401`; wired in
`cmd/agentchaos/main.go:134` and `:277` via `fault.ExitProcess`.

**Impact.**
1. `os.Exit` skips all deferred cleanup (`cmd.Process.Kill`/`Wait` at
   `main.go:157`, `:182`, `:307`), orphaning/zombie-ing the upstream
   subprocess.
2. Assertion checking (`runWithAssertions`, `main.go:172`–`198`) **never
   runs** for a `kill_process` scenario — the process dies from the goroutine
   before the pump returns. So `kill_process` scenarios can only ever yield
   exit code 77, never an assertion verdict.
3. **Worst:** during `--shrink-on-failure`, the shrink *predicate*
   (`main.go:96`) calls `runWithAssertions(cand, …)` for each candidate. If a
   candidate still contains a `kill_process` fault, `os.Exit(77)` fires from
   inside the predicate and **terminates the entire `agentchaos run`
   process**, aborting the shrink search mid-stream. Shrink-on-failure is
   effectively broken for any scenario that uses `kill_process`.

**Fix.** Replace the in-band `os.Exit` with a signal: `ProcessForward`
already returns `kill bool`. The pump should record `exitCode=77` and
**return normally** (close pipes, run assertions, kill upstream via defer),
then `main` exits once. For shrink, the predicate must never call the real
`os.Exit` — inject a recording `exitFn` that only sets a flag.

---

## 🔴 C3. `KindCheckpointCommit` and `KindTerminalState` events are never emitted → two built-in assertions are unusable

**Where:** `internal/event/event.go` defines `KindCheckpointCommit` and
`KindTerminalState`; `internal/assert/assert.go` reads them in
`checkEffectWithoutCheckpoint` and `checkTerminalStateReached`. A grep for
`Record` of either kind in non-test code returns **zero hits** — nothing in
the proxy, executor, or CLI ever records these events.

**Impact.**
- `terminal_state_reached` **always fails** (`len(terminalEvents) == 0`).
- `effect_without_checkpoint_commit` **always fails** whenever any
  `tools/call` response is delivered, because `committedMsgIDs` is always
  empty.

Two of the three documented built-in assertions cannot pass in any real run.
The two example scenarios `corrupt_checkpoint.yaml` and `example.yaml` rely on
exactly these assertions. There is no documented hook or API by which an
agent's internal "checkpoint committed" / "terminal state reached" signals
enter the event log — the proxy only sees JSON-RPC messages, not agent
internals.

**Fix.** Define and document an ingestion path: either (a) a well-known
JSON-RPC notification (e.g. `notifications/agentchaos/checkpoint_commit`) the
proxy translates into the corresponding event, (b) an explicit
`event.Log.Record` API the agent side calls via a side channel, or (c) a
custom-verifier-based convention. Until one exists, mark these assertions as
"requires external event source" in the README and validate.

---

## 🟠 H1. Streamable HTTP upstream is not wired into the CLI and is not fault-aware

**Where:** `internal/proxy/http_proxy.go` implements `HTTPProxy`, but
`cmd/agentchaos/main.go` hard-codes `fault.TransportStdio` (`:134`, `:277`)
and spawns a subprocess via `exec.Command`. There is no `--transport` flag.
`HTTPProxy.NewHTTP`/`Run` take **no `*fault.Executor`**, so even if exposed,
no fault would be applied to HTTP upstream traffic — `HTTPProxy` is a plain
byte shuttle.

**Impact.** The README "Transports" section advertises Streamable HTTP
support ("the proxy speaks stdio or Streamable HTTP to the upstream"). In v1
this is unreachable from the CLI and incapable of fault injection. The
`reorder` fault is HTTP-only by design (`NewExecutorForTransport` rejects it
for stdio), yet there is no way to reach the HTTP path.

**Fix.** Add `--transport stdio|http` and `--upstream-url` flags; refactor
`HTTPProxy` to accept an executor and route each parsed JSON-RPC frame through
`ProcessForward`/`ProcessReverse` (mirroring the stdio pump). Add an
end-to-end HTTP fault test.

---

## 🟠 H2. `cmd/agentchaos` (the entire user-facing CLI, ~440 LOC) has zero tests

**Where:** `go test` reports `? github.com/seanrobmerriam/agentchaos/cmd/agentchaos [no test files]`.

**Impact.** Argument parsing, exit codes (0/70/77/78), the seed loop,
shrink-on-failure wiring, `pumpWithFaults`'s concurrent pump, and
`trimTrailingNewline` are all unverified at the CLI level. The subprocess
integration tests live under `internal/fault` and `internal/proxy` and build
the binary via `buildProxyBinary`, but no test exercises `main.go`'s own
branching. This is the largest untested surface in the project and the place
where C1/C2 manifest.

**Fix.** Add a `cmd/agentchaos` test package that builds the binary and
asserts: exit codes for valid/invalid scenarios, `validate`/`inspect` output,
`run` against `testutil/mcptoy` for a passing and a failing assertion, and
shrink producing a smaller reproducer file.

---

## 🟠 H3. `shrink.Shrink` discards its own `Result` — exported stats are dead

**Where:** `internal/shrink/shrink.go:86`
```go
_ = Result{Scenario: current, Iterations: iterations, OriginalN: len(original.Faults), FinalN: len(current.Faults)}
return current, nil
```
`Shrink` returns `(*scenario.Scenario, error)`, so `Result` (and
`Iterations`, `OriginalN`, `FinalN`) can never reach a caller. `main.go:104`
recomputes `len(s.Faults)`/`len(shrunk.Faults)` by hand because it can't get
the stats.

**Impact.** The exported `Result`/`ShrinkResult` types and their fields are
dead API; iteration budget reporting is lost; the `_ = Result{...}` line is a
bug-hiding placeholder.

**Fix.** Change the signature to `(*Result, error)` (or add a sibling
`ShrinkWithStats`), populate and return the `Result`; update `main.go`.

---

## 🟡 M1. Temporal anchor (`at`) values are not validated — typos silently never match

**Where:** `internal/scenario/scenario.go` `Validate()` checks `action`,
`match.type`, `match.method` emptiness, `count`, `window`, `path`, `bytes`,
`probability` — but **not** `at`. Matching then does
`f.At != "" && f.At != string(anchor)` (`executor.go:278`), so a typo like
`at: after_request_snt` makes the fault match nothing, silently, with no
error.

**Impact.** Scenarios that look correct do nothing; users get false "all
seeds passed" greens. The README enumerates the valid anchors; the parser
should too.

**Fix.** Add `validAnchors` and validate `f.At` (when non-empty) against it
in `Validate()`; emit exit code 78 on violation.

---

## 🟡 M2. No run timeout — a hung upstream or non-EOF agent stdin hangs the CLI forever

**Where:** `cmd/agentchaos/main.go:164` comments *"Run the pump in a
goroutine with a timeout."* but no `time.After`/`context.WithTimeout` is
wired. The `select` at `:176` only watches `ctx.Done()`, and `ctx` is
`context.Background()` with a `cancel` that is never triggered by a timer.

**Impact.** In CI, a misbehaving upstream (no EOF) or an agent that never
closes stdin causes `agentchaos run` to block indefinitely. Bad for CI
determinism (ironic, given the determinism focus).

**Fix.** `context.WithTimeout(ctx, --timeout)` (default e.g. 60s) and treat
timeout as a failure with a distinct exit code.

---

## 🟡 M3. `internal/proxy.Proxy` (stdio) is dead in the CLI path; logic is duplicated

**Where:** `cmd/agentchaos/main.go:438` `var _ = proxy.New // placeholder
until proxy is wired into the CLI`. `pumpWithFaults` (`main.go:339`)
reimplements newline-delimited copying instead of using `proxy.Proxy`/`copyLines`
(`internal/proxy/proxy.go`). `runWithAssertions` (`main.go:132`) and
`runOnce` (`main.go:275`) are ~80% identical (subprocess setup, pipe wiring,
pump launch, cleanup).

**Impact.** Two sources of truth for line framing; the tested `proxy`
package is bypassed in production; ~70 lines of duplicated subprocess
boilerplate that must be kept in sync by hand.

**Fix.** Have `pumpWithFaults` compose `proxy`-style copying with the
executor, or make `proxy.Proxy` accept an executor hook. Factor the
subprocess setup in `main.go` into one helper used by both `run` and
`replay`.

---

## 🟡 M4. README Quick start references `scenarios/idempotency.yaml`, which does not exist

**Where:** `README.md` Quick start shows
`--scenario scenarios/idempotency.yaml` for `run`, `replay`, `validate`, and
`inspect`. `ls scenarios/` yields only `corrupt_checkpoint.yaml`,
`duplicate.yaml`, `example.yaml`, `in_doubt.yaml`, `kill_process.yaml`.

**Impact.** Every copy-pasted Quick start command fails with a read error.
First-run experience is broken.

**Fix.** Either add `scenarios/idempotency.yaml` or change the README
examples to `scenarios/example.yaml`.

---

## 🟡 M5. `SPEC.md` is referenced throughout the source but is absent from the repo

**Where:** Package/file comments cite "SPEC.md §7" (`internal/event/event.go`),
"§4.3" (`internal/fault/pipeline.go`), "§6" (`internal/shrink/shrink.go`),
"§4.2"/"§4.3"/"§4.4"/"§3" (`internal/scenario/scenario.go`), "§6.1", "§8"
(`internal/scenario/scenario.go`, `cmd/agentchaos/main.go`). `ls SPEC.md` →
no such file.

**Impact.** Reviewers and contributors following the §-citations hit dead
references; the canonical design doc is missing.

**Fix.** Commit `SPEC.md` (or a `docs/` equivalent) and point the comments at
it; if it was intentionally dropped, rewrite the comments to be
self-contained.

---

## 🔵 L1. `gofmt -l` reports 23 of 28 Go files as not formatted

**Where:** `gofmt -l .` lists 23 files (essentially every `.go` file).
Diff sample: misaligned struct-field columns in `scenario.Assertion`,
`Executor`, `NewExecutor*`; missing trailing newline at EOF in
`scenario.go`.

**Impact.** Fails the standard `gofmt -l` CI gate; noisy diffs in any
future edit.

**Fix.** `gofmt -w .` (one-shot) and add a `gofmt -l` check to CI.

## 🔵 L2. Dead code / placeholder hacks

- `internal/fault/executor.go:463` — `var _ = math.Pi` keeps an unused
  `math` import alive; `math` is not actually used by `Float64`.
- `internal/scenario/scenario.go:227` — `var _ = matchIDWildcard` plus
  `matchTypeIDWildcard` ("reserved for future use") — unused.
- `cmd/agentchaos/main.go:438` — `var _ = proxy.New` placeholder (see M3).
- `internal/fault/executor.go:401` — `ExitProcess` is an exported var used
  only as a default arg in `main.go`; consider unexporting or folding into
  the constructor.

**Fix.** Delete the unused `math` import and the `var _ = math.Pi` line;
remove `matchIDWildcard`/`matchTypeIDWildcard` or wire them up; resolve M3.

## 🔵 L3. `ProcessReverse` unlock-then-relock around `pendingInDoubt`/`droppedResponses`

**Where:** `internal/fault/executor.go:167`–`184` — locks, checks/deletes
`pendingInDoubt`, **unlocks**, then immediately **re-locks** to append to
`droppedResponses` and `Record`. Not a data race (the map entry was already
deleted), but it's an unnecessary lock release/reacquire that complicates
reasoning.

**Fix.** Hold `ex.mu` across both the `pendingInDoubt` check and the
`droppedResponses` append (the `event.Log.Record` is already internally
locked, so nesting is safe).

## 🔵 L4. `cmdInspect` shows almost no fault detail

**Where:** `cmd/agentchaos/main.go` `cmdInspect` prints only
`fault[i]: <matcher> <action>`. It omits `probability`, `count`, `window`,
`path`, `offset`, `bytes`, and `at` — the things a user most wants to
"inspect".

**Fix.** Print all populated fields per fault (and per assertion).

## 🔵 L5. `run` seed loop off-by-one when `--seeds > 1`

**Where:** `cmd/agentchaos/main.go:74`–`77`:
```go
for seed := int64(0); seed < int64(*seeds); seed++ {
    s := *baseScenario
    if *seeds > 1 { s.Seed = baseScenario.Seed + seed }
```
When `--seeds > 1`, the first iteration uses `baseScenario.Seed + 0`, i.e. the
same seed as the `--seeds 1` case, so the documented base seed is "tried
twice" relative to the multi-seed space and the total distinct seeds is
`seeds` (correct count) but the range starts at the base rather than base+1.

**Fix.** Start the offset at 1 when extending, or document that seed 0 of a
multi-seed run equals the YAML seed.

## 🔵 L6. `corruptFile` silently corrupts fewer bytes than requested on short read

**Where:** `internal/fault/executor.go` `corruptFile` does `ReadAt(buf, offset)`
then XORs `buf`; if `offset+n` exceeds EOF, `ReadAt` returns
`io.ErrUnexpectedEOF` and the executor swallows it as "non-fatal" — so a
`corrupt_checkpoint` fault that targets bytes beyond the current file size
**does nothing, silently**. The scenario author gets no signal that the
corruption didn't happen.

**Fix.** On short read, corrupt the bytes that *were* read and record a
`fault_fired`/warning event noting the short count; or validate the file size
at fire time and record a failure event.

## 🔵 L7. `mcptoy` replies to unknown-method notifications (JSON-RPC violation)

**Where:** `testutil/mcptoy/mcptoy.go` `handleMessage` `default` branch sends
an error response with `"id": decodeID(req.ID)`. For a notification (no `id`)
`decodeID` returns `nil`, producing `{"id": null, "error": {...}}`. JSON-RPC
2.0 §5 says servers MUST NOT respond to notifications.

**Impact.** Test-only utility, low severity, but can confuse integration
tests that count messages on the wire.

**Fix.** In `handleMessage`, if `len(req.ID) == 0` and method is unknown,
return without replying.

## 🔵 L8. `event.Log.Filter` allocates a fresh set map on every call

**Where:** `internal/event/event.go` `Filter` builds a `map[Kind]bool` per
invocation. `assert.Check` calls `log.Filter` several times per assertion per
run; during shrink this is called many times.

**Impact.** Minor GC pressure; not a correctness issue.

**Fix.** Package-level `var` sentinel set, or accept variadic kinds and do a
linear scan over a small slice (kinds are few).
