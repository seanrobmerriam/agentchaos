package fault_test

import (
	"testing"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"

	"pgregory.net/rapid"
)

// ============================================================================
// Phase 4 TDD: Determinism & seeding
//
// Property-test that two runs with the same seed and scenario produce
// byte-identical fault schedules, and a different seed produces a different
// schedule with overwhelming probability.
// ============================================================================

// FaultSchedule is a recorded list of which faults fired during a run.
// Exposed by the Executor after processing a message sequence.
// Each entry records: fault index, action, message id, direction.

// makeScenario returns a scenario with probabilistic faults.
func makeScenario(seed int64) *scenario.Scenario {
	p := 0.5
	return &scenario.Scenario{
		Seed: seed,
		Faults: []scenario.Fault{
			{
				Match:       scenario.Matcher{Tool: strPtrn("counter")},
				Action:      "in_doubt",
				Probability: &p,
			},
			{
				Match:  scenario.Matcher{Type: strPtrn("response")},
				At:     "before_response",
				Action: "duplicate",
				Count:  2,
			},
			{
				Match:  scenario.Matcher{Type: strPtrn("response")},
				At:     "before_response",
				Action: "reorder",
				Window: 3,
			},
		},
	}
}

// runSequence processes a fixed message sequence through the executor and
// returns the fault schedule (which faults fired, in order).
func runSequence(s *scenario.Scenario) []fault.ScheduleEntry {
	// Use HTTP transport to allow reorder (which is rejected in stdio).
	ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportHTTP)

	// Fixed message sequence — same every time so only the seed varies.
	// 20 counter request/response pairs = 20 probability rolls at p=0.5,
	// making the chance of two different seeds producing identical
	// schedules ~ (0.5)^20 ≈ 1-in-a-million.
	var messages []struct {
		msg  scenario.Message
		raw  []byte
		dir  fault.Direction
	}
	for i := int64(1); i <= 20; i++ {
		messages = append(messages, struct {
			msg  scenario.Message
			raw  []byte
			dir  fault.Direction
		}{
			scenario.Message{Kind: "request", Method: "tools/call", Tool: "counter", ID: i},
			[]byte(`{"jsonrpc":"2.0","id":` + itoa(int(i)) + `,"method":"tools/call","params":{"name":"counter"}}`),
			fault.AgentToUpstream,
		})
		messages = append(messages, struct {
			msg  scenario.Message
			raw  []byte
			dir  fault.Direction
		}{
			scenario.Message{Kind: "response", ID: i},
			[]byte(`{"jsonrpc":"2.0","id":` + itoa(int(i)) + `,"result":{"counter":` + itoa(int(i)) + `}}`),
			fault.UpstreamToAgent,
		})
	}

	for _, m := range messages {
		switch m.dir {
		case fault.AgentToUpstream:
			ex.ProcessForward(m.msg, m.raw, m.dir)
		case fault.UpstreamToAgent:
			ex.ProcessReverse(m.msg, m.raw, m.dir)
		}
	}
	// Drain any reorder buffer
	ex.Drain()

	return ex.Schedule()
}

// ---- Property: same seed → identical schedule ----

func TestDeterminismSameSeedProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		seed := rapid.Int64Range(1, 1<<40).Draw(rt, "seed")
		s1 := makeScenario(seed)
		s2 := makeScenario(seed)

		sched1 := runSequence(s1)
		sched2 := runSequence(s2)

		if len(sched1) != len(sched2) {
			rt.Fatalf("schedule lengths differ: %d vs %d", len(sched1), len(sched2))
		}
		for i, a := range sched1 {
			b := sched2[i]
			if a.FaultIndex != b.FaultIndex ||
				a.Action != b.Action ||
				a.MsgID != b.MsgID ||
				a.Direction != b.Direction {
				rt.Fatalf("schedule[%d] differs:\n %+v\n %+v", i, a, b)
			}
		}
	})
}

// ---- Property: different seed → different schedule ----

func TestDeterminismDifferentSeedProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate two distinct seeds.
		s1_ := rapid.Int64Range(1, 1<<40).Draw(rt, "seed1")
		offset := rapid.Int64Range(1, 1<<40).Draw(rt, "offset")
		s2_ := s1_ + offset // guarantee different

		s1 := makeScenario(s1_)
		s2 := makeScenario(s2_)

		sched1 := runSequence(s1)
		sched2 := runSequence(s2)

		// They must differ with overwhelming probability. The only way
		// they could be identical is if the probability rolls happen to
		// land the same way (probability (0.5)^k where k is the number of
		// probabilistic decisions). With at least 3 in_doubt decisions
		// at p=0.5, the chance of identical schedules is < 12.5%.
		// Over 100 rapid cases, the chance of a false positive is
		// astronomically small.
		identical := true
		if len(sched1) != len(sched2) {
			identical = false
		} else {
			for i := range sched1 {
				if sched1[i].FaultIndex != sched2[i].FaultIndex ||
					sched1[i].Action != sched2[i].Action ||
					sched1[i].MsgID != sched2[i].MsgID ||
					sched1[i].Direction != sched2[i].Direction {
					identical = false
					break
				}
			}
		}
		if identical {
			rt.Fatalf("different seeds (%d vs %d) produced identical schedules — probability rolls landed identically", s1_, s2_)
		}
	})
}

// ---- Example: explicit seed reproduces exact schedule ----

func TestDeterminismReproducibleExample(t *testing.T) {
	seed := int64(4891)
	s1 := makeScenario(seed)
	s2 := makeScenario(seed)

	sched1 := runSequence(s1)
	sched2 := runSequence(s2)

	if len(sched1) == 0 {
		t.Fatal("expected non-empty schedule")
	}
	if len(sched1) != len(sched2) {
		t.Fatalf("schedule lengths: %d vs %d", len(sched1), len(sched2))
	}
	for i := range sched1 {
		if sched1[i] != sched2[i] {
			t.Fatalf("schedule[%d]: %+v vs %+v", i, sched1[i], sched2[i])
		}
	}
	t.Logf("[determinism] seed=%d schedule: %d entries, reproducible", seed, len(sched1))
}

// ---- Property: reorder permutation is deterministic per seed ----

func TestReorderPermutationDeterministicProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		seed := rapid.Int64Range(1, 1<<40).Draw(rt, "seed")

		// Run the same reorder scenario twice and verify the permutation
		// output is identical.
		run := func() []int {
			s := &scenario.Scenario{
				Seed: seed,
				Faults: []scenario.Fault{
					{
						Match:  scenario.Matcher{Type: strPtrn("response")},
						At:     "before_response",
						Action: "reorder",
						Window: 3,
					},
				},
			}
			// Use HTTP transport to allow reorder
			ex, _ := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportHTTP)

			// Send 3 requests + 3 responses.
			for i := 1; i <= 3; i++ {
				reqMsg := scenario.Message{Kind: "request", Method: "tools/call", Tool: "echo", ID: int64(i)}
				reqRaw := []byte(`{"jsonrpc":"2.0","id":` + itoa(i) + `,"method":"tools/call","params":{"name":"echo"}}`)
				ex.ProcessForward(reqMsg, reqRaw, fault.AgentToUpstream)
			}
			var released []int
			for i := 1; i <= 3; i++ {
				respMsg := scenario.Message{Kind: "response", ID: int64(i)}
				respRaw := []byte(`{"jsonrpc":"2.0","id":` + itoa(i) + `,"result":{}}`)
				fwd, _ := ex.ProcessReverse(respMsg, respRaw, fault.UpstreamToAgent)
				for _, b := range fwd {
					released = append(released, extractID(b))
				}
			}
			return released
		}

		r1 := run()
		r2 := run()
		if len(r1) != len(r2) {
			rt.Fatalf("reorder lengths: %d vs %d", len(r1), len(r2))
		}
		for i := range r1 {
			if r1[i] != r2[i] {
				rt.Fatalf("reorder[%d]: %d vs %d (seed=%d)", i, r1[i], r2[i], seed)
			}
		}
	})
}

// itoa is a tiny strconv.Itoa replacement to avoid importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// extractID pulls the "id" field from a JSON byte slice.
func extractID(b []byte) int {
	var m map[string]any
	_ = unmarshalJSON(b, &m)
	if v, ok := m["id"].(float64); ok {
		return int(v)
	}
	return -1
}

// unmarshalJSON is a thin wrapper to avoid importing json in the test file.
func unmarshalJSON(b []byte, v any) error {
	return jsonUnmarshal(b, v)
}