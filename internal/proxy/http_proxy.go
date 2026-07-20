package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// HTTPOptions configures the streamable-HTTP upstream.
type HTTPOptions struct {
	// ReverseGET, when true, opens a long-lived SSE GET on the upstream URL
	// to pump server-initiated notifications to the agent. When false, only
	// POST/response traffic is forwarded.
	ReverseGET bool
}

// HTTPProxy is a transparent proxy that exposes an stdio server side to an
// agent and connects to an MCP server over Streamable HTTP on the upstream
// side. In v1 the agent always speaks stdio (newline-delimited JSON); the
// upstream side speaks HTTP with JSON or SSE bodies.
type HTTPProxy struct {
	agentIn  io.Reader
	agentOut io.Writer
	url      string
	opts     *HTTPOptions
	client   *http.Client

	mu sync.Mutex // serialises writes to agentOut
}

// NewHTTP constructs a streamable-HTTP-proxy. If opts is nil, defaults
// (no reverse GET) are used. The proxy uses a default *http.Client with no
// timeout (rely on context cancellation for timeouts).
func NewHTTP(agentIn io.Reader, agentOut io.Writer, url string, opts *HTTPOptions) *HTTPProxy {
	if opts == nil {
		opts = &HTTPOptions{}
	}
	return &HTTPProxy{
		agentIn:  agentIn,
		agentOut: agentOut,
		url:      url,
		opts:     opts,
		client:   &http.Client{},
	}
}

// Run forwards traffic between the agent and upstream until one side ends.
// When the agent closes stdin (EOF), the forward pump completes and the
// reverse pump (if any) is cancelled; agent stdout is closed so the agent
// observes EOF on its read.
func (p *HTTPProxy) Run(ctx context.Context) error {
	innerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var fwdErr, revErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		fwdErr = p.pumpForward(innerCtx)
		// Forward completion terminates reverse (forces the GET reader to EOF
		// through request-context cancellation and lets the connection close).
		cancel()
	}()

	if p.opts.ReverseGET {
		wg.Add(1)
		go func() {
			defer wg.Done()
			revErr = p.pumpReverse(innerCtx)
			cancel()
			// Reverse completion must also terminate forward. io.Pipe and
			// os.File reads are not interrupted by ctx cancellation,
			// so we close p.agentIn (where it is a Closer) to unblock the
			// blocked read. Errors from this path are treated as benign below.
			if c, ok := p.agentIn.(io.Closer); ok {
				_ = c.Close()
			}
		}()
	}

	wg.Wait()
	if c, ok := p.agentOut.(io.Closer); ok {
		_ = c.Close()
	}

	// Benign termination errors: context cancellation, plain EOF, and the
	// ErrClosedPipe that arises from us actively closing p.agentIn to
	// interrupt a blocked forward read after the reverse pump ended.
	benign := func(err error) bool {
		return err == nil ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, io.EOF) ||
			errors.Is(err, io.ErrClosedPipe)
	}
	if !benign(fwdErr) {
		return fwdErr
	}
	if !benign(revErr) {
		return revErr
	}
	return nil
}

// pumpForward reads newline-delimited JSON from the agent, POSTs each line to
// the upstream, and delivers the upstream response (single JSON body or a
// sequence of SSE event payloads) as newline-delimited JSON to the agent.
func (p *HTTPProxy) pumpForward(ctx context.Context) error {
	r := bufio.NewReader(p.agentIn)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			body := bytes.TrimSpace(bytes.TrimRight(line, "\n"))
			if len(body) > 0 {
				if ferr := p.post(ctx, body); ferr != nil {
					return ferr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// post sends one JSON-RPC message body (one line, no newline) to the upstream
// via HTTP POST, and writes any response payload(s) to agentOut.
func (p *HTTPProxy) post(ctx context.Context, msg []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		// notification acknowledged, no body.
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream POST returned status %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "text/event-stream"):
		return p.consumeSSE(ctx, resp.Body)
	default:
		// application/json: full body is a single JSON-RPC message.
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		body = bytes.TrimSpace(body)
		if len(body) == 0 {
			return nil
		}
		return p.writeToAgent(append(body, '\n'))
	}
}

// consumeSSE reads an SSE stream and emits each event payload to agentOut as
// one newline-delimited JSON line. Multiple data: lines within a single event
// are joined with \n before being emitted.
func (p *HTTPProxy) consumeSSE(ctx context.Context, r io.Reader) error {
	sc := bufio.NewReader(r)
	var parts [][]byte
	flush := func() error {
		if len(parts) == 0 {
			return nil
		}
		var payload []byte
		for i, p := range parts {
			if i > 0 {
				payload = append(payload, '\n')
			}
			payload = append(payload, p...)
		}
		parts = parts[:0]
		if len(payload) == 0 {
			return nil
		}
		return p.writeToAgent(append(payload, '\n'))
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := sc.ReadBytes('\n')
		// strip trailing CR/LF
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			if ferr := flush(); ferr != nil {
				return ferr
			}
		} else if bytes.HasPrefix(line, []byte("data:")) {
			v := bytes.TrimPrefix(line, []byte("data:"))
			if len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			parts = append(parts, v)
		}
		// Other fields (id:, event:, comments) are ignored in v1.
		if err != nil {
			if err == io.EOF {
				return flush()
			}
			return err
		}
	}
}

// pumpReverse opens a long-lived GET on the upstream URL to receive
// server-initiated SSE events and pumps them to agentOut.
func (p *HTTPProxy) pumpReverse(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream GET returned status %d", resp.StatusCode)
	}
	return p.consumeSSE(ctx, resp.Body)
}

// writeToAgent writes a complete ny-delimited frame to agentOut under the
// shared mutex so forward and reverse pumps cannot interleave writes.
func (p *HTTPProxy) writeToAgent(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if f, ok := p.agentOut.(interface{ Flush() error }); ok {
		defer func() { _ = f.Flush() }()
	}
	_, err := p.agentOut.Write(b)
	return err
}