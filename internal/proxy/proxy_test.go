package proxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/proxy"

	"pgregory.net/rapid"
)

// ============================================================================
// Test helpers (shared by example tests and property tests)
// ============================================================================

// jsonRound serialises v compact-form and fails the test on marshal error.
func jsonRound[T any](t interface{ Fatalf(string, ...any) }, v T) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// genMessage generates a single valid JSON-RPC 2.0 message, drawn from a
// rapid.T. Returns the literal JSON bytes.
func genMessage(t *rapid.T) []byte {
	kind := rapid.SampledFrom([]string{"request", "response", "notification"}).Draw(t, "kind")
	id := rapid.IntRange(0, 1_000_000).Draw(t, "id")
	method := rapid.StringMatching(`[a-zA-Z_][a-zA-Z0-9_]*`).Draw(t, "method")

	switch kind {
	case "request":
		hasParams := rapid.Bool().Draw(t, "has_params")
		msg := map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  method,
		}
		if hasParams {
			msg["params"] = map[string]any{
				"name":  rapid.StringMatching(`[a-zA-Z_][a-zA-Z0-9_]*`).Draw(t, "p_name"),
				"value": rapid.IntRange(0, 100).Draw(t, "p_value"),
			}
		}
		return jsonRound(t, msg)
	case "response":
		isErr := rapid.Bool().Draw(t, "is_error")
		if isErr {
			return jsonRound(t, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error": map[string]any{
					"code":    rapid.IntRange(-32000, -1).Draw(t, "err_code"),
					"message": rapid.StringMatching(`[a-z_]{1,20}`).Draw(t, "err_msg"),
				},
			})
		}
		return jsonRound(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"ok":    rapid.Bool().Draw(t, "res_ok"),
				"value": rapid.IntRange(0, 1000).Draw(t, "res_value"),
			},
		})
	default: // notification
		return jsonRound(t, map[string]any{
			"jsonrpc": "2.0",
			"method":  method,
		})
	}
}

// genMessageSequence generates a newline-delimited sequence of 1..64 JSON-RPC
// messages. Length is bounded so a single property iteration is fast.
func genMessageSequence(t *rapid.T) []byte {
	n := rapid.IntRange(1, 64).Draw(t, "count")
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.Write(genMessage(t))
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// pipeQuartet is the four io ends a Proxy needs, plus the windows the test
// uses to send and receive on each side.
type pipeQuartet struct {
	// forward: agent -> proxy -> upstream
	agentInW io.ReadWriteCloser // test writes here (proxy reads)
	upOutR   io.ReadWriteCloser // test reads here (proxy writes)
	// reverse: upstream -> proxy -> agent
	upInW     io.ReadWriteCloser // test writes here (proxy reads)
	agentOutR io.ReadWriteCloser // test reads here (proxy writes)
}

// oneWay returns a synchronous io.Pipe pair split into a reader-end and a
// writer-end, both as ReadWriteCloser (the unused half returns a benign error
// if ever invoked; tests never invoke the unused half).
func oneWay() (readEnd, writeEnd io.ReadWriteCloser) {
	r, w := io.Pipe()
	rEnd := &readerSide{r: r, c: r}
	wEnd := &writerSide{w: w, c: w}
	return rEnd, wEnd
}

type readerSide struct {
	r io.Reader
	c io.Closer
}

func (s *readerSide) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *readerSide) Write(p []byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (s *readerSide) Close() error                { return s.c.Close() }

type writerSide struct {
	w io.Writer
	c io.Closer
}

func (s *writerSide) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *writerSide) Read(p []byte) (int, error)  { return 0, io.EOF }
func (s *writerSide) Close() error                { return s.c.Close() }

// setupProxy builds a Proxy over four io ends and starts it in a goroutine.
// The returned cancel func frees proxy goroutines after the test asserts.
func setupProxy(t interface{ Helper() }) (*proxy.Proxy, context.CancelFunc, *pipeQuartet) {
	agentInR, agentInW := oneWay()   // proxy reads agentInR, agent writes agentInW
	upOutR, upOutW := oneWay()       // proxy writes upOutW, upstream reads upOutR
	upInR, upInW := oneWay()         // proxy reads upInR, upstream writes upInW
	agentOutR, agentOutW := oneWay() // proxy writes agentOutW, agent reads agentOutR

	p := proxy.New(agentInR, agentOutW, upInR, upOutW)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = p.Run(ctx) }()
	return p, cancel, &pipeQuartet{
		agentInW:  agentInW,
		upOutR:    upOutR,
		upInW:     upInW,
		agentOutR: agentOutR,
	}
}

