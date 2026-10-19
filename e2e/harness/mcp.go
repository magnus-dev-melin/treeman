package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// MCPServer drives a `treeman mcp` subprocess over its stdio JSON-RPC
// transport: one request line in, one response line out.
type MCPServer struct {
	t      *testing.T
	stdin  io.WriteCloser
	rdr    *bufio.Reader
	nextID int
}

// StartMCPServer builds the treeman binary, starts `treeman mcp` in cwd
// with the test's environment, and completes the initialize handshake.
// The process is killed in t.Cleanup.
func StartMCPServer(t *testing.T, cwd string) *MCPServer {
	t.Helper()
	bin := BuildTreeman(t)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "mcp")
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start mcp: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		cancel()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	m := &MCPServer{t: t, stdin: stdin, rdr: bufio.NewReader(stdout)}
	resp := m.request("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "treeman-e2e", "version": "0.0.0"},
	})
	if resp["error"] != nil {
		t.Fatalf("initialize: %v", resp["error"])
	}
	m.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return m
}

// CallTool calls a tool and returns its structured output plus the
// text of a tool-level error (empty when the call succeeded). A
// protocol-level JSON-RPC error fails the test.
func (m *MCPServer) CallTool(name string, args map[string]any) (map[string]any, string) {
	m.t.Helper()
	resp := m.request("tools/call", map[string]any{"name": name, "arguments": args})
	if resp["error"] != nil {
		m.t.Fatalf("%s: JSON-RPC error: %v", name, resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		m.t.Fatalf("%s: response missing result: %+v", name, resp)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		return nil, toolText(result)
	}
	sc, _ := result["structuredContent"].(map[string]any)
	return sc, ""
}

func toolText(result map[string]any) string {
	content, _ := result["content"].([]any)
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				return s
			}
		}
	}
	return "tool error without text"
}

func (m *MCPServer) request(method string, params map[string]any) map[string]any {
	m.t.Helper()
	m.nextID++
	m.send(map[string]any{"jsonrpc": "2.0", "id": m.nextID, "method": method, "params": params})
	line, err := m.rdr.ReadBytes('\n')
	if err != nil && err != io.EOF {
		m.t.Fatalf("read %s response: %v", method, err)
	}
	if len(line) == 0 {
		m.t.Fatalf("empty %s response", method)
	}
	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		m.t.Fatalf("parse %s response %q: %v", method, line, err)
	}
	return resp
}

func (m *MCPServer) send(msg map[string]any) {
	m.t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		m.t.Fatal(err)
	}
	if _, err := m.stdin.Write(append(body, '\n')); err != nil {
		m.t.Fatalf("write: %v", err)
	}
}

// BuildTreeman builds ./cmd/treeman into a temp dir and returns the
// binary's path. Tests run with the suite directory as cwd, so the
// module root is two levels up.
func BuildTreeman(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "treeman")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/treeman")
	cmd.Dir = filepath.Dir(filepath.Dir(wd))
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build treeman: %v", err)
	}
	return bin
}
