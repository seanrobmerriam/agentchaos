package proxy_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/proxy"
	"github.com/seanrobmerriam/agentchaos/testutil/mcptoy"
)

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// mcpAgentReader wraps a *bufio.Reader to expose ReadOneMessage which reads
// a single newline-delimited JSON-RPC message with a soft 5s deadline.
type mcpAgentReader struct {
	r *bufio.Reader
}

func newAgentReader(r io.Reader) *mcpAgentReader {
	return &mcpAgentReader{r: bufio.NewReader(r)}
}

func (a *mcpAgentReader) ReadOne(t *testing.T) map[string]any {
	t.Helper()
	type readResult struct {
		line []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		b, err := a.r.ReadBytes('\n')
		ch <- readResult{b, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil && res.err != io.EOF {
			t.Fatalf("read: %v", res.err)
		}
		var msg map[string]any
		if err := json.Unmarshal(res.line, &msg); err != nil {
			t.Fatalf("unmarshal agent output %q: %v", res.line, err)
		}
		return msg
	case <-time.After(5 * time.Second):
		t.Fatalf("ReadOne timed out")
		return nil
	}
}

func mustWriteR(t *testing.T, w io.Writer, msg map[string]any) {
	t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func idFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	default:
		return 0, false
	}
}

// -----------------------------------------------------------------------
// Stdio Integration Gate
// -----------------------------------------------------------------------

// TestStdioIntegrationGate exercises a full MCP handshake from an in-process
// client through the stdio Proxy to the in-process toy MCP server, verifying
// that initialize + tools/list + tools/call all behave correctly through the
// proxy.
func TestStdioIntegrationGate(t *testing.T) {
	// Wire io ends between agent <-> proxy <-> upstream (toy server).
	agentInR, agentInW := oneWayForEnds()
	agentOutR, agentOutW := oneWayForEnds()
	upstreamInR, upstreamInW := oneWayForEnds()
	upstreamOutR, upstreamOutW := oneWayForEnds()

	// Start the toy upstream server (reads from upstreamInR, writes to upstreamOutW).
	// Close its stdout when Serve returns so the proxy sees EOF on its
	// reverse-direction reader.
	go func() {
		_ = mcptoy.Serve(upstreamInR, upstreamOutW)
		if c, ok := upstreamOutW.(io.Closer); ok {
			_ = c.Close()
		}
	}()

	// Start the proxy.
	p := proxy.New(agentInR, agentOutW, upstreamOutR, upstreamInW)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	client := newAgentReader(agentOutR)

	// 1) initialize
	mustWriteR(t, agentInW, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": mcptoy.ProtocolVersion},
	})
	resp := client.ReadOne(t)
	if id, ok := idFloat64(resp["id"]); !ok || id != 1 {
		t.Fatalf("initialize id drifted: %v", resp["id"])
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil || result["protocolVersion"] != mcptoy.ProtocolVersion {
		t.Fatalf("initialize protocolVersion missing: %v", result)
	}

	// 2) notifications/initialized (server is silent; nothing to read)
	mustWriteR(t, agentInW, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})

	// 3) tools/list
	mustWriteR(t, agentInW, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	resp = client.ReadOne(t)
	if id, ok := idFloat64(resp["id"]); !ok || id != 2 {
		t.Fatalf("tools/list id drifted: %v", resp["id"])
	}
	result, _ = resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools/list result: %v", result)
	}

	// 4) tools/call echo
	mustWriteR(t, agentInW, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"text": "hello"},
		},
	})
	resp = client.ReadOne(t)
	if id, ok := idFloat64(resp["id"]); !ok || id != 3 {
		t.Fatalf("tools/call id drifted: %v", resp["id"])
	}
	result, _ = resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call echo result missing content: %v", result)
	}

	// 5) tools/call unknown tool
	mustWriteR(t, agentInW, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "nonexistent"},
	})
	resp = client.ReadOne(t)
	if id, ok := idFloat64(resp["id"]); !ok || id != 4 {
		t.Fatalf("tools/call id drifted: %v", resp["id"])
	}
	result, _ = resp["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("expected isError=true for unknown tool: %v", result)
	}

	// 6) Tear down: close agent side -> proxy EOF -> toy EOF.
	_ = agentInW.Close()
	_ = upstreamInW.Close()
}

