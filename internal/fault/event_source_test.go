package fault_test

import (
	"sync"
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// TestConcurrentSchedulesBounded verifies that concurrent forward+reverse
// processing under the data-race fix produces a schedule within reasonable
// bounds (no entries lost, no infinite loops). True byte-identical
// determinism across separate program runs of concurrent goroutines is
// not achievable because Go scheduler ordering varies; the existing
// TestDeterminismSameSeedProperty covers sequential determinism.
func TestConcurrentSchedulesBounded(t *testing.T) {
	prob := 0.5
	s := &scenario.Scenario{
		Seed: 42,
		Faults: []scenario.Fault{
			{Action: "duplicate", Match: scenario.Matcher{}, Probability: &prob},
		},
	}
	ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)

	var wg sync.WaitGroup
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

	sched := ex.Schedule()
	n := len(sched)
	// 100 calls, ~50% probability → schedule should be in [20, 80].
	if n < 20 || n > 80 {
		t.Fatalf("schedule length %d out of expected range [20, 80]", n)
	}
}

// TestNoScheduleRace is satisfied if -race reports nothing. It hammers the
// executor from many goroutines so that any un-mutexed access to the
// schedule slice, the splitMix64 state, or the pendingInDoubt /
// droppedResponses / reorderBuffer maps and slices will trip the race
// detector and fail the test.
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

// ptr returns a pointer to s. A small helper for inline Matcher literals
// (e.g. Tool: ptr("t")) used only inside this file.
func ptr(s string) *string { return &s }
