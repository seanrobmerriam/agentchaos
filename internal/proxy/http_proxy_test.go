package proxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/proxy"

	"pgregory.net/rapid"
)

// ---------------------------------------------------------------------------
// Structured client-side message generators (used by the HTTP property tests)
// ---------------------------------------------------------------------------

// clientMsg is a single JSON-RPC message sent by the agent to the upstream
// over Streamable HTTP. v1 v1: only requests (with id) and notifications
// (without id) are meaningful as agent-to-upstream traffic; responses and
// server-initiated notifications are modelled on the upstream side.
type clientMsg struct {
	IsReq  bool
	ID     int
	Method string
}

// json returns the JSON-RPC body bytes of this message.
func (m clientMsg) json(t interface{ Fatalf(string, ...any) }) []byte {
	if m.IsReq {
		return jsonRound(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      m.ID,
			"method":  m.Method,
		})
	}
	return jsonRound(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  m.Method,
	})
}

// responseJSON returns the JSON-RPC response that the test HTTP server emits
// for this message. Notifications produce no response (returns nil).
func (m clientMsg) responseJSON(t interface{ Fatalf(string, ...any) }) []byte {
	if !m.IsReq {
		return nil
	}
	return jsonRound(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      m.ID,
		"result": map[string]any{
			"ok":    true,
			"echo":  m.Method,
			"reqid": m.ID,
		},
	})
}

// genClientSequence generates 1..32 client-side JSON-RPC messages.
func genClientSequence(t *rapid.T) []clientMsg {
	n := rapid.IntRange(1, 32).Draw(t, "count")
	out := make([]clientMsg, n)
	for i := 0; i < n; i++ {
		isReq := rapid.Bool().Draw(t, "is_req")
		method := rapid.StringMatching(`[a-zA-Z_][a-zA-Z0-9_]*`).Draw(t, "method")
		if isReq {
			out[i] = clientMsg{
				IsReq:  true,
				ID:     rapid.IntRange(1, 1_000_000).Draw(t, "id"),
				Method: method,
			}
		} else {
			out[i] = clientMsg{IsReq: false, Method: method}
		}
	}
	return out
}

func bytesAsNewlineDelimited(parts [][]byte) []byte {
	var sb strings.Builder
	for _, p := range parts {
		sb.Write(p)
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// ---------------------------------------------------------------------------
// Streamable HTTP upstream test server
// ---------------------------------------------------------------------------

// testHTTPServer runs an httptest.Server that:
//   - On POST: records the raw body; if body is a request (has id), responds
//     200 OK with application/json body = clientMsg.responseJSON; if it is a
//     notification (no id), responds 202 Accepted (no body).
//   - On GET: holds a text/event-stream response open, emitting each payload in
//     reversePushes through
//     reversePushes (one SSE event per payload), then blocks until ctx done.
type testHTTPServer struct {
	t         *testing.T
	url       string
	server    *httptest.Server
	mu        sync.Mutex
	received  [][]byte // ordered POST bodies received from the proxy
	revMtx    sync.Mutex
	revPushes [][]byte // payloads to emit on GET SSE
}

func newTestHTTPServer(t *testing.T) *testHTTPServer {
	t.Helper()
	s := &testHTTPServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	srv := httptest.NewServer(mux)
	s.url = srv.URL
	s.server = srv
	return s
}

func (s *testHTTPServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.received = append(s.received, body)
		s.mu.Unlock()
		var peek struct {
			ID *json.Number `json:"id"`
		}
		_ = json.Unmarshal(body, &peek)
		if peek.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// Build a matching response.
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		respBody, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"ok":    true,
				"echo":  req.Method,
				"reqid": req.ID,
			},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBody)
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		s.revMtx.Lock()
		pushes := s.revPushes
		s.revMtx.Unlock()
		for _, p := range pushes {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", p)
			if flusher != nil {
				flusher.Flush()
			}
		}
		// End response after emitting all queued events; closes the SSE
		// stream so the proxy observes an EOF on its GET reader.
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *testHTTPServer) Close() { s.server.Close() }

func (s *testHTTPServer) ReceivedPOSTs() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.received))
	copy(out, s.received)
	return out
}

func (s *testHTTPServer) SetReversePushes(parts [][]byte) {
	s.revMtx.Lock()
	s.revPushes = append([][]byte(nil), parts...)
	s.revMtx.Unlock()
}

// ---------------------------------------------------------------------------
// Pipe scaffolding specific to HTTP tests
// ---------------------------------------------------------------------------

type httpPipes struct {
	agentInW, agentOutR io.ReadWriteCloser
}

func newHTTPPipes() (proxyIn io.Reader, proxyOut io.Writer, pipes *httpPipes) {
	r1, w1 := oneWay() // agent-in proxy reads; agent writes
	r2, w2 := oneWay() // agent-out proxy writes; agent reads
	return r1, w2, &httpPipes{agentInW: w1, agentOutR: r2}
}

// ---------------------------------------------------------------------------
// Example: a single POST/response round-trip
// ---------------------------------------------------------------------------

