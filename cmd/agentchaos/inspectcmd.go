package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// cmdInspect implements the `agentchaos inspect` subcommand.
//
// Modes:
//   - Default: pretty-print the scenario (seed, faults, assertions).
//   - --dry-run --messages <path>: read a newline-delimited JSON-RPC
//     trace, replay each message through the executor (without a live
//     upstream), and print the resulting fault schedule. This lets the
//     user preview which faults would fire on a given trace before
//     actually running it.
func cmdInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	dryRun := fs.Bool("dry-run", false, "preview fault schedule from a recorded message trace")
	messagesPath := fs.String("messages", "", "path to a newline-delimited JSON-RPC trace (for --dry-run)")
	showResolved := fs.Bool("resolved", false, "show the post-composition scenario (after extends/include)")
	fs.Parse(args)

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "inspect: --scenario is required")
		os.Exit(1)
	}

	var s *scenario.Scenario
	var err error
	if *showResolved {
		s, err = scenario.Load(*scenarioPath)
	} else {
		s, err = scenario.Parse(readFile(*scenarioPath))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(78)
	}

	if *dryRun {
		if *messagesPath == "" {
			fmt.Fprintln(os.Stderr, "--dry-run requires --messages")
			os.Exit(1)
		}
		runDryRun(s, *messagesPath)
		return
	}

	fmt.Printf("seed: %d\n\n", s.Seed)
	for i, f := range s.Faults {
		fmt.Printf("fault[%d]\n", i)
		fmt.Printf("  match:  %s\n", f.Match.String())
		if f.At != "" {
			fmt.Printf("  at:     %s\n", f.At)
		}
		fmt.Printf("  action: %s\n", f.Action)
		if f.Probability != nil {
			fmt.Printf("  probability: %g\n", *f.Probability)
		}
		if f.Action == "duplicate" {
			fmt.Printf("  count:   %d\n", f.Count)
		}
		if f.Action == "reorder" {
			fmt.Printf("  window:  %d\n", f.Window)
		}
		if f.Action == "corrupt_checkpoint" {
			fmt.Printf("  path:    %s\n", f.Path)
			fmt.Printf("  offset:  %d\n", f.Offset)
			fmt.Printf("  bytes:   %d\n", f.Bytes)
		}
		fmt.Println()
	}
	for i, a := range s.Assertions {
		fmt.Printf("assertion[%d] type=%s", i, a.Type)
		if a.Key != "" {
			fmt.Printf(" key=%s", a.Key)
		}
		if a.WithinRetries > 0 {
			fmt.Printf(" within_retries=%d", a.WithinRetries)
		}
		if a.Tool != "" {
			fmt.Printf(" tool=%s", a.Tool)
		}
		fmt.Println()
	}
}

// runDryRun replays a recorded JSON-RPC trace through the executor
// (with a no-op exit callback) and prints the resulting fault schedule.
// Each trace entry feeds ProcessForward (request/notification) or
// ProcessReverse (response) depending on the message kind. Stdio mode
// is enforced because this CLI never speaks HTTP.
func runDryRun(s *scenario.Scenario, messagesPath string) {
	ex, err := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)
	if err != nil {
		fmt.Fprintf(os.Stderr, "executor: %v\n", err)
		os.Exit(78)
	}

	trace, err := loadTrace(messagesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load trace: %v\n", err)
		os.Exit(78)
	}

	var forward, reverse int
	for _, e := range trace {
		if e.Dir == fault.UpstreamToAgent {
			ex.ProcessReverse(e.Msg, e.Raw, e.Dir)
			reverse++
		} else {
			ex.ProcessForward(e.Msg, e.Raw, e.Dir)
			forward++
		}
	}

	for _, entry := range ex.Schedule() {
		fmt.Printf("schedule fault[%d] action=%s id=%d dir=%s\n",
			entry.FaultIndex, entry.Action, entry.MsgID, entry.Direction)
	}
	fmt.Printf("# forward=%d reverse=%d\n", forward, reverse)
}

// traceEntry is one parsed line of the recorded JSON-RPC trace.
type traceEntry struct {
	Msg scenario.Message
	Raw []byte
	Dir fault.Direction
}

// loadTrace reads a newline-delimited JSON-RPC trace file and decodes
// each line into a scenario.Message + raw bytes + direction. Direction
// is inferred from the message kind: request/notification →
// AgentToUpstream, response → UpstreamToAgent. Unknown or
// unparseable entries default to forward.
func loadTrace(path string) ([]traceEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []traceEntry
	sc := bufio.NewScanner(f)
	// Allow long lines (MCP responses can be large).
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		// Trim trailing newline/CR for the parser; keep raw bytes as
		// originally written (the executor tolerates both).
		trimmed := trimTrailingNewline(append([]byte(nil), line...))
		if len(trimmed) == 0 {
			continue
		}
		msg := scenario.ParseMessage(trimmed)
		dir := fault.AgentToUpstream
		switch msg.Kind {
		case "response":
			dir = fault.UpstreamToAgent
		case "request", "notification":
			dir = fault.AgentToUpstream
		default:
			dir = fault.AgentToUpstream
		}
		out = append(out, traceEntry{Msg: msg, Raw: trimmed, Dir: dir})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
