// Package mcptoy is a minimal MCP stdio server used by AgentChaos integration
// tests. It speaks the JSON-RPC 2.0 subset of MCP necessary to home-grow
// deterministic flows: initialise, standard notification, and tools/call.
package mcptoy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
)

// Handshake is the sequence of JSON-RPC messages an MCP client expects from a
// server during initialisation.
const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "agentchaos-mcptoy"
	ServerVersion   = "v0.0.1"
)

// Request is the subset of JSON-RPC request fields we read in mcptoy.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ToolCallParams is the params shape of a MCP tools/call request.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Server is a stateful toy MCP server. The counter tool increments an
// internal atomic counter on each call, returning the new value.
type Server struct {
	counterVal atomic.Int64
}

// New creates a Server with counter starting at 0.
func New() *Server { return &Server{} }

// Serve runs the toy server reading newline-delimited JSON from r and writing
// newline-delimited JSON to w until r reaches EOF.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	sc := bufio.NewReader(r)
	for {
		line, err := sc.ReadBytes('\n')
		if len(line) > 0 {
			s.handleMessage(line, w)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// Serve is a convenience that creates a Server and runs it. Kept for
// backward compatibility with Phase 1 + Phase 2 integration tests.
func Serve(r io.Reader, w io.Writer) error {
	return New().Serve(r, w)
}

// handleMessage dispatches one JSON-RPC message.
func (s *Server) handleMessage(b []byte, w io.Writer) {
	if len(b) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(b, &req); err != nil {
		return
	}

	switch req.Method {
	case "initialize":
		sendMarshaled(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      decodeID(req.ID),
			"result": map[string]any{
				"protocolVersion": ProtocolVersion,
				"serverInfo": map[string]any{
					"name":    ServerName,
					"version": ServerVersion,
				},
				"capabilities": map[string]any{"tools": map[string]any{}},
			},
		})
	case "notifications/initialized":
		return
	case "tools/list":
		sendMarshaled(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      decodeID(req.ID),
			"result": map[string]any{
				"tools": []any{
					map[string]any{
						"name":        "echo",
						"description": "Echo every key/value of arguments back in result.",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"text": map[string]any{"type": "string"},
							},
						},
					},
					map[string]any{
						"name":        "counter",
						"description": "Increment a per-instance counter and return the new value.",
						"inputSchema": map[string]any{"type": "object"},
					},
				},
			},
		})
	case "tools/call":
		var p ToolCallParams
		if req.Params != nil {
			_ = json.Unmarshal(req.Params, &p)
		}
		var result any
		switch p.Name {
		case "echo":
			out := map[string]any{"ok": true, "echo": p.Arguments}
			result = map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": fmt.Sprintf("%v", p.Arguments)},
				},
				"_internals": out,
			}
		case "counter":
			n := s.counterVal.Add(1)
			result = map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": fmt.Sprintf("counter=%d", n)},
				},
				"counter": n,
			}
		default:
			result = map[string]any{
				"isError": true,
				"content": []any{
					map[string]any{"type": "text", "text": fmt.Sprintf("unknown tool: %q", p.Name)},
				},
			}
		}
		sendMarshaled(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      decodeID(req.ID),
			"result":  result,
		})
	default:
		sendMarshaled(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      decodeID(req.ID),
			"error": map[string]any{
				"code":    -32601,
				"message": fmt.Sprintf("method not handled: %s", req.Method),
			},
		})
	}
}

// decodeID echoes the request id in the response regardless of whether it was
// a number or string. nil id (notification) returns nil.
func decodeID(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var id any
	if err := json.Unmarshal(raw, &id); err == nil {
		return id
	}
	return string(raw)
}

func sendMarshaled(w io.Writer, v map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = w.Write(append(b, '\n'))
	if f, ok := w.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}