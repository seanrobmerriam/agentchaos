// Command agentchaos is the CLI entry point for the AgentChaos fault proxy.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/assert"
	"github.com/seanrobmerriam/agentchaos/internal/event"
	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
	"github.com/seanrobmerriam/agentchaos/internal/shrink"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "replay":
		cmdReplay(os.Args[2:])
	case "validate":
		cmdValidate(os.Args[2:])
	case "inspect":
		cmdInspect(os.Args[2:])
	case "lint":
		cmdLint(os.Args[2:])
	case "explain":
		cmdExplain(os.Args[2:])
	case "risk":
		cmdRisk(os.Args[2:])
	case "fuzz":
		cmdFuzz(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `agentchaos — fault injection proxy for MCP workflows

Usage:
  agentchaos run      --scenario <path> [--upstream <cmd>] [--seeds N] [--shrink-on-failure] [--no-shrink] [--shrink-strategy greedy|bisect] [--shrink-max-iter N] [--stop-on first|all] [--event-log <path>]
  agentchaos replay   --seed <uint64> --scenario <path> [--upstream <cmd>]
  agentchaos validate --scenario <path>
  agentchaos inspect  --scenario <path>
  agentchaos lint     --scenario <path>
  agentchaos explain  --event-log <path>
  agentchaos fuzz     --upstream <cmd> [--scenario <path>] [--runs N] [--max-faults N]`)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	upstreamCmd := fs.String("upstream", "", "upstream command (e.g. 'npx -y @modelcontextprotocol/server-everything stdio')")
	transport := fs.String("transport", "stdio", "upstream transport: stdio|http")
	upstreamURL := fs.String("upstream-url", "", "upstream URL for --transport http")
	seeds := fs.Int("seeds", 1, "number of seeds to try")
	shrinkOnFailure := fs.Bool("shrink-on-failure", false, "shrink the fault schedule on failure")
	noShrink := fs.Bool("no-shrink", false, "shorthand for --shrink-on-failure=false")
	shrinkStrategy := fs.String("shrink-strategy", "greedy", "shrink strategy: greedy|bisect")
	shrinkMaxIter := fs.Int("shrink-max-iter", 200, "max shrink iterations")
	stopOn := fs.String("stop-on", "first", "stop after first failing seed (first) or run all (all)")
	reproducerPath := fs.String("reproducer", "", "path to write minimal reproducer scenario on failure")
	timeout := fs.Duration("timeout", 60*time.Second, "max wall-clock duration; exit 75 on deadline")
	eventLogPath := fs.String("event-log", "", "write the event log as NDJSON to this path")
	fs.Parse(args)

	if *stopOn != "first" && *stopOn != "all" {
		fmt.Fprintf(os.Stderr, "run: --stop-on must be 'first' or 'all' (got %q)\n", *stopOn)
		os.Exit(2)
	}
	if *transport != "stdio" && *transport != "http" {
		fmt.Fprintf(os.Stderr, "run: --transport must be 'stdio' or 'http' (got %q)\n", *transport)
		os.Exit(2)
	}
	if *transport == "http" && *upstreamURL == "" {
		fmt.Fprintln(os.Stderr, "run: --transport http requires --upstream-url")
		os.Exit(2)
	}
	effectiveShrink := *shrinkOnFailure && !*noShrink

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "run: --scenario is required")
		os.Exit(1)
	}

	baseScenario, err := scenario.Parse(readFile(*scenarioPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scenario error: %v\n", err)
		os.Exit(78)
	}

	worstExit := 0
	for seed := int64(1); seed <= int64(*seeds); seed++ {
		s := *baseScenario
		if *seeds > 1 {
			s.Seed = baseScenario.Seed + seed
		}

		// Run the scenario + assertions.
		result := runWithAssertions(&s, *upstreamCmd, *timeout)

		// Export the event log as NDJSON if --event-log is set. Done
		// unconditionally per-seed so the log survives early exits.
		writeEventLog(*eventLogPath, result.eventLog, s.Seed)

		if result.passed && result.exitCode == 0 {
			continue // no failure found; try next seed
		}

		// Failure found.
		failedSeed := s.Seed
		fmt.Fprintf(os.Stderr, "[run] seed %d triggered failure: %s\n",
			failedSeed, result.reason)

		if effectiveShrink {
			fmt.Fprintln(os.Stderr, "[shrink] shrinking fault schedule...")
			res, err := shrink.Shrink(&s, func(cand *scenario.Scenario) bool {
				// Predicate: does this reduced scenario still fail
				// assertions with the same seed?
				r := runWithAssertions(cand, *upstreamCmd, *timeout)
				return !r.passed
			}, shrink.Options{MaxIterations: *shrinkMaxIter, Strategy: shrink.Strategy(*shrinkStrategy)})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[shrink] error: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "[shrink] %d → %d faults in %d iterations\n",
					res.OriginalN, res.FinalN, res.Iterations)
				if *reproducerPath != "" {
					body, _ := scenario.Marshal(res.Scenario)
					header := fmt.Sprintf("# shrunk from seed %d via %d iterations\n---\n", failedSeed, res.Iterations)
					out := append([]byte(header), body...)
					if werr := os.WriteFile(*reproducerPath, out, 0644); werr != nil {
						fmt.Fprintf(os.Stderr, "[shrink] write reproducer: %v\n", werr)
					} else {
						fmt.Fprintf(os.Stderr, "[shrink] reproducer written to %s\n", *reproducerPath)
					}
				}
			}
		}

		if *stopOn == "first" {
			os.Exit(result.exitCode)
		}
		if result.exitCode > worstExit {
			worstExit = result.exitCode
		}
	}

	if worstExit != 0 {
		fmt.Fprintf(os.Stderr, "[run] %d seed(s) failed; exiting with worst code %d\n", *seeds, worstExit)
		os.Exit(worstExit)
	}
	// All seeds passed.
	fmt.Fprintf(os.Stderr, "[run] all %d seeds passed\n", *seeds)
	os.Exit(0)
}

