// Package proxy is the transparent bidirectional copier that AgentChaos uses
// to sit between an MCP client (the agent runtime) and an MCP server
// (upstream). Transport adapters build the four io ends that the Proxy
// shuttles bytes between:
//
//	forward:  agentIn    -> upstreamOut   (agent -> upstream)
//	reverse:  upstreamIn -> agentOut     (upstream -> agent)
//
// In v1 the agent side is always stdio and the upstream side is either stdio
// (spawned child process) or Streamable HTTP.
package proxy

import (
	"bufio"
	"context"
	"io"
	"sync"
)

// Proxy forwards JSON-RPC frames in both directions. Phase 1 has no fault
// injection: every forwarded byte is preserved exactly. The line-fenced
// reader is in place so later phases (which need per-message boundaries for
// matching and fault injection) can intercept messages without changing the
// public API.
type Proxy struct {
	agentIn     io.Reader // bytes the agent produces (proxy reads here)
	agentOut    io.Writer // bytes delivered to the agent (proxy writes here)
	upstreamIn  io.Reader // bytes upstream produces (proxy reads here)
	upstreamOut io.Writer // bytes delivered upstream (proxy writes here)
}

// New constructs a Proxy over four io ends.
func New(agentIn io.Reader, agentOut io.Writer, upstreamIn io.Reader, upstreamOut io.Writer) *Proxy {
	return &Proxy{
		agentIn:     agentIn,
		agentOut:    agentOut,
		upstreamIn:  upstreamIn,
		upstreamOut: upstreamOut,
	}
}

// Run forwards messages in both directions until both sides reach EOF or the
// context is cancelled. When one direction finishes it closes its dst writer
// so the other end of that channel observes EOF. The function blocks until
// both directions have completed or ctx is done.
func (p *Proxy) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)
	var fwdErr, revErr error

	go func() {
		defer wg.Done()
		fwdErr = copyLines(ctx, p.upstreamOut, p.agentIn)
		if c, ok := p.upstreamOut.(io.Closer); ok {
			_ = c.Close()
		}
	}()
	go func() {
		defer wg.Done()
		revErr = copyLines(ctx, p.agentOut, p.upstreamIn)
		if c, ok := p.agentOut.(io.Closer); ok {
			_ = c.Close()
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-ctx.Done():
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if fwdErr != nil && fwdErr != io.EOF {
		return fwdErr
	}
	if revErr != nil && revErr != io.EOF {
		return revErr
	}
	return nil
}

// copyLines copies from src to dst one newline-delimited frame at a time.
// ReadBytes returns the trailing delimiter with the line; empty-data EOF
// yields a clean exit. A line without a trailing newline at EOF is forwarded
// as-is (preserves byte-for-byte input that lacks a final newline).
func copyLines(ctx context.Context, dst io.Writer, src io.Reader) error {
	r := bufio.NewReader(src)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := dst.Write(line); werr != nil {
				return werr
			}
			if f, ok := dst.(interface{ Flush() error }); ok {
				_ = f.Flush()
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
