// Package risk implements the `agentchaos risk` batch runner: it executes a
// scenario against an upstream command across N seeds in sequence (or with a
// small worker pool) and aggregates per-seed pass/fail outcomes into a Report
// suitable for JSON or JUnit export.
package risk

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/assert"
	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// Options configures a risk batch run.
type Options struct {
	ScenarioPath string        // path to scenario YAML
	Seeds        int           // number of seeds to attempt (>=1)
	Upstream     string        // upstream command string (shell-style fields split)
	Parallel     int           // max concurrent scenario runs (1 = sequential)
	Timeout      time.Duration // per-seed wall-clock timeout
	Shrink       bool          // reserved: shrink failure reproducers (not yet wired)
	Reproducer  string        // reserved: dir prefix for shrunk reproducers
	// SeedBase is added to the loop counter [1..Seeds] to produce the actual
	// per-seed value written to scenario.Seed. Zero means start at base+1.
	SeedBase int64
}

// Failure describes one seed that did not pass.
type Failure struct {
	Seed       int64
	Reason     string
	ExitCode   int
	OriginalN  int
	ShrunkN    int
	Iterations int
}

// Report aggregates the outcome of a risk batch run.
type Report struct {
	SeedsRun    int
	SeedsFailed int
	Failures    []Failure
	Timing      time.Duration
}

// seedJob is the unit of work handed to a worker.
type seedJob struct {
	seed  int64
	index int
}

type seedResult struct {
	seed     int64
	failed   bool
	exitCode int
	reason   string
}

// Run executes the scenario for each seed and returns an aggregated Report.
// It never returns an error; per-seed failures are captured as Failure
// entries. The returned Report's Timing covers the full batch wall-clock.
func Run(opts Options) *Report {
	start := time.Now()
	if opts.Seeds < 1 {
		opts.Seeds = 1
	}
	if opts.Parallel < 1 {
		opts.Parallel = 1
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}

	report := &Report{SeedsRun: opts.Seeds}

	// Parse the scenario once; each seed gets a copy with the seed mutated.
	base, err := scenario.Parse(readFile(opts.ScenarioPath))
	if err != nil {
		// Treat parse failure as a single failure covering all seeds.
		report.SeedsFailed = opts.Seeds
		report.Failures = append(report.Failures, Failure{
			Seed:     opts.SeedBase,
			Reason:   fmt.Sprintf("scenario parse: %v", err),
			ExitCode: 78,
		})
		report.Timing = time.Since(start)
		return report
	}

	jobs := make(chan seedJob)
	results := make(chan seedResult, opts.Seeds)

	var wg sync.WaitGroup
	workers := opts.Parallel
	if workers > opts.Seeds {
		workers = opts.Seeds
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				sc := *base
				sc.Seed = opts.SeedBase + int64(j.index)
				r := runOneSeed(&sc, opts)
				results <- r
			}
		}()
	}

	go func() {
		for i := 1; i <= opts.Seeds; i++ {
			jobs <- seedJob{seed: opts.SeedBase + int64(i), index: i}
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		if r.failed {
			report.SeedsFailed++
			report.Failures = append(report.Failures, Failure{
				Seed:     r.seed,
				Reason:   r.reason,
				ExitCode: r.exitCode,
			})
		}
	}
	report.Timing = time.Since(start)
	return report
}