// runResult captures the outcome of a single scenario run.
//
// exitCode is the public exit code: 0 on success, 70 on assertion
// failure, 75 on timeout, 78 on executor/parse error. pumpCode is the
// raw code reported by the pump (77 for kill_process, 0 for normal
// exit) and is surfaced by runOnce but masked to 0 by runWithAssertions
// per the C2 contract.
type runResult struct {
	passed   bool
	exitCode int
	pumpCode int
	reason   string
	eventLog *event.Log // captured for --event-log export; nil if no executor ran
}

// runScenario builds the fault executor, launches the upstream subprocess,
// pumps agent I/O through the executor, and checks scenario assertions on
// completion. It is the shared body of runWithAssertions and runOnce.
//
// Returns the captured exit code (0 on clean pump completion, 70 on
// assertion failure, 75 on timeout, 78 on executor/parse error, 77 if
// the pump was killed via kill_process). The passed flag is true only
// when the pump returned 0 AND no assertions failed.
func runScenario(s *scenario.Scenario, upstreamCmd string, timeout time.Duration) runResult {
	// Signal-only exit callback: kill_process only flips the boolean
	// returned by ProcessForward; the pump then sets pumpCode=77. No
	// os.Exit is ever fired from inside a goroutine.
	ex, err := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)
	if err != nil {
		return runResult{passed: false, exitCode: 78, reason: fmt.Sprintf("executor: %v", err)}
	}
	// eventLog is captured here so every return path after this point can
	// surface the in-flight log to the caller (for --event-log export).
	eventLog := ex.EventLog()

	parts := strings.Fields(upstreamCmd)
	if len(parts) == 0 {
		return runResult{passed: false, exitCode: 1, reason: "no upstream command", eventLog: eventLog}
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stderr = os.Stderr
	upIn, err := cmd.StdinPipe()
	if err != nil {
		return runResult{passed: false, exitCode: 1, reason: fmt.Sprintf("stdin: %v", err), eventLog: eventLog}
	}
	upOut, err := cmd.StdoutPipe()
	if err != nil {
		return runResult{passed: false, exitCode: 1, reason: fmt.Sprintf("stdout: %v", err), eventLog: eventLog}
	}
	if err := cmd.Start(); err != nil {
		return runResult{passed: false, exitCode: 1, reason: fmt.Sprintf("start: %v", err), eventLog: eventLog}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Run the pump in a goroutine with a timeout.
	doneCh := make(chan int, 1)
	go func() {
		code := pumpWithFaults(ctx, os.Stdin, os.Stdout, upOut, upIn, upIn, ex)
		doneCh <- code
	}()

	pumpDone := false
	var pumpCode int
	select {
	case pumpCode = <-doneCh:
		pumpDone = true
	case <-ctx.Done():
	}

	if !pumpDone {
		cancel()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		pumpCode = <-doneCh
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return runResult{passed: false, exitCode: 75, pumpCode: pumpCode, reason: "timeout", eventLog: eventLog}
	}

	// Check assertions against the event log.
	if len(s.Assertions) > 0 {
		results := assert.CheckAll(s.Assertions, ex.EventLog())
		if assert.AnyFailed(results) {
			var reasons []string
			for i, r := range results {
				if r.Failed {
					reasons = append(reasons, fmt.Sprintf("%s: %s", s.Assertions[i].Type, r.Reason))
				}
			}
			return runResult{
				passed:   false,
				exitCode: 70, // SPEC §8: assertion failure
				pumpCode: pumpCode,
				reason:   strings.Join(reasons, "; "),
				eventLog: eventLog,
			}
		}
	}

	// Success: mask the raw pump code (e.g. 77 from kill_process) to 0
	// per the C2 contract — kill_process signals the run rather than
	// failing it. runOnce exposes the raw code separately.
	return runResult{passed: pumpCode == 0, exitCode: 0, pumpCode: pumpCode, eventLog: eventLog}
}

// runWithAssertions delegates to runScenario, preserving the runResult
// return type used by the shrink predicate (C2 fix).
func runWithAssertions(s *scenario.Scenario, upstreamCmd string, timeout time.Duration) runResult {
	return runScenario(s, upstreamCmd, timeout)
}

func cmdReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	upstreamCmd := fs.String("upstream", "", "upstream command")
	seed := fs.Int64("seed", 0, "override the scenario's seed (0 = use the file's seed)")
	timeout := fs.Duration("timeout", 60*time.Second, "max wall-clock duration; exit 75 on deadline")
	fs.Parse(args)

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "replay: --scenario is required")
		os.Exit(1)
	}

	s, err := scenario.Parse(readFile(*scenarioPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scenario error: %v\n", err)
		os.Exit(78)
	}
	// Only override the seed when the flag was explicitly provided.
	if *seed != 0 {
		s.Seed = *seed
	}
	os.Exit(runOnce(s, *upstreamCmd, *timeout))
}

func cmdValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	fs.Parse(args)

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "validate: --scenario is required")
		os.Exit(1)
	}

	_, err := scenario.Parse(readFile(*scenarioPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid: %v\n", err)
		os.Exit(78)
	}
	fmt.Println("valid")
}

func cmdLint(args []string) {
	fs := flag.NewFlagSet("lint", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	fs.Parse(args)
	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "lint: --scenario is required")
		os.Exit(1)
	}
	s, err := scenario.Parse(readFile(*scenarioPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint: scenario parse: %v\n", err)
		os.Exit(78)
	}
	diags := scenario.Lint(s)
	hasErr := false
	for _, d := range diags {
		fmt.Fprintf(os.Stderr, "%s: %s: %s\n", d.Severity, d.Location, d.Message)
		if d.Severity == scenario.LintError {
			hasErr = true
		}
	}
	if hasErr {
		os.Exit(78)
	}
}

// runOnce delegates to runScenario and surfaces the raw pump code
// (77 for kill_process). On timeout it returns 75.
func runOnce(s *scenario.Scenario, upstreamCmd string, timeout time.Duration) int {
	res := runScenario(s, upstreamCmd, timeout)
	if res.exitCode == 75 {
		return 75
	}
	return res.pumpCode
}

// pumpWithFaults is the v1 message-level fault-injecting pump. It reads
// newline-delimited JSON from agentIn, parses each line into a Message,
// runs it through the executor, forwards the result(s) to upstreamOut,
// reads responses from upstreamIn, runs them through the executor, and
// forwards the result(s) to agentOut.
//
// upstreamOutCloser is the upstream's stdin pipe write end (from exec.Cmd);
// it is closed when the forward pump finishes so the upstream server sees
// EOF and terminates.
func pumpWithFaults(ctx context.Context, agentIn io.Reader, agentOut io.Writer, upstreamIn io.Reader, upstreamOut io.Writer, upstreamOutCloser io.Closer, ex *fault.Executor) int {
	fwdDone := make(chan struct{})
	revDone := make(chan struct{})
	var exitCode int
	var exitOnce sync.Once

	exitNow := func(code int) {
		exitOnce.Do(func() { exitCode = code })
	}

	// Forward pump: agent -> upstream
	go func() {
		defer close(fwdDone)
		sc := bufio.NewReader(agentIn)
		for {
			line, err := sc.ReadBytes('\n')
			if len(line) > 0 {
				msg := scenario.ParseMessage(line)
				trimmed := trimTrailingNewline(line)
				forward, killed := ex.HandleForwardMessage(msg, trimmed, fault.AgentToUpstream)
				for _, b := range forward {
					if _, werr := upstreamOut.Write(append(b, '\n')); werr != nil {
						return
					}
				}
				if killed {
					exitNow(77)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Reverse pump: upstream -> agent
	go func() {
		defer close(revDone)
		sc := bufio.NewReader(upstreamIn)
		for {
			line, err := sc.ReadBytes('\n')
			if len(line) > 0 {
				msg := scenario.ParseMessage(line)
				trimmed := trimTrailingNewline(line)
				forward, _ := ex.HandleReverseMessage(msg, trimmed, fault.UpstreamToAgent)
				for _, b := range forward {
					if _, werr := agentOut.Write(append(b, '\n')); werr != nil {
						return
					}
				}
			}
			if err != nil {
				// Drain any buffered reorder responses.
				drained := ex.Drain()
				for _, b := range drained {
					_, _ = agentOut.Write(append(b, '\n'))
				}
				return
			}
		}
	}()

	<-fwdDone
	// Forward done: close upstream's stdin so it terminates and the
	// reverse pump sees EOF.
	if upstreamOutCloser != nil {
		_ = upstreamOutCloser.Close()
	}
	<-revDone

	// Emit the fault schedule to stderr if AGENTCHAOS_DEBUG is set.
	if os.Getenv("AGENTCHAOS_DEBUG") != "" {
		for _, entry := range ex.Schedule() {
			fmt.Fprintf(os.Stderr, "[schedule] fault[%d] %s id=%d dir=%s\n",
				entry.FaultIndex, entry.Action, entry.MsgID, entry.Direction)
		}
	}

	return exitCode
}

func readFile(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
		os.Exit(1)
	}
	return b
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// writeEventLog serialises log to path as NDJSON. It is a no-op when path
// is empty or log is nil. Failures are reported on stderr but do not abort
// the run — exporting the event log is a side feature.
func writeEventLog(path string, log *event.Log, seed int64) {
	if path == "" || log == nil {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[run] --event-log: create %s: %v\n", path, err)
		return
	}
	defer f.Close()
	if err := log.WriteNDJSON(f); err != nil {
		fmt.Fprintf(os.Stderr, "[run] --event-log: write %s: %v\n", path, err)
		return
	}
	fmt.Fprintf(os.Stderr, "[run] --event-log: wrote seed %d events to %s\n", seed, path)
}
