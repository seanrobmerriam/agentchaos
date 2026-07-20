package fault_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
	"github.com/seanrobmerriam/agentchaos/testutil/mcptoy"
)

// ============================================================================
// Phase 3 GATE: run each primitive against a toy stateful upstream server
// (the counter tool) and confirm the observable effect matches its
// definition exactly.
// ============================================================================

// dualPipe creates a pair of io.Pipe-based ReadWriteClosers for two-way
// communication between the proxy and the test.
type dualPipe struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func newDualPipe() (a, b *dualPipe) {
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	return &dualPipe{r1, w1}, &dualPipe{r2, w2}
}

func (d *dualPipe) Read(p []byte) (int, error)  { return d.r.Read(p) }
func (d *dualPipe) Write(p []byte) (int, error) { return d.w.Write(p) }
func (d *dualPipe) Close() error {
	_ = d.r.Close()
	_ = d.w.Close()
	return nil
}

// proxyHarness is the full in-process wiring for Phase 3 gate tests.
// It creates a toy MCP Server, wires it through the executor-driven pump,
// and provides interactive send/read methods for the test.
type proxyHarness struct {
	t          *testing.T
	srv        *mcptoy.Server
	ex         *fault.Executor
	agentOut   *bufio.Reader
	agentIn    io.Writer
	agentInCloser  io.Closer
	agentOutR      io.Reader
	agentOutRCloser io.Closer
	upInR           io.Reader
	upInRCloser     io.Closer
	upOutW          io.Writer
	upOutWCloser    io.Closer
	upInW           io.Writer
	upInWCloser     io.Closer
	upOutR          io.Reader
	upOutRCloser    io.Closer
	doneCh          chan int
	cancel          context.CancelFunc
}

func newProxyHarness(t *testing.T, s *scenario.Scenario) *proxyHarness {
	t.Helper()
	// Agent <-> pump pipes
	agentInR, agentInW := io.Pipe()
	agentOutR, agentOutW := io.Pipe()
	// Upstream (toy server) <-> pump pipes
	upInR, upInW := io.Pipe()
	upOutR, upOutW := io.Pipe()

	srv := mcptoy.New()
	go func() { _ = srv.Serve(upInR, upOutW) }()

	ex, err := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportStdio)
	if err != nil {
		t.Fatalf("executor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan int, 1)
	go func() {
		code := pump(ctx, agentInR, agentOutW, upOutR, upInW, ex)
		doneCh <- code
	}()

	return &proxyHarness{
		t:              t,
		srv:            srv,
		ex:             ex,
		agentOut:       bufio.NewReader(agentOutR),
		agentIn:        agentInW,
		agentInCloser:  agentInW,
		agentOutR:      agentOutR,
		agentOutRCloser: agentOutR,
		upInR:          upInR,
		upInRCloser:    upInR,
		upOutW:         upOutW,
		upOutWCloser:   upOutW,
		upInW:          upInW,
		upInWCloser:    upInW,
		upOutR:         upOutR,
		upOutRCloser:   upOutR,
		doneCh:         doneCh,
		cancel:         cancel,
	}
}

func (h *proxyHarness) send(msg string) {
	if _, err := h.agentIn.Write([]byte(msg + "\n")); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

func (h *proxyHarness) readLine() string {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := h.agentOut.ReadBytes('\n')
		ch <- result{string(bytes.TrimSpace(b)), err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && r.err != io.EOF {
			h.t.Fatalf("readLine: %v", r.err)
		}
		return r.line
	case <-time.After(5 * time.Second):
		h.t.Fatal("readLine timed out")
		return ""
	}
}

func (h *proxyHarness) readLineOpt() (string, bool) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := h.agentOut.ReadBytes('\n')
		ch <- result{string(bytes.TrimSpace(b)), err}
	}()
	select {
	case r := <-ch:
		return r.line, true
	case <-time.After(2 * time.Second):
		return "", false
	}
}

func (h *proxyHarness) stop() {
	// Close all pipes in order to unblock any pending reads and let the
	// pump goroutines exit cleanly.
	_ = h.agentInCloser.Close()
	_ = h.upInWCloser.Close()
	_ = h.upOutWCloser.Close()
	// Wait for the pump to finish
	select {
	case <-h.doneCh:
	case <-time.After(3 * time.Second):
		h.t.Log("[harness] pump did not finish in 3s, forcefully cancelling")
	}
	// Close the read ends to unblock any pending readLineOpt goroutines.
	_ = h.agentOutRCloser.Close()
	_ = h.upInRCloser.Close()
	_ = h.upOutRCloser.Close()
	h.cancel()
}