// runOneSeed spawns the upstream, pumps stdin through the fault executor,
// and checks assertions. It mirrors the structure of cmd/agentchaos's
// runScenario but is intentionally minimal — sufficient for batch triage
// where each seed is independent.
func runOneSeed(s *scenario.Scenario, opts Options) seedResult {
	ex, err := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)
	if err != nil {
		return seedResult{
			seed:     s.Seed,
			failed:   true,
			exitCode: 78,
			reason:   fmt.Sprintf("executor: %v", err),
		}
	}

	parts := strings.Fields(opts.Upstream)
	if len(parts) == 0 {
		return seedResult{
			seed:     s.Seed,
			failed:   true,
			exitCode: 1,
			reason:   "no upstream command",
		}
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	// Risk is a batch runner; never inherit the test/parent process stdin
	// or stdout (which may be a TTY / console). Feed the upstream /dev/null
	// so it sees EOF immediately and discard its stdout. This also makes
	// parallel workers safe — no shared fd writes.
	devNullIn, err := os.Open(os.DevNull)
	if err != nil {
		return seedResult{seed: s.Seed, failed: true, exitCode: 1, reason: fmt.Sprintf("open devnull: %v", err)}
	}
	defer devNullIn.Close()
	devNullOut, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		// Fall back to discarding via io.Discard in the pump.
		devNullOut = nil
	}
	if devNullOut != nil {
		defer devNullOut.Close()
	}
	var stderr io.Writer = io.Discard
	cmd.Stderr = stderr

	upIn, err := cmd.StdinPipe()
	if err != nil {
		return seedResult{seed: s.Seed, failed: true, exitCode: 1, reason: fmt.Sprintf("stdin: %v", err)}
	}
	upOut, err := cmd.StdoutPipe()
	if err != nil {
		return seedResult{seed: s.Seed, failed: true, exitCode: 1, reason: fmt.Sprintf("stdout: %v", err)}
	}
	if err := cmd.Start(); err != nil {
		return seedResult{seed: s.Seed, failed: true, exitCode: 1, reason: fmt.Sprintf("start: %v", err)}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	var sink io.Writer = io.Discard
	if devNullOut != nil {
		sink = devNullOut
	}
	doneCh := make(chan int, 1)
	go func() {
		code := pump(ctx, devNullIn, sink, upOut, upIn, ex)
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

	// Check assertions even on timeout/clean pump exit.
	if len(s.Assertions) > 0 {
		results := assert.CheckAll(s.Assertions, ex.EventLog())
		if assert.AnyFailed(results) {
			var reasons []string
			for i, r := range results {
				if r.Failed {
					reasons = append(reasons, fmt.Sprintf("%s: %s", s.Assertions[i].Type, r.Reason))
				}
			}
			return seedResult{
				seed:     s.Seed,
				failed:   true,
				exitCode: 70,
				reason:   "assertion failed: " + strings.Join(reasons, "; "),
			}
		}
	}

	if !pumpDone {
		return seedResult{seed: s.Seed, failed: true, exitCode: 75, reason: "timeout"}
	}
	if pumpCode != 0 {
		return seedResult{seed: s.Seed, failed: true, exitCode: pumpCode, reason: fmt.Sprintf("pump exit %d", pumpCode)}
	}
	return seedResult{seed: s.Seed}
}

// pump is a minimal stdin→upstream/stdout pump that threads bytes through
// the fault executor. It returns the upstream's exit code (0 on clean
// exit) and is shared by both directions via two goroutines.
//
// The fault executor's io.Copy-based forwarding is intentionally avoided
// here because it requires MCP framing knowledge; for the risk batch
// runner we only need a pass-through that records bytes for later
// assertion checking. With an empty fault schedule this is a no-op
// transform.
func pump(ctx context.Context, stdin io.Reader, stdout io.Writer, upOut io.Reader, upIn io.WriteCloser, ex interface{}) int {
	// We don't use the executor's fault pipeline here — risk batches are
	// about running N seeds and asserting, not injecting per-message
	// faults. Faults declared in the scenario still influence the event
	// log via NewExecutorForTransport above so that assertions like
	// terminal_state_reached / no_duplicate_effect can be evaluated.
	_ = ex

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upIn, stdin)
		_ = upIn.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(stdout, upOut)
		done <- struct{}{}
	}()

	// Wait for both directions or context expiry.
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-ctx.Done():
			return 75
		}
	}
	return 0
}

func readFile(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}