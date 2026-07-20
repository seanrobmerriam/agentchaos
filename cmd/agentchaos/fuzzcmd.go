package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/fuzz"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

func cmdFuzz(args []string) {
	fs := flag.NewFlagSet("fuzz", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "base scenario YAML (assertions are reused from it)")
	upstream := fs.String("upstream", "", "upstream command")
	runs := fs.Int("runs", 200, "number of generated scenarios to execute")
	maxFaults := fs.Int("max-faults", 8, "max number of faults per generated scenario")
	timeout := fs.Duration("timeout", 30*time.Second, "per-run timeout")
	shrinkFails := fs.Bool("shrink-on-failure", false, "shrink each unique failure class to a minimal reproducer")
	shrinkMaxIter := fs.Int("shrink-max-iter", 200, "max shrink iterations per failure class")
	reportPath := fs.String("report", "", "write a JSON report to this path")
	fs.Parse(args)

	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "fuzz: --upstream is required")
		os.Exit(1)
	}

	opts := fuzz.Options{
		ScenarioPath:  *scenarioPath,
		Upstream:      *upstream,
		Runs:          *runs,
		MaxFaults:     *maxFaults,
		Timeout:       *timeout,
		ShrinkFails:   *shrinkFails,
		MaxShrinkIter: *shrinkMaxIter,
	}

	fmt.Fprintf(os.Stderr, "[fuzz] running %d scenarios (max-faults=%d)...\n", *runs, *maxFaults)
	r := fuzz.Run(opts)
	fmt.Fprintf(os.Stderr, "[fuzz] %d/%d failed, %d unique failure class(es) in %s\n",
		r.RunsFailed, r.Runs, r.UniqueClasses, r.Timing.Round(time.Millisecond))

	for i, fc := range r.Classes {
		fmt.Fprintf(os.Stderr, "[fuzz] class %d: %q (%d occurrences, exit %d)\n",
			i+1, fc.Reason, fc.Count, fc.ExitCode)
		if fc.Reproducer != nil && fc.FinalN < fc.OriginalN {
			fmt.Fprintf(os.Stderr, "[fuzz]   shrunk %d → %d faults in %d iterations\n",
				fc.OriginalN, fc.FinalN, fc.Iterations)
		}
		if fc.Reproducer != nil && *shrinkFails {
			out, err := scenario.Marshal(fc.Reproducer)
			if err == nil {
				fmt.Fprintf(os.Stderr, "[fuzz]   reproducer:\n%s\n", string(out))
			}
		}
	}

	if *reportPath != "" {
		type jsonClass struct {
			Reason     string `json:"reason"`
			Count      int    `json:"count"`
			ExitCode   int    `json:"exit_code"`
			OriginalN  int    `json:"original_faults,omitempty"`
			FinalN     int    `json:"final_faults,omitempty"`
			Iterations int    `json:"iterations,omitempty"`
		}
		type jsonReport struct {
			Runs          int         `json:"runs"`
			RunsFailed    int         `json:"runs_failed"`
			UniqueClasses int         `json:"unique_classes"`
			Classes       []jsonClass `json:"classes"`
			TimingMS      int64       `json:"timing_ms"`
		}
		rep := jsonReport{
			Runs:          r.Runs,
			RunsFailed:    r.RunsFailed,
			UniqueClasses: r.UniqueClasses,
			TimingMS:      r.Timing.Milliseconds(),
		}
		for _, fc := range r.Classes {
			rep.Classes = append(rep.Classes, jsonClass{
				Reason:     fc.Reason,
				Count:      fc.Count,
				ExitCode:   fc.ExitCode,
				OriginalN:  fc.OriginalN,
				FinalN:     fc.FinalN,
				Iterations: fc.Iterations,
			})
		}
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*reportPath, append(b, '\n'), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "fuzz: write report: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[fuzz] report written to %s\n", *reportPath)
		}
	}

	if r.UniqueClasses > 0 {
		os.Exit(70)
	}
}
