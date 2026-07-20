package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/risk"
)

// cmdRisk implements `agentchaos risk`: run a scenario across N seeds and
// emit a JSON and/or JUnit report. Designed for CI batch triage where you
// want a deterministic pass/fail aggregate across many random seeds.
func cmdRisk(args []string) {
	fs := flag.NewFlagSet("risk", flag.ExitOnError)
	scenarioPath := fs.String("scenario", "", "path to scenario YAML")
	upstream := fs.String("upstream", "", "upstream command (shell-style fields)")
	seeds := fs.Int("seeds", 100, "number of seeds to attempt")
	parallel := fs.Int("parallel", 1, "max concurrent scenario runs")
	reportPath := fs.String("report", "", "path to write JSON report")
	junitPath := fs.String("junit", "", "path to write JUnit XML report")
	timeout := fs.Duration("timeout", 60*time.Second, "per-seed wall-clock timeout")
	shrink := fs.Bool("shrink-on-failure", false, "shrink reproducers on failure (reserved)")
	fs.Parse(args)

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "risk: --scenario is required")
		os.Exit(2)
	}
	if *seeds < 1 {
		fmt.Fprintln(os.Stderr, "risk: --seeds must be >= 1")
		os.Exit(2)
	}

	opts := risk.Options{
		ScenarioPath: *scenarioPath,
		Seeds:        *seeds,
		Upstream:     *upstream,
		Parallel:     *parallel,
		Timeout:      *timeout,
		Shrink:       *shrink,
		SeedBase:     0,
	}

	rep := risk.Run(opts)

	if *reportPath != "" {
		if err := writeRiskJSON(*reportPath, rep); err != nil {
			fmt.Fprintf(os.Stderr, "risk: write json report: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "[risk] wrote JSON report to %s\n", *reportPath)
	}

	if *junitPath != "" {
		f, err := os.Create(*junitPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "risk: create junit: %v\n", err)
			os.Exit(1)
		}
		if err := risk.WriteJUnit(f, rep); err != nil {
			_ = f.Close()
			fmt.Fprintf(os.Stderr, "risk: write junit: %v\n", err)
			os.Exit(1)
		}
		_ = f.Close()
		fmt.Fprintf(os.Stderr, "[risk] wrote JUnit XML to %s\n", *junitPath)
	}

	fmt.Fprintf(os.Stderr, "[risk] seeds=%d failed=%d timing=%s\n",
		rep.SeedsRun, rep.SeedsFailed, rep.Timing)

	if rep.SeedsFailed > 0 {
		os.Exit(70)
	}
	os.Exit(0)
}

// writeRiskJSON marshals the report with indentation for human-readable CI
// artifacts.
func writeRiskJSON(path string, rep *risk.Report) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}