// -----------------------------------------------------------------------
// HTTP Integration Gate
// -----------------------------------------------------------------------

// mcptoyHTTPServer adapts the toy MCP server to Streamable HTTP. Each POST is
// parsed as a JSON-RPC message and the toy server's response is relayed back
// as application/json. Mcptoy writes one response line per request it sees in
// the input stream; we funnel a single message through it.
type mcptoyHTTPServer struct {
	url    string
	server *httptest.Server
}

func newMcptoyHTTPServer(t *testing.T) *mcptoyHTTPServer {
	t.Helper()
	mux := http.NewServeMux()
	s := &mcptoyHTTPServer{}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg map[string]any
		_ = json.Unmarshal(body, &msg)
		// Feed the single line to mcptoy.Serve and capture its output.
		pr := bytes.NewReader(append(body, '\n'))
		var buf bytes.Buffer
		_ = mcptoy.Serve(pr, &buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	})
	srv := httptest.NewServer(mux)
	s.url = srv.URL
	s.server = srv
	return s
}

func (s *mcptoyHTTPServer) Close() { s.server.Close() }

// TestHTTPIntegrationGate exercises a full MCP handshake from an in-process
// client through the HTTP Proxy to the in-process toy MCP server exposed via
// Streamable HTTP upstream.
func TestHTTPIntegrationGate(t *testing.T) {
	srv := newMcptoyHTTPServer(t)
	defer srv.Close()

	agentInR, agentInW := oneWayForEnds()
	agentOutR, agentOutW := oneWayForEnds()

	p := proxy.NewHTTP(agentInR, agentOutW, srv.url, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	client := newAgentReader(agentOutR)

	// 1) initialize via POST/response.
	mustWriteR(t, agentInW, map[string]any{
		"jsonrpc": "2.0", "id": 100, "method": "initialize",
	})
	resp := client.ReadOne(t)
	if id, ok := idFloat64(resp["id"]); !ok || id != 100 {
		t.Fatalf("initialize id drifted: %v", resp["id"])
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil || result["protocolVersion"] != mcptoy.ProtocolVersion {
		t.Fatalf("initialize result missing protocolVersion: %v", result)
	}

	// 2) tools/call echo through proxy->HTTP.
	mustWriteR(t, agentInW, map[string]any{
		"jsonrpc": "2.0", "id": 101, "method": "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"text": strings.Repeat("a", 5)},
		},
	})
	resp = client.ReadOne(t)
	if id, ok := idFloat64(resp["id"]); !ok || id != 101 {
		t.Fatalf("tools/call id drifted: %v", resp["id"])
	}
	result, _ = resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call echo result missing content: %v", result)
	}

	// 3) tools/call unknown tool -> isError
	mustWriteR(t, agentInW, map[string]any{
		"jsonrpc": "2.0", "id": 102, "method": "tools/call",
		"params": map[string]any{"name": fmt.Sprintf("unknown_%d", 999)},
	})
	resp = client.ReadOne(t)
	if id, ok := idFloat64(resp["id"]); !ok || id != 102 {
		t.Fatalf("tools/call id drifted: %v", resp["id"])
	}
	result, _ = resp["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("expected isError=true: %v", result)
	}

	_ = agentInW.Close()
}

// -----------------------------------------------------------------------
// Direct-vs-proxy equality (the actual gate assertion)
// -----------------------------------------------------------------------

