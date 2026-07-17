package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

func TestHTTPListAndCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}})
		case "tools/call":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer srv.Close()
	agent := supervisor.Agent{Ref: "remote", Transport: "streamable-http", URL: srv.URL, Enabled: true}
	c := New()
	tools, err := c.ListTools(context.Background(), agent)
	if err != nil || len(tools) != 1 || tools[0]["name"] != "echo" {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	result, err := c.CallTool(context.Background(), agent, "echo", map[string]any{"value": "x"})
	if err != nil || result["content"] == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestStdioListAndCall(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "child.sh")
	body := `#!/bin/sh
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}' ;;
  *'"method":"notifications/initialized"'*) ;;
  *'"method":"tools/list"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}' ;;
  *'"method":"tools/call"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"stdio-ok"}]}}' ;;
 esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	agent := supervisor.Agent{Ref: "local", Transport: "stdio", Command: script, Enabled: true}
	c := New()
	tools, err := c.ListTools(context.Background(), agent)
	if err != nil || len(tools) != 1 || fmt.Sprint(tools[0]["name"]) != "echo" {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	result, err := c.CallTool(context.Background(), agent, "echo", nil)
	if err != nil || result["content"] == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestDisabledAgentRejected(t *testing.T) {
	_, err := New().ListTools(context.Background(), supervisor.Agent{Ref: "off", Transport: "streamable-http", URL: "http://127.0.0.1", Enabled: false})
	if err == nil {
		t.Fatal("expected disabled error")
	}
}

func TestStdioSessionIsReusedAcrossListAndCall(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "starts")
	script := filepath.Join(dir, "child.sh")
	body := `#!/bin/sh
echo x >> "$COUNT_FILE"
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}' ;;
  *'"method":"notifications/initialized"'*) ;;
  *'"method":"tools/list"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}' ;;
  *'"method":"tools/call"'*) printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}' ;;
 esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	agent := supervisor.Agent{Ref: "reused", Transport: "stdio", Command: script, Env: map[string]string{"COUNT_FILE": counter}, Enabled: true}
	c := New()
	defer c.CloseAll()
	if _, err := c.ListTools(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallTool(context.Background(), agent, "echo", nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "x"); got != 1 {
		t.Fatalf("process starts=%d data=%q", got, data)
	}
	if err := c.Close(agent.Ref); err != nil {
		t.Fatal(err)
	}
}
