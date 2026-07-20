package fault

import (
	"fmt"
	"math"
	"os"
	"sync"

	"github.com/seanrobmerriam/agentchaos/internal/event"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// ExitFunc is the function called when kill_process fires. Defaults to os.Exit.
type ExitFunc func(int)

// Transport describes the transport mode.
type Transport int

const (
	TransportStdio Transport = iota
	TransportHTTP
)

// ScheduleEntry is one record in the fault schedule: a record of which
// fault fired during a run. The schedule is deterministic given the same
// seed and scenario.
type ScheduleEntry struct {
	FaultIndex int       // index into scenario.Faults
	Action     string    // the action that fired
	MsgID      int64     // the message id (0 for notifications)
	Direction  Direction // which direction the message was travelling
}

// Executor is the Phase 3 execution layer. It takes a parsed scenario and
// actually performs fault actions on messages flowing through the proxy.
// Unlike the Phase 2 Pipeline (which only logs matches), the Executor
// modifies, drops, duplicates, or terminates on matched messages.
type Executor struct {
	scenario  *scenario.Scenario
	exitFn    ExitFunc
	transport Transport
	prng      *splitMix64

	mu sync.Mutex
	// pendingInDoubt maps request id → true for requests whose responses
	// should be dropped (in_doubt fault matched on the request).
	pendingInDoubt map[int64]bool

	// reorderBuffer holds responses awaiting permutation release.
	reorderBuffer [][]byte
	reorderWindow int

	// droppedResponses stores in_doubt-dropped responses for the oracle.
	droppedResponses [][]byte

	// schedule is the recorded fault schedule for this run.
	schedule []ScheduleEntry

	// eventLog records all events during the run for assertion checking.
	eventLog *event.Log
}

// NewExecutor creates an executor for stdio transport (default).
func NewExecutor(s *scenario.Scenario, exitFn ExitFunc) *Executor {
	return &Executor{
		scenario:       s,
		exitFn:         exitFn,
		transport:      TransportStdio,
		prng:           newSplitMix64(uint64(s.Seed)),
		pendingInDoubt: make(map[int64]bool),
		eventLog:       event.New(),
	}
}

// NewExecutorForTransport creates an executor for a specific transport.
// Returns an error if a scenario uses reorder in stdio mode.
func NewExecutorForTransport(s *scenario.Scenario, exitFn ExitFunc, transport Transport) (*Executor, error) {
	ex := &Executor{
		scenario:       s,
		exitFn:         exitFn,
		transport:      transport,
		prng:           newSplitMix64(uint64(s.Seed)),
		pendingInDoubt: make(map[int64]bool),
		eventLog:       event.New(),
	}
	if transport == TransportStdio {
		for i, f := range s.Faults {
			if f.Action == "reorder" {
				return nil, fmt.Errorf("fault[%d]: reorder is not supported in stdio mode (transport is inherently ordered)", i)
			}
		}
	}
	return ex, nil
}

// ProcessForward handles a message going agent->upstream.
// Returns: messages to forward to upstream, and whether to kill the process.
func (ex *Executor) ProcessForward(msg scenario.Message, raw []byte, dir Direction) (forward [][]byte, kill bool) {
	anchor := ex.deriveForwardAnchor(msg, dir)

	// Record the request/notification in the event log.
	ex.recordForwardEvent(msg, dir)

	for i := range ex.scenario.Faults {
		f := &ex.scenario.Faults[i]
		if !ex.shouldFire(i, f, msg, dir, anchor) {
			continue
		}

		switch f.Action {
		case "kill_process":
			// Forward the request, then kill.
			forward = append(forward, raw)
			// Call the exit function (defaults to os.Exit; tests inject a
			// recording function). The return signals kill to the caller.
			if ex.exitFn != nil {
				ex.exitFn(77)
			}
			return forward, true

		case "in_doubt":
			// Mark this request id for response dropping.
			ex.mu.Lock()
			ex.pendingInDoubt[msg.ID] = true
			ex.mu.Unlock()
			// Forward the request normally.
			forward = append(forward, raw)

		case "duplicate":
			count := f.DefaultCount()
			for c := 0; c < count; c++ {
				forward = append(forward, raw)
			}

		case "corrupt_checkpoint":
			// Flip bytes in the target file at the specified offset.
			// Errors are non-fatal (the file might not exist yet).
			if f.Path != "" && f.Bytes > 0 {
				_ = corruptFile(f.Path, f.Offset, f.Bytes)
			}
			forward = append(forward, raw)

		case "reorder":
			// Reorder is for responses, not requests.
			forward = append(forward, raw)

		default:
			forward = append(forward, raw)
		}
	}

	// If no fault added to forward, pass through unchanged.
	if len(forward) == 0 {
		forward = append(forward, raw)
	}

	return forward, false
}

// ProcessReverse handles a message going upstream->agent.
// Returns: messages to forward to the agent (may be buffered, dropped, or
// duplicated), and whether to kill the process.
func (ex *Executor) ProcessReverse(msg scenario.Message, raw []byte, dir Direction) (forward [][]byte, kill bool) {
	anchor := ex.deriveReverseAnchor(msg, dir)

	// Check in_doubt FIRST: if this response's id is pending, drop it.
	ex.mu.Lock()
	if msg.Kind == "response" && ex.pendingInDoubt[msg.ID] {
		delete(ex.pendingInDoubt, msg.ID)
		ex.mu.Unlock()
		// Record the dropped response for the oracle.
		ex.mu.Lock()
		ex.droppedResponses = append(ex.droppedResponses, raw)
		ex.mu.Unlock()
		// Record event: response was dropped (oracle sees it, agent doesn't).
		ex.eventLog.Record(event.Event{
			Kind:      event.KindResponseDropped,
			MsgID:     msg.ID,
			Direction: string(dir),
			Raw:       raw,
		})
		return nil, false // dropped
	}
	ex.mu.Unlock()

	for i := range ex.scenario.Faults {
		f := &ex.scenario.Faults[i]
		if !ex.shouldFire(i, f, msg, dir, anchor) {
			continue
		}

		switch f.Action {
		case "duplicate":
			count := f.DefaultCount()
			out := make([][]byte, count)
			for c := 0; c < count; c++ {
				out[c] = raw
			}
			ex.recordReverseDelivered(msg, dir, count)
			return out, false

		case "reorder":
			fwd, killed := ex.handleReorder(f, raw)
			if len(fwd) > 0 {
				ex.recordReverseDelivered(msg, dir, len(fwd))
			}
			return fwd, killed

		case "in_doubt", "kill_process":
			// These fire on the forward side; on reverse they're no-ops.

		case "corrupt_checkpoint":
			// Out-of-band.
		}
	}

	// Default: passthrough — record 1 delivery.
	ex.recordReverseDelivered(msg, dir, 1)
	return [][]byte{raw}, false
}

// Drain flushes any buffered messages (e.g. reorder buffer not yet full).
func (ex *Executor) Drain() [][]byte {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	out := ex.reorderBuffer
	ex.reorderBuffer = nil
	return out
}

// DroppedResponses returns internally-logged in_doubt-dropped responses.
func (ex *Executor) DroppedResponses() [][]byte {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	out := make([][]byte, len(ex.droppedResponses))
	copy(out, ex.droppedResponses)
	return out
}

// Schedule returns the deterministic fault schedule recorded during this
// run. Two runs with the same seed and scenario produce identical schedules.
func (ex *Executor) Schedule() []ScheduleEntry {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	out := make([]ScheduleEntry, len(ex.schedule))
	copy(out, ex.schedule)
	return out
}

// Seed returns the PRNG seed for this executor.
func (ex *Executor) Seed() uint64 {
	return uint64(ex.scenario.Seed)
}

// EventLog returns the event log recorded during this run.
func (ex *Executor) EventLog() *event.Log {
	return ex.eventLog
}

// shouldFire checks if a fault should fire for a given message+direction+anchor,
// including the probability roll. The faultIndex is recorded in the schedule
// when the fault fires.
func (ex *Executor) shouldFire(faultIndex int, f *scenario.Fault, msg scenario.Message, dir Direction, anchor Anchor) bool {
	if !f.Match.Matches(msg) {
		return false
	}

	// Temporal anchor check.
	if f.At != "" && f.At != string(anchor) {
		return false
	}
	if f.At == "" {
		def := defaultAnchor(f.Action)
		if def != "" && def != anchor {
			return false
		}
	}

	// Probability roll.
	p := f.DefaultProbability()
	if p < 1.0 {
		roll := ex.prng.Float64()
		if roll >= p {
			return false
		}
	}

	// Record that this fault fired in the deterministic schedule.
	ex.schedule = append(ex.schedule, ScheduleEntry{
		FaultIndex: faultIndex,
		Action:     f.Action,
		MsgID:      msg.ID,
		Direction:  dir,
	})

	return true
}

// handleReorder buffers responses until the window is full, then releases
// them in a permuted order. Returns (released messages, killed).
func (ex *Executor) handleReorder(f *scenario.Fault, raw []byte) ([][]byte, bool) {
	ex.mu.Lock()
	defer ex.mu.Unlock()

	window := f.Window
	if window <= 0 {
		window = 3
	}

	ex.reorderBuffer = append(ex.reorderBuffer, raw)
	ex.reorderWindow = window

	if len(ex.reorderBuffer) >= ex.reorderWindow {
		// Permute the buffer using the PRNG.
		buf := ex.reorderBuffer
		ex.reorderBuffer = nil
		return permute(ex.prng, buf), false
	}

	return nil, false // buffered, not yet released
}

// deriveForwardAnchor determines the anchor for a forward-direction message.
func (ex *Executor) deriveForwardAnchor(msg scenario.Message, dir Direction) Anchor {
	switch msg.Kind {
	case "request":
		// Could be before_request_send or after_request_sent.
		// The executor doesn't know which phase of the send we're in;
		// the caller should specify. For v1, we use after_request_sent
		// as the default when ProcessForward is called (meaning the
		// message has been parsed and is about to be forwarded).
		// The proxy will call ProcessForward twice if needed.
		return AnchorAfterRequestSent
	case "notification":
		return AnchorAtNotification
	default:
		return AnchorAfterRequestSent
	}
}

// deriveReverseAnchor determines the anchor for a reverse-direction message.
func (ex *Executor) deriveReverseAnchor(msg scenario.Message, dir Direction) Anchor {
	switch msg.Kind {
	case "response":
		return AnchorBeforeResponse
	case "notification":
		return AnchorAtNotification
	default:
		return AnchorBeforeResponse
	}
}

// ============================================================================
// SplitMix64 PRNG — deterministic, platform-independent
// ============================================================================

type splitMix64 struct {
	state uint64
}

func newSplitMix64(seed uint64) *splitMix64 {
	return &splitMix64{state: seed}
}

func (s *splitMix64) next() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Float64 returns a pseudo-random float64 in [0, 1).
func (s *splitMix64) Float64() float64 {
	// Use the top 53 bits for a double.
	v := s.next() >> 11
	return float64(v) / float64(1<<53)
}

// Intn returns a pseudo-random int in [0, n).
func (s *splitMix64) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(s.next() % uint64(n))
}

