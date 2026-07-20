package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/seanrobmerriam/agentchaos/internal/fault"
	"github.com/seanrobmerriam/agentchaos/internal/scenario"
)

// HTTPFaultProxy is a fault-aware HTTP transport proxy. It extends
// HTTPProxy-style behaviour by running each parsed JSON-RPC frame through a
// fault.Executor in both directions before forwarding.
//
// v1 scope:
//   - Forward: read newline-delimited JSON-RPC from agentIn, parse, run
//     ProcessForward, POST each resulting frame to the upstream URL, parse
//     the response body as JSON-RPC, run ProcessReverse, write each
//     resulting frame to agentOut.
//   - The optional reverse-channel SSE pump is out of scope for v1; ReverseGET
//     is honoured by inheriting the upstream's HTTP framing only inside the
//     POST response (single JSON body).
//   - kill_process fires through the executor's kill return; the proxy Run()
//     returns 77 and the agent side observes an EOF on its read.
type HTTPFaultProxy struct {
	agentIn  io.Reader
	agentOut io.Writer
	url      string
	opts     *HTTPOptions
	ex       *fault.Executor
	client   *http.Client

	mu sync.Mutex // serialises writes to agentOut
}

// NewHTTPFault constructs a fault-aware HTTP proxy. opts may be nil for
// defaults. ex must be created with fault.NewExecutorForTransport(..., fault.TransportHTTP)
// so reorder is allowed.
func NewHTTPFault(agentIn io.Reader, agentOut io.Writer, url string, opts *HTTPOptions, ex *fault.Executor) *HTTPFaultProxy {
	if opts == nil {
		opts = &HTTPOptions{}
	}
	return &HTTPFaultProxy{
		agentIn:  agentIn,
		agentOut: agentOut,
		url:      url,
		opts:     opts,
		ex:       ex,
		client:   &http.Client{},
	}
}

// Run pumps traffic until EOF on agentIn, the context is cancelled, or
// kill_process fires. The returned exit code follows the stdio pump
// convention: 0 on clean completion, 77 if kill_process fired, and the
// context error is propagated to the caller via the error return when it
// caused the run to abort.
//
// The forward pump only: a single goroutine reads from agentIn, parses
// each line, applies faults, POSTs to upstream, parses the response,
// applies reverse faults, and writes to agentOut. This matches v1 scope
// (no reverse SSE channel).
func (p *HTTPFaultProxy) Run(ctx context.Context) (int, error) {
	r := bufio.NewReader(p.agentIn)
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			body := bytes.TrimSpace(bytes.TrimRight(line, "\n"))
			if len(body) > 0 {
				code, ferr := p.handleOne(ctx, body)
				if ferr != nil {
					return code, ferr
				}
				if code == 77 {
					// kill_process: drain agentIn on best-effort basis by
					// closing it (if a Closer) and return immediately.
					if c, ok := p.agentIn.(io.Closer); ok {
						_ = c.Close()
					}
					return 77, nil
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return 0, nil
			}
			return 0, err
		}
	}
}

// handleOne runs a single forward message through the executor, POSTs it to
// the upstream, parses the response, runs it through the reverse executor,
// and writes each resulting frame to agentOut.
func (p *HTTPFaultProxy) handleOne(ctx context.Context, body []byte) (int, error) {
	// The executor consumes frames one at a time and may emit multiple
	// (duplicate) or zero (in_doubt). We POST each emitted frame and
	// process its response through ProcessReverse. For v1, the response is
	// read as a single JSON-RPC body per request; the executor handles
	// any further replication/reordering.
	msg := scenario.ParseMessage(body)
	forward, kill := p.ex.ProcessForward(msg, body, fault.AgentToUpstream)
	if len(forward) == 0 {
		// in_doubt request: still need to POST to upstream so the
		// response is generated and then dropped by the reverse pass.
		forward = [][]byte{body}
	}
	if kill {
		// Forward the kill frame (per executor semantics) before exiting.
		// We deliberately do NOT POST it: kill_process is local.
		if len(forward) > 0 {
			for _, b := range forward {
				if werr := p.writeToAgent(append(b, '\n')); werr != nil {
					return 77, werr
				}
			}
		}
		return 77, nil
	}

	for _, frame := range forward {
		respBody, err := p.post(ctx, frame)
		if err != nil {
			return 0, err
		}
		if len(respBody) == 0 {
			continue
		}
		// Parse the response and run it through ProcessReverse.
		rMsg := scenario.ParseMessage(respBody)
		out, _ := p.ex.ProcessReverse(rMsg, respBody, fault.UpstreamToAgent)
		for _, b := range out {
			if werr := p.writeToAgent(append(b, '\n')); werr != nil {
				return 0, werr
			}
		}
		// If reorder buffered this response, drain and write the rest.
		for _, b := range p.ex.Drain() {
			if werr := p.writeToAgent(append(b, '\n')); werr != nil {
				return 0, werr
			}
		}
	}
	return 0, nil
}

// post sends one JSON-RPC message body to the upstream via HTTP POST and
// returns the parsed response body as a JSON byte slice. SSE responses
// return an error in v1 (SSE reverse-channel is a follow-up).
func (p *HTTPFaultProxy) post(ctx context.Context, msg []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream POST returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	body = bytes.TrimSpace(body)
	return body, nil
}

// writeToAgent writes a complete newline-delimited frame to agentOut under
// the shared mutex so concurrent writes (forward + any future reverse
// pumps) cannot interleave.
func (p *HTTPFaultProxy) writeToAgent(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if f, ok := p.agentOut.(interface{ Flush() error }); ok {
		defer func() { _ = f.Flush() }()
	}
	_, err := p.agentOut.Write(b)
	return err
}