// pipeForward sends input bytes agent->upstream and returns what the upstream
// side received. After collecting output, it closes the reverse-direction
// pipes so the proxy can finish both directions.
func pipeForward(t interface{ Fatalf(string, ...any) }, qt *pipeQuartet, input []byte) []byte {
	doneCh := make(chan struct{})
	var errW error
	go func() {
		_, errW = qt.agentInW.Write(input)
		_ = qt.agentInW.Close()
		close(doneCh)
	}()
	got, err := io.ReadAll(qt.upOutR)
	if err != nil {
		t.Fatalf("read upstream side: %v", err)
	}
	select {
	case <-doneCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("forward write timed out")
	}
	if errW != nil {
		t.Fatalf("write agent side: %v", errW)
	}
	// Idle reverse side: close it so proxy goroutines exit cleanly.
	_ = qt.upInW.Close()
	_ = qt.agentOutR.Close()
	return got
}

// pipeReverse sends input bytes upstream->agent and returns what the agent
// side received.
func pipeReverse(t interface{ Fatalf(string, ...any) }, qt *pipeQuartet, input []byte) []byte {
	doneCh := make(chan struct{})
	var errW error
	go func() {
		_, errW = qt.upInW.Write(input)
		_ = qt.upInW.Close()
		close(doneCh)
	}()
	got, err := io.ReadAll(qt.agentOutR)
	if err != nil {
		t.Fatalf("read agent side: %v", err)
	}
	select {
	case <-doneCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("reverse write timed out")
	}
	if errW != nil {
		t.Fatalf("write upstream side: %v", errW)
	}
	_ = qt.agentInW.Close()
	_ = qt.upOutR.Close()
	return got
}

// pipeBidirectional concurrently sends both directions, returning whatever
// each side received.
func pipeBidirectional(t interface{ Errorf(string, ...any) }, qt *pipeQuartet, fwd, rev []byte) (fwdGot, revGot []byte) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		got, err := io.ReadAll(qt.upOutR)
		if err != nil {
			t.Errorf("read upstream side: %v", err)
		}
		fwdGot = got
	}()
	go func() {
		defer wg.Done()
		got, err := io.ReadAll(qt.agentOutR)
		if err != nil {
			t.Errorf("read agent side: %v", err)
		}
		revGot = got
	}()

	var wgw sync.WaitGroup
	wgw.Add(2)
	go func() {
		defer wgw.Done()
		if _, err := qt.agentInW.Write(fwd); err != nil {
			t.Errorf("write agent side: %v", err)
		}
		_ = qt.agentInW.Close()
	}()
	go func() {
		defer wgw.Done()
		if _, err := qt.upInW.Write(rev); err != nil {
			t.Errorf("write upstream side: %v", err)
		}
		_ = qt.upInW.Close()
	}()

	wg.Wait()
	wgw.Wait()
	return
}

// ============================================================================
// Example tests
// ============================================================================

// Example: a single request is forwarded unchanged agent->upstream.
func TestForwardSingleExample(t *testing.T) {
	_, cancel, qt := setupProxy(t)
	defer cancel()
	in := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	got := pipeForward(t, qt, in)
	if !bytes.Equal(got, in) {
		t.Fatalf("got=%q want=%q", got, in)
	}
}

// Example: a single response is forwarded unchanged upstream->agent.
func TestReverseSingleExample(t *testing.T) {
	_, cancel, qt := setupProxy(t)
	defer cancel()
	in := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n")
	got := pipeReverse(t, qt, in)
	if !bytes.Equal(got, in) {
		t.Fatalf("got=%q want=%q", got, in)
	}
}

// ============================================================================
// Property tests (rapid)
// ============================================================================

// Property: arbitrary JSON-RPC sequences pass through agent->upstream byte-identical.
func TestPassthroughForwardProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := genMessageSequence(rt)
		_, cancel, qt := setupProxy(rt)
		got := pipeForward(rt, qt, input)
		if !bytes.Equal(got, input) {
			rt.Fatalf("forward mismatch:\n input=%q\n got   =%q", input, got)
		}
		_ = cancel
	})
}

// Property: arbitrary JSON-RPC sequences pass through upstream->agent byte-identical.
func TestPassthroughReverseProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := genMessageSequence(rt)
		_, cancel, qt := setupProxy(rt)
		got := pipeReverse(rt, qt, input)
		if !bytes.Equal(got, input) {
			rt.Fatalf("reverse mismatch:\n input=%q\n got   =%q", input, got)
		}
		_ = cancel
	})
}

// Property: bidirectional flows simultaneously preserve both byte streams.
func TestPassthroughBidirectionalProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		fwd := genMessageSequence(rt)
		rev := genMessageSequence(rt)
		_, cancel, qt := setupProxy(rt)
		fwdGot, revGot := pipeBidirectional(rt, qt, fwd, rev)
		_ = cancel
		if !bytes.Equal(fwdGot, fwd) {
			rt.Fatalf("fwd mismatch:\n input=%q\n got   =%q", fwd, fwdGot)
		}
		if !bytes.Equal(revGot, rev) {
			rt.Fatalf("rev mismatch:\n input=%q\n got   =%q", rev, revGot)
		}
	})
}
