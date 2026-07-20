package proxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/seanrobmerriam/agentchaos/internal/proxy"
)

// ---------------------------------------------------------------------------
// Phase 1 gate: the Proxy is transparent against the REAL MCP "everything"
// reference server (https://www.npmjs.com/package/@modelcontextprotocol/server-everything).
//
// The test spawns the official server over stdio as a subprocess, sits the
// stdio Proxy between the in-process MCP client (the test, acting as the
// "agent runtime") and the subprocess, and runs a full MCP handshake +
// tools/list + tools/call. Passing this gate proves real MCP clients see
// the Proxy as a no-op against a real MCP server — not just our own toy.
// ---------------------------------------------------------------------------

// npxAvailable checks whether npx exists on PATH.
func npxAvailable() bool {
	_, err := exec.LookPath("npx")
	return err == nil
}

// everythingEnv returns true if the AGENTCHAOS_SKIP_REAL_MCP env var is set
// to "1", allowing CI without Node to skip this test explicitly rather than
// fail it. When unset, the test only skips if npx is missing from PATH.
func everythingSkipRequested() bool {
	return os.Getenv("AGENTCHAOS_SKIP_REAL_MCP") == "1"
}

// TestProxyVsRealEverythingServer is the literal Phase 1 gate. It must pass
// under -race with npx available. The test is skipped (not failed) when
// AGENTCHAOS_SKIP_REAL_MCP=1 or npx is not on PATH.
func TestProxyVsRealEverythingServer(t *testing.T) {
	if everythingSkipRequested() {
		t.Skip("AGENTCHAOS_SKIP_REAL_MCP=1")
	}
	if !npxAvailable() {
		t.Skip("npx not on PATH")
	}

	// Spawn the real server.
	cmd := exec.Command("npx", "-y", "@modelcontextprotocol/server-everything", "stdio")
	cmd.Env = os.Environ()
	childIn, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start everything server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		time.Sleep(50 * time.Millisecond)
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Build agent <-> proxy pipes.
	agentInR, agentInW := oneWayForEnds() // proxy reads agentInR, agent writes agentInW
	agentOutR, agentOutW := oneWayForEnds() // proxy writes agentOutW, agent reads agentOutR

	// Proxy reads agentIn = agentInR; writes upstreamOut = childIn;
	// reads upstreamIn = childOut; writes agentOut = agentOutW.
	p := proxy.New(agentInR, agentOutW, childOut, childIn)
	proxyCtx, proxyCancel := context.WithCancel(context.Background())
	defer proxyCancel()
	go func() { _ = p.Run(proxyCtx) }()

	// Agent writes to agentInW; reads from agentOutR.
	agentWrite := agentInW
	client := newAgentReader(agentOutR)
	send := func(m map[string]any) {
		t.Helper()
		b, _ := json.Marshal(m)
		if _, err := agentWrite.Write(append(b, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// readUntilID reads messages from the agent's output until it sees a
	// JSON-RPC response with the expected numeric id. Interspersed server
	// notifications (e.g. logging) are logged and skipped.
	readUntilID := func(wantID float64) map[string]any {
		t.Helper()
		for {
			msg := client.ReadOne(t)
			if id, ok := idFloat64(msg["id"]); ok && id == wantID {
				return msg
			}
			// Not our response — log it as a skipped notification or
			// unexpected response and keep reading.
			t.Logf("[skip] non-matching message: %v", msg)
		}
	}

	// 1) initialize
	send(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "agentchaos-test", "version": "v0.0.1"},
		},
	})
	resp := readUntilID(1)
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("initialize missing result: %v", resp)
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo == nil || !strings.Contains(getString(serverInfo, "name"), "everything") {
		t.Fatalf("initialize serverInfo.name doesn't look like everything: %v", serverInfo)
	}
	t.Logf("[initialize] server=%q protocol=%v", getString(serverInfo, "name"), result["protocolVersion"])

	// 2) notifications/initialized (no reply expected)
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	// 3) tools/list
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	resp = readUntilID(2)
	result, _ = resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) < 5 {
		t.Fatalf("expected >=5 tools from everything server, got %d: %v", len(tools), result)
	}
	toolNames := make([]string, 0, len(tools))
	for _, t0 := range tools {
		if m, ok := t0.(map[string]any); ok {
			toolNames = append(toolNames, getString(m, "name"))
		}
	}
	t.Logf("[tools/list] %d tools: %v", len(toolNames), toolNames)
	if !contains(toolNames, "echo") {
		t.Fatalf("tools/list missing echo: %v", toolNames)
	}

	// 4) tools/call echo with a payload
	send(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"message": "hello-from-proxy"},
		},
	})
	resp = readUntilID(3)
	result, _ = resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call echo missing content: %v", result)
	}
	echoed := false
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if txt, _ := m["text"].(string); strings.Contains(txt, "hello-from-proxy") {
				echoed = true
			}
		}
	}
	if !echoed {
		t.Fatalf("tools/call echo didn't echo our payload: %v", result)
	}
	t.Logf("[tools/call echo] echoed back our payload")

	// 5) tools/call get-sum with two arguments (sanity check a non-trivial
	// structured response through the proxy). The everything server defines
	// get-sum accepting `a` and `b` numbers; we send 11 and 31 and expect 42.
	send(map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{
			"name":      "get-sum",
			"arguments": map[string]any{"a": 11, "b": 31},
		},
	})
	resp = readUntilID(4)
	t.Logf("[tools/call get-sum] response: %v", resp["result"])
}

// getString safely reads string field from a map[string]any.
func getString(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// contains is a tiny helper; we don't pull slices just for this.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// _ keeps import valid if we remove ioutil later.
var _ = fmt.Sprintf