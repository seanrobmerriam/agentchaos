// Package fuzz implements property-based scenario fuzzing for AgentChaos.
// It generates random fault schedules derived from a base scenario (using
// the base's matchers and assertions as a template), runs each generated
// scenario against an upstream command, collapses failures into equivalence
// classes (by reason substring), and surfaces one minimal reproducer per
// class via [shrink.Shrink].
package fuzz

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/assert"
	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
	"github.com/seanrobmerriam/agentchaos/internal/shrink"
)

// fuzzActions is the set of actions Generate can choose from. corrupt_checkpoint
// is excluded because it requires a valid filesystem path unavailable at
// scenario-generation time.
var fuzzActions = []string{"kill_process", "duplicate", "reorder", "in_doubt"}

// Generate returns a scenario whose faults are randomly drawn using seed.
// Up to maxFaults fault rules are generated; each uses an empty matcher
// (matches all messages). The base's Seed, Assertions, and Extends/Include
// fields are preserved verbatim.
func Generate(base *scenario.Scenario, maxFaults int, seed uint64) *scenario.Scenario {
	r := rand.New(rand.NewSource(int64(seed))) //nolint:gosec // fuzz-quality rng is fine
	n := 1 + r.Intn(maxFaults)

	faults := make([]scenario.Fault, 0, n)
	for i := 0; i < n; i++ {
		action := fuzzActions[r.Intn(len(fuzzActions))]
		f := scenario.Fault{
			Action: action,
			Match:  scenario.Matcher{},
		}
		p := r.Float64()
		f.Probability = &p
		switch action {
		case "duplicate":
			f.Count = 1 + r.Intn(3) // 1..3 extra copies
		case "reorder":
			f.Window = 2 + r.Intn(4) // window 2..5
		}
		faults = append(faults, f)
	}

	s := *base
	s.Seed = int64(seed)
	s.Faults = faults
	return &s
}

// Options configures a fuzz run.
type Options struct {
	ScenarioPath  string        // base scenario (assertions reused from it)
	Upstream      string        // upstream command
	Runs          int           // total generated scenarios to execute (>=1)
	MaxFaults     int           // upper bound on faults per generated scenario
	Timeout       time.Duration // per-run timeout
	ShrinkFails   bool          // shrink each unique failure class to a minimal reproducer
	MaxShrinkIter int           // max shrink iterations per failure class (0 → 200)
}

// FailureClass groups failures that share the same normalised reason.
type FailureClass struct {
	Reason     string
	Count      int
	ExitCode   int
	Reproducer *scenario.Scenario // minimal reproducer (if ShrinkFails=true)
	OriginalN  int
	FinalN     int
	Iterations int
}

// Report summarises a fuzz run.
type Report struct {
	Runs          int
	RunsFailed    int
	Classes       []*FailureClass
	UniqueClasses int
	Timing        time.Duration
}