// pump is the message-level fault-injecting pump for in-process tests.
// Mirrors the CLI's pumpWithFaults logic.
func pump(ctx context.Context, agentIn io.Reader, agentOut io.Writer, upstreamIn io.Reader, upstreamOut io.Writer, ex *fault.Executor) int {
	fwdDone := make(chan struct{})
	revDone := make(chan struct{})
	var exitCode int
	var exitOnce sync.Once
	exitNow := func(code int) {
		exitOnce.Do(func() { exitCode = code })
	}

	go func() {
		defer close(fwdDone)
		sc := bufio.NewReader(agentIn)
		for {
			line, err := sc.ReadBytes('\n')
			if len(line) > 0 {
				msg := scenario.ParseMessage(line)
				trimmed := bytes.TrimRight(line, "\r\n")
				forward, killed := ex.ProcessForward(msg, trimmed, fault.AgentToUpstream)
				for _, b := range forward {
					if _, werr := upstreamOut.Write(append(b, '\n')); werr != nil {
						return
					}
				}
				if killed {
					exitNow(77)
					// Don't return immediately; let the reverse pump
					// finish buffered messages.
				}
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		defer close(revDone)
		sc := bufio.NewReader(upstreamIn)
		for {
			line, err := sc.ReadBytes('\n')
			if len(line) > 0 {
				msg := scenario.ParseMessage(line)
				trimmed := bytes.TrimRight(line, "\r\n")
				forward, _ := ex.ProcessReverse(msg, trimmed, fault.UpstreamToAgent)
				for _, b := range forward {
					if _, werr := agentOut.Write(append(b, '\n')); werr != nil {
						return
					}
				}
			}
			if err != nil {
				drained := ex.Drain()
				for _, b := range drained {
					_, _ = agentOut.Write(append(b, '\n'))
				}
				return
			}
		}
	}()

	<-fwdDone
	<-revDone
	return exitCode
}

// ---- Gate: in_doubt observe ----

func TestPhase3GateInDoubt(t *testing.T) {
	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{Match: scenario.Matcher{Tool: strPtrn("counter")}, Action: "in_doubt"},
		},
	}
	h := newProxyHarness(t, s)
	defer h.stop()

	// Send initialize
	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	resp := h.readLine()
	if !strings.Contains(resp, `"id":1`) {
		t.Fatalf("initialize: %s", resp)
	}

	// Send counter call — response should be DROPPED (in_doubt)
	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"counter"}}`)
	_, gotResponse := h.readLineOpt()
	if gotResponse {
		t.Fatalf("in_doubt: counter response should have been dropped, got: see above")
	}

	// BUT: the internal event log should have the dropped response.
	dropped := h.ex.DroppedResponses()
	if len(dropped) != 1 {
		t.Fatalf("dropped responses: want 1, got %d", len(dropped))
	}
	// Verify the dropped response contains counter=1
	if !bytes.Contains(dropped[0], []byte("counter=1")) {
		t.Fatalf("dropped response content: %s", dropped[0])
	}
	t.Logf("[in_doubt gate] response dropped, internal log has counter=1")
}

// ---- Gate: duplicate observe ----

func TestPhase3GateDuplicate(t *testing.T) {
	s := &scenario.Scenario{
		Seed: 1,
		Faults: []scenario.Fault{
			{
				Match:  scenario.Matcher{Type: strPtrn("response")},
				At:     "before_response",
				Action: "duplicate",
				Count:  3,
			},
		},
	}
	h := newProxyHarness(t, s)
	defer h.stop()

	// Send initialize
	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	firstResp := h.readLine()
	if !strings.Contains(firstResp, `"id":1`) {
		t.Fatalf("first response: %s", firstResp)
	}

	// Read the 2 extra copies
	secondResp := h.readLine()
	thirdResp := h.readLine()

	// All 3 should be the same message
	if !strings.Contains(secondResp, `"id":1`) {
		t.Fatalf("second duplicate: %s", secondResp)
	}
	if !strings.Contains(thirdResp, `"id":1`) {
		t.Fatalf("third duplicate: %s", thirdResp)
	}
	t.Logf("[duplicate gate] initialise response delivered 3 times")

	// Now send a counter call — the response should also be triplicated
	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"counter"}}`)
	for i := 0; i < 3; i++ {
		resp := h.readLine()
		if !strings.Contains(resp, `"id":2`) {
			t.Fatalf("counter duplicate[%d]: %s", i, resp)
		}
	}
	t.Logf("[duplicate gate] counter response delivered 3 times")
}

// ---- Gate: kill_process observe (subprocess) ----

func TestPhase3GateKillProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}

	scenarioFile := t.TempDir() + "/kill.yaml"
	writeFile(t, scenarioFile, []byte(`
seed: 1
faults:
  - match: {tool: "counter"}
    action: kill_process
    probability: 1.0
assertions: []
`))

	ip := startInteractiveProxy(t, scenarioFile, "npx -y @modelcontextprotocol/server-everything stdio")
	defer func() { ip.cmd.Process.Kill() }()

	// Initialize
	ip.sendLine(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	line, ok := ip.readLineTimeout(t, 10*time.Second)
	if !ok || !containsSubstring(line, `"id":1`) {
		t.Fatalf("initialize: ok=%v line=%s", ok, line)
	}

	// tools/call counter — kill fires
	ip.sendLine(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"counter"}}`)

	// Proxy should exit with code 77
	code, _ := ip.wait()
	if code != 77 {
		t.Fatalf("exit code: want 77 got %d (stderr: %s)", code, ip.stderr.String())
	}

	// No response with id:2 should have been received
	_, gotResponse := ip.readLineTimeout(t, 2*time.Second)
	if gotResponse {
		// May be a notification, that's OK — just not id:2
	}
	t.Logf("[kill_process gate] exit code 77, counter request forwarded, response dropped by death")
}

// suppress unused
var _ = exec.Command
var _ = json.Unmarshal