func TestHTTPForwardSingleExample(t *testing.T) {
	srv := newTestHTTPServer(t)
	defer srv.Close()
	proxyIn, proxyOut, pipes := newHTTPPipes()

	p := proxy.NewHTTP(proxyIn, proxyOut, srv.url, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = p.Run(ctx) }()

	msg := clientMsg{IsReq: true, ID: 7, Method: "ping"}
	in := append(msg.json(t), '\n')

	done := make(chan struct{})
	go func() {
		_, _ = pipes.agentInW.Write(in)
		_ = pipes.agentInW.Close()
		close(done)
	}()

	out, err := io.ReadAll(pipes.agentOutR)
	if err != nil {
		t.Fatalf("read agent side: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("agent write timed out")
	}

	want := append(msg.responseJSON(t), '\n')
	if !bytes.Equal(out, want) {
		t.Fatalf("got=%q want=%q", out, want)
	}

	posts := srv.ReceivedPOSTs()
	if len(posts) != 1 {
		t.Fatalf("upstream received %d POSTs, want 1", len(posts))
	}
	if !bytes.Equal(posts[0], bytes.TrimSpace(in)) {
		t.Fatalf("upstream received %q, want %q", posts[0], bytes.TrimSpace(in))
	}
	cancel()
}

// ---------------------------------------------------------------------------
// Property: arbitrary client-side sequences pass through unchanged.
// ---------------------------------------------------------------------------

// Property: every POST body received upstream equals the JSON the agent sent;
// every response the agent received equals the response computed for the
// matched request, in order.
func TestHTTPPassthroughForwardProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		msgs := genClientSequence(rt)
		expected := make([][]byte, 0, len(msgs))
		sent := make([][]byte, 0, len(msgs))
		for _, m := range msgs {
			b := m.json(rt)
			sent = append(sent, b)
			if r := m.responseJSON(rt); r != nil {
				expected = append(expected, r)
			}
		}
		expectedBytes := bytesAsNewlineDelimited(expected)
		sentBytes := bytesAsNewlineDelimited(sent)

		srv := newTestHTTPServer(t)
		defer srv.Close()
		proxyIn, proxyOut, pipes := newHTTPPipes()

		p := proxy.NewHTTP(proxyIn, proxyOut, srv.url, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = p.Run(ctx) }()

		done := make(chan struct{})
		go func() {
			_, _ = pipes.agentInW.Write(sentBytes)
			_ = pipes.agentInW.Close()
			close(done)
		}()

		out, err := io.ReadAll(pipes.agentOutR)
		if err != nil {
			rt.Fatalf("read agent side: %v", err)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			rt.Fatal("agent write timed out")
		}

		// Upstream received identical bytes to what the agent wrote (one POST
		// body per JSON-RPC message; newline delimiter is not part of the
		// POST body).
		posts := srv.ReceivedPOSTs()
		if len(posts) != len(msgs) {
			rt.Fatalf("upstream recorded %d POSTs, want %d", len(posts), len(msgs))
		}
		for i, wantBody := range sent {
			if !bytes.Equal(posts[i], wantBody) {
				rt.Fatalf("POST[%d] body mismatch:\n got=%q\n want=%q", i, posts[i], wantBody)
			}
		}

		// Agent received exactly the responses we predicted, concatenated as
		// newline-delimited JSON.
		if !bytes.Equal(out, expectedBytes) {
			rt.Fatalf("agent output mismatch:\n got   =%q\n want=%q", out, expectedBytes)
		}

		cancel()
	})
}

// ---------------------------------------------------------------------------
// Property: server-pushed SSE events arrive unchanged on the agent side.
// ---------------------------------------------------------------------------

// Property: arbitrary SSE payloads emitted by the upstream reach the agent
// as newline-delimited JSON, byte-identical to the payloads themselves.
func TestHTTPPassthroughReverseSSEProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate K notification payloads.
		n := rapid.IntRange(1, 32).Draw(rt, "count")
		payloads := make([][]byte, n)
		for i := 0; i < n; i++ {
			method := rapid.StringMatching(`[a-zA-Z_][a-zA-Z0-9_]*`).Draw(rt, "method")
			payloads[i] = jsonRound(rt, map[string]any{
				"jsonrpc": "2.0",
				"method":  method,
			})
		}
		expected := bytesAsNewlineDelimited(payloads)

		srv := newTestHTTPServer(t)
		defer srv.Close()
		srv.SetReversePushes(payloads)

		proxyIn, proxyOut, pipes := newHTTPPipes()
		opts := &proxy.HTTPOptions{ReverseGET: true}
		p := proxy.NewHTTP(proxyIn, proxyOut, srv.url, opts)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = p.Run(ctx) }()

		out, err := io.ReadAll(pipes.agentOutR)
		if err != nil {
			rt.Fatalf("read agent side: %v", err)
		}
		if !bytes.Equal(out, expected) {
			rt.Fatalf("SSE passthrough mismatch:\n got   =%q\n want=%q", out, expected)
		}
	})
}
