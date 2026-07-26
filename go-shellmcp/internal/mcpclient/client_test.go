package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestStreamableHTTPReadsUntilMatchingResponseID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"tools\":[{\"name\":\"after-progress\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n", req.ID)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer srv.Close()

	tools, err := New().ListTools(context.Background(), supervisor.Agent{Ref: "events", Transport: "streamable-http", URL: srv.URL, Enabled: true})
	if err != nil || len(tools) != 1 || tools[0]["name"] != "after-progress" {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
}

func TestStreamableHTTPSessionIsReusedAndDeletedOnClose(t *testing.T) {
	var initializes atomic.Int32
	var deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != "strict-session" {
				t.Errorf("DELETE session=%q", r.Header.Get("Mcp-Session-Id"))
			}
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			initializes.Add(1)
			w.Header().Set("Mcp-Session-Id", "strict-session")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") != "strict-session" {
				t.Errorf("initialized session=%q", r.Header.Get("Mcp-Session-Id"))
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "strict-session" {
				t.Errorf("tools/list session=%q", r.Header.Get("Mcp-Session-Id"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}},
				},
			})
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "strict-session" {
				t.Errorf("tools/call session=%q", r.Header.Get("Mcp-Session-Id"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "ok"}},
				},
			})
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer srv.Close()

	c := New()
	agent := supervisor.Agent{Ref: "strict", Transport: "streamable-http", URL: srv.URL, Enabled: true}
	if _, err := c.ListTools(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallTool(context.Background(), agent, "echo", nil); err != nil {
		t.Fatal(err)
	}
	c.CloseAll()
	if got := initializes.Load(); got != 1 {
		t.Fatalf("initialize requests=%d want one reused session", got)
	}
	if got := deletes.Load(); got != 1 {
		t.Fatalf("DELETE requests=%d want one", got)
	}
}

func TestStreamableHTTPCloseAcceptsUnsupportedDelete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "optional-delete")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{}}})
		}
	}))
	defer srv.Close()
	c := New()
	agent := supervisor.Agent{Ref: "optional-delete", Transport: "streamable-http", URL: srv.URL, Enabled: true}
	if _, err := c.ListTools(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(agent.Ref); err != nil {
		t.Fatalf("Close rejected server without DELETE support: %v", err)
	}
}

func TestStreamableHTTPUsesNegotiatedProtocolVersion(t *testing.T) {
	const negotiated = "2024-11-05"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "initialize" && r.Header.Get("MCP-Protocol-Version") != negotiated {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "expected negotiated protocol %s", negotiated)
			return
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "versioned")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": negotiated, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []any{map[string]any{"name": "versioned", "inputSchema": map[string]any{"type": "object"}}},
				},
			})
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer srv.Close()

	tools, err := New().ListTools(context.Background(), supervisor.Agent{Ref: "versioned", Transport: "streamable-http", URL: srv.URL, Enabled: true})
	if err != nil || len(tools) != 1 || tools[0]["name"] != "versioned" {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
}

func TestStreamableHTTPReinitializesAfterSessionNotFound(t *testing.T) {
	var initializes atomic.Int32
	var rejected atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			count := initializes.Add(1)
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", count))
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if !rejected.Swap(true) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "recovered", "inputSchema": map[string]any{"type": "object"}}}}})
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer srv.Close()
	tools, err := New().ListTools(context.Background(), supervisor.Agent{Ref: "recover", Transport: "streamable-http", URL: srv.URL, Enabled: true})
	if err != nil || len(tools) != 1 || tools[0]["name"] != "recovered" {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	if got := initializes.Load(); got != 2 {
		t.Fatalf("initialize requests=%d want 2 after 404 recovery", got)
	}
}

func TestLegacySSEListAndCallUsesEndpointStream(t *testing.T) {
	events := make(chan []byte, 8)
	var streamCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sse":
			streamCount.Add(1)
			if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				t.Errorf("GET Accept=%q", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: endpoint\ndata: /message?sessionId=test-session\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case payload := <-events:
					_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case r.Method == http.MethodPost && r.URL.Path == "/message":
			if r.URL.Query().Get("sessionId") != "test-session" {
				t.Errorf("POST sessionId=%q", r.URL.Query().Get("sessionId"))
			}
			var req rpcRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode POST: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			var result map[string]any
			switch req.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}
			case "notifications/initialized":
				return
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}
			case "tools/call":
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "legacy-sse-ok"}}}
			default:
				t.Errorf("unexpected method %q", req.Method)
				return
			}
			payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			if err != nil {
				t.Errorf("encode response: %v", err)
				return
			}
			events <- payload
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	agent := supervisor.Agent{Ref: "legacy", Transport: "sse", URL: srv.URL + "/sse", Enabled: true}
	c := New()
	defer c.CloseAll()
	tools, err := c.ListTools(context.Background(), agent)
	if err != nil || len(tools) != 1 || tools[0]["name"] != "echo" {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	result, err := c.CallTool(context.Background(), agent, "echo", map[string]any{"value": "x"})
	if err != nil || !strings.Contains(fmt.Sprint(result["content"]), "legacy-sse-ok") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if got := streamCount.Load(); got != 1 {
		t.Fatalf("legacy SSE streams=%d want one reused session", got)
	}
}

func TestLegacySSEOpenHonorsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := New()
	agent := supervisor.Agent{Ref: "hung-sse", Transport: "sse", URL: srv.URL, Enabled: true}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.ListTools(ctx, agent)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ListTools error=%v want deadline exceeded", err)
		}
	case <-time.After(200 * time.Millisecond):
		_ = c.Close(agent.Ref)
		t.Fatal("legacy SSE endpoint discovery ignored context cancellation")
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

func TestStdioChildStderrCaptureIsBounded(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "noisy-child.sh")
	body := `#!/bin/sh
dd if=/dev/zero bs=1024 count=1024 2>/dev/null | tr '\000' x >&2
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}' ;;
  *'"method":"notifications/initialized"'*) ;;
  *'"method":"tools/list"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"quiet-tool","inputSchema":{"type":"object"}}]}}' ;;
 esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	c := New()
	defer c.CloseAll()
	agent := supervisor.Agent{Ref: "noisy", Transport: "stdio", Command: script, Enabled: true}
	if _, err := c.ListTools(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	session := c.stdio[agent.Ref]
	c.mu.Unlock()
	if session == nil {
		t.Fatal("stdio session was not retained")
	}
	session.mu.Lock()
	captured := session.stderr.Len()
	session.mu.Unlock()
	if captured > 64<<10 {
		t.Fatalf("captured child stderr=%d bytes want <=65536", captured)
	}
}

func TestCloseCancelsStartingStdioSession(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	script := filepath.Join(dir, "hung-child.sh")
	body := `#!/bin/sh
echo started > "$STARTED_FILE"
while IFS= read -r line; do
  : # Deliberately never answer initialize.
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	c := New()
	agent := supervisor.Agent{Ref: "hung", Transport: "stdio", Command: script, Env: map[string]string{"STARTED_FILE": started}, Enabled: true}
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(context.Background(), agent) }()
	waitForFile(t, started)

	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close(agent.Ref) }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close blocked behind a child stuck in initialize")
	}
	select {
	case <-startDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("starting request did not stop after Close")
	}
}

func TestStdioCallCancellationClosesBlockedSession(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "blocked-call.sh")
	body := `#!/bin/sh
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}' ;;
  *'"method":"notifications/initialized"'*) ;;
  *'"method":"tools/list"'*) : ;; # Never answer the call.
 esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	c := New()
	defer c.CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.ListTools(ctx, supervisor.Agent{Ref: "blocked", Transport: "stdio", Command: script, Enabled: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ListTools error=%v want deadline exceeded", err)
	}
	if status := c.Status("blocked"); status.Running {
		t.Fatalf("blocked session still running after cancellation: %+v", status)
	}
}

func TestCloseGenerationRejectsSessionStartingFromStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "child.sh")
	body := `#!/bin/sh
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}' ;;
  *'"method":"notifications/initialized"'*) ;;
 esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	c := New()
	validatorEntered := make(chan struct{})
	releaseValidator := make(chan struct{})
	c.SetAgentValidator(func(supervisor.Agent) bool {
		close(validatorEntered)
		<-releaseValidator
		return true
	})
	agent := supervisor.Agent{Ref: "stale", Transport: "stdio", Command: script, Enabled: true}
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(context.Background(), agent) }()
	<-validatorEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close(agent.Ref) }()
	time.Sleep(10 * time.Millisecond)
	close(releaseValidator)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; err == nil {
		t.Fatal("stale start unexpectedly published a session")
	}
	if status := c.Status(agent.Ref); status.Running {
		t.Fatalf("stale session remained active: %+v", status)
	}
}

func TestCloseRejectsWaiterFromInvalidatedGeneration(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "starts")
	script := filepath.Join(dir, "generation-child.sh")
	body := `#!/bin/sh
echo x >> "$COUNT_FILE"
instance=$(wc -l < "$COUNT_FILE" | tr -d ' ')
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*)
    if [ "$instance" -gt 1 ]; then
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}'
    fi ;;
  *'"method":"notifications/initialized"'*) ;;
 esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	c := New()
	agent := supervisor.Agent{Ref: "generation", Transport: "stdio", Command: script, Env: map[string]string{"COUNT_FILE": counter}, Enabled: true}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- c.Start(context.Background(), agent) }()
	waitForFile(t, counter)
	go func() { secondDone <- c.Start(context.Background(), agent) }()
	time.Sleep(20 * time.Millisecond)
	if err := c.Close(agent.Ref); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err == nil {
		t.Fatal("first invalidated start unexpectedly succeeded")
	}
	select {
	case err := <-secondDone:
		if err == nil {
			t.Fatal("waiter from invalidated generation restarted the child")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waiter from invalidated generation did not stop")
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if starts := strings.Count(string(data), "x"); starts != 1 {
		t.Fatalf("child starts=%d want 1; stale waiter restarted it", starts)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