// permute shuffles a slice of byte slices using the Fisher-Yates algorithm
// with the PRNG, returning a new (possibly same-order) permutation.
func permute(prng *splitMix64, items [][]byte) [][]byte {
	out := make([][]byte, len(items))
	copy(out, items)
	for i := len(out) - 1; i > 0; i-- {
		j := prng.Intn(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ExitProcess is the default exit function.
var ExitProcess ExitFunc = func(code int) { os.Exit(code) }

// recordForwardEvent logs a request or notification being sent forward.
func (ex *Executor) recordForwardEvent(msg scenario.Message, dir Direction) {
	kind := event.KindRequestSent
	if msg.Kind == "notification" {
		kind = event.KindNotificationSent
	}
	ex.eventLog.Record(event.Event{
		Kind:      kind,
		MsgID:     msg.ID,
		Method:    msg.Method,
		Tool:      msg.Tool,
		Direction: string(dir),
	})
}

// recordReverseEvent logs a response or notification arriving from upstream.
// If the message is delivered to the agent, kindDelivered is used; if
// duplicated, multiple events are recorded.
func (ex *Executor) recordReverseDelivered(msg scenario.Message, dir Direction, count int) {
	for i := 0; i < count; i++ {
		kind := event.KindResponseDelivered
		if msg.Kind == "notification" {
			kind = event.KindNotificationDelivered
		}
		ex.eventLog.Record(event.Event{
			Kind:      kind,
			MsgID:     msg.ID,
			Method:    msg.Method,
			Tool:      msg.Tool,
			Direction: string(dir),
		})
	}
}

// corruptFile flips N bytes at the given offset in the file at path.
// Uses XOR with 0xFF to flip each byte, which is a simple corruption that
// changes the value without zeroing it. Errors are returned but the caller
// (the executor) treats them as non-fatal — the file might not exist.
func corruptFile(path string, offset int64, n int) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, n)
	_, err = f.ReadAt(buf, offset)
	if err != nil {
		return err
	}

	for i := range buf {
		buf[i] ^= 0xFF // flip all bits
	}

	_, err = f.WriteAt(buf, offset)
	return err
}

// _ keeps math import valid for Float64 if we add more math later.
var _ = math.Pi
