package proxy_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/proxy"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

const (
	dupResp = `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hello"}]}}`
	dupReq  = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`
)

// httpFixture captures the bodies posted to the upstream and replies with a
// fixed JSON-RPC response for each request. Used by the fault-injection
// integration tests below.
type httpFixture struct {
	mu        sync.Mutex
	requests  [][]byte
	replyBody []byte
	replyCode int
}

func newHTTPFixture(reply string) *httpFixture {
	return &httpFixture{replyBody: []byte(reply), replyCode: http.StatusOK}
}

func (f *httpFixture) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.replyCode)
		_, _ = w.Write(f.replyBody)
	}
}

func (f *httpFixture) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func TestHTTPFault_ForwardsResponse(t *testing.T) {
	fix := newHTTPFixture(dupResp)
	srv := httptest.NewServer(fix.handler(t))
	defer srv.Close()

	s := &scenario.Scenario{Seed: 1}
	ex, err := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportHTTP)
	if err != nil {
		t.Fatalf("executor: %v", err)
	}

	agentIn := bytes.NewBufferString(dupReq + "\n")
	var agentOut bytes.Buffer

	p := proxy.NewHTTPFault(agentIn, &agentOut, srv.URL, nil, ex)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := p.Run(ctx)
	if err != nil && err != io.EOF {
		t.Fatalf("Run: code=%d err=%v", code, err)
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if fix.calls() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", fix.calls())
	}
	if !bytes.Contains(agentOut.Bytes(), []byte(`"id":1`)) {
		t.Fatalf("response not forwarded: %s", agentOut.String())
	}
}

func TestHTTPFault_AppliesDuplicateFault(t *testing.T) {
	fix := newHTTPFixture(dupResp)
	srv := httptest.NewServer(fix.handler(t))
	defer srv.Close()

	s := &scenario.Scenario{
		Seed:   1,
		Faults: []scenario.Fault{{Match: scenario.Matcher{}, Action: "duplicate", Count: 2}},
	}
	ex, err := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportHTTP)
	if err != nil {
		t.Fatalf("executor: %v", err)
	}

	// Single duplicate request frame: the duplicate fault with Count=2
	// produces 2 forwards of this one message, hence 2 upstream calls.
	// The reverse pass also duplicates the response, producing two writes
	// to agentOut.
	agentIn := bytes.NewBufferString(dupReq + "\n")
	var agentOut bytes.Buffer

	p := proxy.NewHTTPFault(agentIn, &agentOut, srv.URL, nil, ex)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := p.Run(ctx)
	if err != nil && err != io.EOF {
		t.Fatalf("Run: code=%d err=%v", code, err)
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if got := fix.calls(); got != 2 {
		t.Fatalf("expected 2 upstream calls (duplicate on forward), got %d", got)
	}
	if got := bytes.Count(agentOut.Bytes(), []byte(`"id":1`)); got < 2 {
		t.Fatalf("expected >=2 occurrences of id:1 in agentOut, got %d: %s", got, agentOut.String())
	}
}

func TestHTTPFault_KillProcessExits77(t *testing.T) {
	fix := newHTTPFixture(dupResp)
	srv := httptest.NewServer(fix.handler(t))
	defer srv.Close()

	s := &scenario.Scenario{
		Seed:   1,
		Faults: []scenario.Fault{{Match: scenario.Matcher{}, Action: "kill_process"}},
	}
	ex, err := fault.NewExecutorForTransport(s, func(int) {}, fault.TransportHTTP)
	if err != nil {
		t.Fatalf("executor: %v", err)
	}

	agentIn := bytes.NewBufferString(dupReq + "\n")
	var agentOut bytes.Buffer

	p := proxy.NewHTTPFault(agentIn, &agentOut, srv.URL, nil, ex)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := p.Run(ctx)
	if err != nil {
		t.Fatalf("Run: unexpected err=%v", err)
	}
	if code != 77 {
		t.Fatalf("expected exit code 77, got %d", code)
	}
	// kill_process should not have POSTed the request.
	if got := fix.calls(); got != 0 {
		t.Fatalf("expected 0 upstream calls on kill_process, got %d", got)
	}
}