// Run executes Runs generated scenarios and returns an aggregated Report.
func Run(opts Options) *Report {
	start := time.Now()
	if opts.Runs < 1 {
		opts.Runs = 1
	}
	if opts.MaxFaults < 1 {
		opts.MaxFaults = 8
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxShrinkIter <= 0 {
		opts.MaxShrinkIter = 200
	}

	var base *scenario.Scenario
	if opts.ScenarioPath != "" {
		b, err := os.ReadFile(opts.ScenarioPath)
		if err == nil {
			base, _ = scenario.Parse(b)
		}
	}
	if base == nil {
		base = &scenario.Scenario{}
	}

	// classByReason maps normalised reason → failure class.
	classByReason := make(map[string]*FailureClass)
	// classScenarios holds the last failing scenario for each class (for shrink).
	classScenario := make(map[string]*scenario.Scenario)

	report := &Report{Runs: opts.Runs}

	for i := 0; i < opts.Runs; i++ {
		seed := uint64(time.Now().UnixNano()) ^ uint64(i*2654435761)
		s := Generate(base, opts.MaxFaults, seed)
		r := runOne(s, opts.Upstream, opts.Timeout)
		if r.failed {
			report.RunsFailed++
			key := normaliseReason(r.reason)
			fc, exists := classByReason[key]
			if !exists {
				fc = &FailureClass{Reason: r.reason, ExitCode: r.exitCode}
				classByReason[key] = fc
				report.Classes = append(report.Classes, fc)
				classScenario[key] = s
			}
			fc.Count++
		}
	}

	report.UniqueClasses = len(report.Classes)

	// Optionally shrink one reproducer per class.
	if opts.ShrinkFails {
		for key, fc := range classByReason {
			s := classScenario[key]
			fc.OriginalN = len(s.Faults)
			res, err := shrink.Shrink(s, func(cand *scenario.Scenario) bool {
				r := runOne(cand, opts.Upstream, opts.Timeout)
				return r.failed && normaliseReason(r.reason) == key
			}, shrink.Options{MaxIterations: opts.MaxShrinkIter})
			if err == nil && res != nil {
				fc.Reproducer = res.Scenario
				fc.FinalN = res.FinalN
				fc.Iterations = res.Iterations
			} else {
				fc.Reproducer = s
				fc.FinalN = fc.OriginalN
			}
		}
	}

	report.Timing = time.Since(start)
	return report
}

type oneResult struct {
	failed   bool
	exitCode int
	reason   string
}

// runOne executes a single generated scenario against the upstream and returns
// a pass/fail outcome. It mirrors the structure of internal/risk.runOneSeed.
func runOne(s *scenario.Scenario, upstream string, timeout time.Duration) oneResult {
	ex, err := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)
	if err != nil {
		return oneResult{failed: true, exitCode: 78, reason: fmt.Sprintf("executor: %v", err)}
	}

	parts := strings.Fields(upstream)
	if len(parts) == 0 {
		return oneResult{failed: true, exitCode: 1, reason: "no upstream command"}
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stderr = io.Discard

	upIn, err := cmd.StdinPipe()
	if err != nil {
		return oneResult{failed: true, exitCode: 1, reason: fmt.Sprintf("stdin: %v", err)}
	}
	upOut, err := cmd.StdoutPipe()
	if err != nil {
		return oneResult{failed: true, exitCode: 1, reason: fmt.Sprintf("stdout: %v", err)}
	}
	if err := cmd.Start(); err != nil {
		return oneResult{failed: true, exitCode: 1, reason: fmt.Sprintf("start: %v", err)}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan int, 1)
	go func() {
		done <- pumpSimple(ctx, io.Discard, upOut, upIn)
	}()

	pumpDone := false
	var pumpCode int
	select {
	case pumpCode = <-done:
		pumpDone = true
	case <-ctx.Done():
	}
	if !pumpDone {
		cancel()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		pumpCode = <-done
	}

	if len(s.Assertions) > 0 {
		results := assert.CheckAll(s.Assertions, ex.EventLog())
		if assert.AnyFailed(results) {
			var reasons []string
			for i, r := range results {
				if r.Failed {
					reasons = append(reasons, s.Assertions[i].Type+": "+r.Reason)
				}
			}
			return oneResult{failed: true, exitCode: 70, reason: strings.Join(reasons, "; ")}
		}
	}

	if !pumpDone {
		return oneResult{failed: true, exitCode: 75, reason: "timeout"}
	}
	if pumpCode != 0 && pumpCode != 77 {
		return oneResult{failed: true, exitCode: pumpCode, reason: fmt.Sprintf("pump exit %d", pumpCode)}
	}
	return oneResult{}
}

// pumpSimple is a minimal pass-through pump (no fault injection) used for
// fuzz runs where the fault executor's impact is captured via the event log.
func pumpSimple(ctx context.Context, stdout io.Writer, upOut io.Reader, upIn io.WriteCloser) int {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upIn, strings.NewReader(""))
		_ = upIn.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(stdout, upOut)
		done <- struct{}{}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-ctx.Done():
			return 75
		}
	}
	return 0
}

// normaliseReason collapses a reason string to a short key for class grouping.
// Two reasons map to the same key if they share the same first 48 characters
// after stripping numeric IDs and addresses.
func normaliseReason(reason string) string {
	// Strip numeric suffixes (e.g. "seed 42" → "seed ") so variations in
	// numeric fields don't create spurious classes.
	b := []byte(reason)
	out := make([]byte, 0, len(b))
	inNum := false
	for _, c := range b {
		if c >= '0' && c <= '9' {
			inNum = true
			continue
		}
		if inNum {
			out = append(out, '#')
			inNum = false
		}
		out = append(out, c)
	}
	if inNum {
		out = append(out, '#')
	}
	s := string(out)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
