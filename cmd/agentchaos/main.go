package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