// TestStdioProxyIsNoopVsDirect replays an identical agent-side sequence both
// directly against the toy upstream and through the proxy, then asserts the
// bytes the agent receives are byte-identical in both runs. This proves the
// proxy is a transparent no-op for the full MCP flow.
func TestStdioProxyIsNoopVsDirect(t *testing.T) {
	seq := []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": mcptoy.ProtocolVersion}},
		{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{"name": "echo", "arguments": map[string]any{"text": "abc"}}},
		{"jsonrpc": "2.0", "id": 4, "method": "tools/call",
			"params": map[string]any{"name": "echo", "arguments": map[string]any{"text": "def"}}},
		{"jsonrpc": "2.0", "id": 5, "method": "tools/call",
			"params": map[string]any{"name": "unknown"}},
	}
	var sendBuf bytes.Buffer
	for _, m := range seq {
		b, _ := json.Marshal(m)
		sendBuf.Write(b)
		sendBuf.WriteByte('\n')
	}

	directOut := runStdioFlowDirect(sendBuf.Bytes())
	proxiedOut := runStdioFlowThroughProxy(sendBuf.Bytes())

	if !bytes.Equal(proxiedOut, directOut) {
		t.Fatalf("direct vs proxied agent-side output differs:\n"+
			" proxied (len=%d): %q\n"+
			" direct   (len=%d): %q",
			len(proxiedOut), proxiedOut,
			len(directOut), directOut)
	}
}

// runStdioFlowDirect connects an "agent" directly to the toy server and plays
// back the provided input bytes. Returns the bytes the agent reads back.
func runStdioFlowDirect(input []byte) []byte {
	// io.Pipe returns (reader, writer). Agent writes -> toy reads here.
	toyReadFromAgent, agentWriteToToy := io.Pipe()
	// Toy writes -> agent reads here.
	agentReadFromToy, toyW := io.Pipe()

	// Run the toy server: reads from toyReadFromAgent, writes to toyW.
	// Close toy's stdout after Serve returns so agent's io.ReadAll sees EOF.
	go func() {
		_ = mcptoy.Serve(toyReadFromAgent, toyW)
		_ = toyW.Close()
	}()

	// Feed agent-side input from a goroutine so the toy server can drain it.
	go func() {
		_, _ = agentWriteToToy.Write(input)
		_ = agentWriteToToy.Close()
	}()

	// Collect agent-side output.
	out, _ := io.ReadAll(agentReadFromToy)
	_ = toyW.Close()
	return out
}

// runStdioFlowThroughProxy is the same flow with the stdio Proxy wedged in
// between the agent and the toy upstream.
func runStdioFlowThroughProxy(input []byte) []byte {
	agentInR, agentInW := oneWayForEnds()
	agentOutR, agentOutW := oneWayForEnds()
	upstreamInR, upstreamInW := oneWayForEnds()
	upstreamOutR, upstreamOutW := oneWayForEnds()

	// Start toy upstream. Close its stdout on Serve return so the proxy
	// observes EOF on its reverse-direction reader and closes agentOut.
	go func() {
		_ = mcptoy.Serve(upstreamInR, upstreamOutW)
		if c, ok := upstreamOutW.(io.Closer); ok {
			_ = c.Close()
		}
	}()

	// Start the proxy.
	p := proxy.New(agentInR, agentOutW, upstreamOutR, upstreamInW)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	// Feed agent-side bytes in a goroutine (the agent write end is a pipe so
	// writing blocks until the proxy reads).
	done := make(chan struct{})
	go func() {
		_, _ = agentInW.Write(input)
		_ = agentInW.Close()
		close(done)
	}()

	out, _ := io.ReadAll(agentOutR)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	}
	_ = upstreamInW.Close()
	return out
}

// oneWayForEnds returns (readEnd, writeEnd) wrapping an io.Pipe. Copied from
// proxy_test.go's oneWay with this name to keep integration tests' aliases
// separate from the rapid property test scaffolding.
func oneWayForEnds() (io.ReadWriteCloser, io.ReadWriteCloser) {
	return oneWay()
}