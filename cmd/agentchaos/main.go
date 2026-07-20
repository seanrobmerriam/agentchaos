// Command agentchaos is the CLI entry point for the AgentChaos fault proxy.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/seanrobmerriam/agentchaos/internal/assert"
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
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `agentchaos — fault injection proxy for MCP workflows

Usage:
  agentchaos run      --scenario <path> [--upstream <cmd>] [--seeds N] [--shrink-on-failure]
  agentchaos replay   --seed <uint64> --scenario <path> [--upstream <cmd>]
  agentchaos validate --scenario <path>
  agentchaos inspect  --scenario <path>`)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	upstreamCmd := fs.String("upstream", "", "upstream command (e.g. 'npx -y @modelcontextprotocol/server-everything stdio')")
	seeds := fs.Int("seeds", 1, "number of seeds to try")
	shrinkOnFailure := fs.Bool("shrink-on-failure", false, "shrink the fault schedule on failure")
	reproducerPath := fs.String("reproducer", "", "path to write minimal reproducer scenario on failure")
	fs.Parse(args)

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "run: --scenario is required")
		os.Exit(1)
	}

	baseScenario, err := scenario.Parse(readFile(*scenarioPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scenario error: %v\n", err)
		os.Exit(78)
	}

	for seed := int64(0); seed < int64(*seeds); seed++ {
		s := *baseScenario
		if *seeds > 1 {
			s.Seed = baseScenario.Seed + seed
		}

		// Run the scenario + assertions.
		result := runWithAssertions(&s, *upstreamCmd)

		if result.passed && result.exitCode == 0 {
			continue // no failure found; try next seed
		}

		// Failure found.
		failedSeed := s.Seed
		fmt.Fprintf(os.Stderr, "[run] seed %d triggered failure: %s\n",
			failedSeed, result.reason)

		if *shrinkOnFailure {
			fmt.Fprintln(os.Stderr, "[shrink] shrinking fault schedule...")
			shrunk, err := shrink.Shrink(&s, func(cand *scenario.Scenario) bool {
				// Predicate: does this reduced scenario still fail
				// assertions with the same seed?
				r := runWithAssertions(cand, *upstreamCmd)
				return !r.passed
			}, shrink.Options{MaxIterations: 200})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[shrink] error: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "[shrink] %d → %d faults\n",
					len(s.Faults), len(shrunk.Faults))
				if *reproducerPath != "" {
					out, _ := scenario.Marshal(shrunk)
					if werr := os.WriteFile(*reproducerPath, out, 0644); werr != nil {
						fmt.Fprintf(os.Stderr, "[shrink] write reproducer: %v\n", werr)
					} else {
						fmt.Fprintf(os.Stderr, "[shrink] reproducer written to %s\n", *reproducerPath)
					}
				}
			}
		}

		os.Exit(result.exitCode)
	}

	// All seeds passed.
	fmt.Fprintf(os.Stderr, "[run] all %d seeds passed\n", *seeds)
	os.Exit(0)
}

// runResult captures the outcome of a single scenario run.
type runResult struct {
	passed   bool
	exitCode int
	reason   string
}

// runWithAssertions runs the scenario, collects the event log, and checks
// assertions. Returns whether assertions passed and additional context.
func runWithAssertions(s *scenario.Scenario, upstreamCmd string) runResult {
	// Run the proxy and collect events.
	ex, err := fault.NewExecutorForTransport(s, fault.ExitProcess, fault.TransportStdio)
	if err != nil {
		return runResult{passed: false, exitCode: 78, reason: fmt.Sprintf("executor: %v", err)}
	}

	parts := strings.Fields(upstreamCmd)
	if len(parts) == 0 {
		return runResult{passed: false, exitCode: 1, reason: "no upstream command"}
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stderr = os.Stderr
	upIn, err := cmd.StdinPipe()
	if err != nil {
		return runResult{passed: false, exitCode: 1, reason: fmt.Sprintf("stdin: %v", err)}
	}
	upOut, err := cmd.StdoutPipe()
	if err != nil {
		return runResult{passed: false, exitCode: 1, reason: fmt.Sprintf("stdout: %v", err)}
	}
	if err := cmd.Start(); err != nil {
		return runResult{passed: false, exitCode: 1, reason: fmt.Sprintf("start: %v", err)}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the pump in a goroutine with a timeout.
	doneCh := make(chan int, 1)
	go func() {
		code := pumpWithFaults(ctx, os.Stdin, os.Stdout, upOut, upIn, upIn, ex)
		doneCh <- code
	}()

	// Wait for pump to finish or timeout.
	pumpDone := false
	select {
	case code := <-doneCh:
		_ = code
		pumpDone = true
	case <-ctx.Done():
	}

	if !pumpDone {
		cancel()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		<-doneCh
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
				reason:   strings.Join(reasons, "; "),
			}
		}
	}

	return runResult{passed: true, exitCode: 0}
}

func cmdReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	upstreamCmd := fs.String("upstream", "", "upstream command")
	seed := fs.Int64("seed", 0, "seed to replay")
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
	s.Seed = *seed
	os.Exit(runOnce(s, *upstreamCmd))
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

func cmdInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	fs.Parse(args)

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "inspect: --scenario is required")
		os.Exit(1)
	}

	s, err := scenario.Parse(readFile(*scenarioPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(78)
	}

	fmt.Printf("seed: %d\n", s.Seed)
	for i, f := range s.Faults {
		fmt.Printf("fault[%d]: %s %s\n", i, f.Match, f.Action)
	}
	for i, a := range s.Assertions {
		fmt.Printf("assertion[%d]: %s\n", i, a.Type)
	}
}

// runOnce starts the proxy with a scenario, piping agent-side stdin/stdout
// through the fault executor to the upstream subprocess. Returns the exit
// code.
func runOnce(s *scenario.Scenario, upstreamCmd string) int {
	// Build the executor.
	ex, err := fault.NewExecutorForTransport(s, fault.ExitProcess, fault.TransportStdio)
	if err != nil {
		fmt.Fprintf(os.Stderr, "executor error: %v\n", err)
		return 78
	}

	// Start the upstream subprocess.
	parts := strings.Fields(upstreamCmd)
	if len(parts) == 0 {
		fmt.Fprintln(os.Stderr, "no upstream command specified")
		return 1
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stderr = os.Stderr

	upIn, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "upstream stdin pipe: %v\n", err)
		return 1
	}
	upOut, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "upstream stdout pipe: %v\n", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "upstream start: %v\n", err)
		return 1
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Wire the proxy: agent stdin/stdout <-> upstream stdin/stdout.
	// We intercept at the message level using the fault executor.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() {
		code := pumpWithFaults(ctx, os.Stdin, os.Stdout, upOut, upIn, upIn, ex)
		done <- code
	}()

	select {
	case code := <-done:
		return code
	case <-ctx.Done():
		return 1
	}
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
				forward, killed := ex.ProcessForward(msg, trimmed, fault.AgentToUpstream)
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
				forward, _ := ex.ProcessReverse(msg, trimmed, fault.UpstreamToAgent)
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
