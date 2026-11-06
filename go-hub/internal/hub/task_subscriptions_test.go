package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMCPTaskSubscriptionStreamsCompletion(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	s.mu.Lock()
	s.shellJobs["stream-task"] = &shellJob{ID: "stream-task", Server: "fixture", CreatedAt: nowFloat(), Status: "running"}
	s.mu.Unlock()
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := `{"jsonrpc":"2.0","id":"listen-1","method":"subscriptions/listen","params":{"notifications":{"taskIds":["stream-task"]},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp", bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "subscriptions/listen")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("not an event stream: %s", response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	read := func() map[string]any {
		t.Helper()
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var result map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
		}
		t.Fatalf("stream ended: %v", scanner.Err())
		return nil
	}
	ack := read()
	if ack["method"] != "notifications/subscriptions/acknowledged" || mapValue(mapValue(ack["params"])["_meta"])["io.modelcontextprotocol/subscriptionId"] != "listen-1" {
		t.Fatalf("bad acknowledgement: %v", ack)
	}
	s.mu.Lock()
	s.shellJobs["stream-task"].Status = "completed"
	s.shellJobs["stream-task"].DoneAt = nowFloat()
	s.shellJobs["stream-task"].Result = map[string]any{"stdout": "stream-result"}
	s.mu.Unlock()
	for {
		event := read()
		params := mapValue(event["params"])
		if event["method"] == "notifications/tasks" && params["status"] == "completed" {
			if !strings.Contains(string(mustJSON(t, params)), "stream-result") {
				t.Fatalf("missing result: %v", params)
			}
			break
		}
	}
}

func TestMCPTaskSubscriptionRejectsForeignReceipt(t *testing.T) {
	profile := AccessProfile{ID: "limited", Version: 7, AccessMode: accessModeFull}
	s := &Server{accessProfiles: map[string]AccessProfile{"limited": profile}, shellJobs: map[string]*shellJob{
		"private-task": {ID: "private-task", Server: "fixture", Status: "completed", Caller: &taskCaller{ClientID: "owner", TokenID: "owner-token", Subject: "owner", ProfileID: "limited", ProfileVersion: 7}, Result: map[string]any{"sentinel": "private-result"}},
	}, relayJobs: map[string]*relayJob{}}
	req := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{"client_id": "foreign", "jti": "foreign-token", "sub": "foreign"})
	req = requestWithAccessProfile(req, profile)
	body := map[string]any{"id": "listen", "params": map[string]any{"notifications": map[string]any{"taskIds": []any{"private-task"}}, "_meta": map[string]any{"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}}}}
	rec := httptest.NewRecorder()
	s.serveTaskSubscription(rec, req, body, "")
	if strings.Contains(rec.Body.String(), "private-result") || strings.Contains(rec.Body.String(), "notifications/subscriptions/acknowledged") || !strings.Contains(rec.Body.String(), "unknown taskId") {
		t.Fatalf("foreign receipt leaked or was acknowledged: %s", rec.Body.String())
	}
}

func TestPinnedMCPTaskRejectsForeignReadAndCancel(t *testing.T) {
	profile := AccessProfile{ID: "limited", Version: 7, AccessMode: accessModeFull}
	s := &Server{accessProfiles: map[string]AccessProfile{"limited": profile}, shellJobs: map[string]*shellJob{
		"private-task": {ID: "private-task", Server: "fixture", Status: "running", Caller: &taskCaller{ClientID: "owner", TokenID: "owner-token", Subject: "owner", ProfileID: "limited", ProfileVersion: 7}},
	}, relayJobs: map[string]*relayJob{}}
	req := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/server/fixture/mcp", nil), map[string]any{"client_id": "foreign", "jti": "foreign-token", "sub": "foreign"})
	req = requestWithAccessProfile(req, profile)
	params := map[string]any{"taskId": "private-task", "_meta": map[string]any{"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}}}
	agent := Agent{AgentID: "shell:fixture", Kind: "shell"}
	for _, method := range []string{"tasks/get", "tasks/cancel"} {
		result, rpcErr, _ := s.agentMCPJSONRPC(req, agent, map[string]any{"method": method, "params": params})
		if rpcErr == nil || result != nil {
			t.Fatalf("foreign %s accepted: result=%v err=%v", method, result, rpcErr)
		}
	}
	if s.shellJobs["private-task"].Status != "running" {
		t.Fatal("foreign cancellation changed task")
	}
}

func TestChildTaskOwnerMatchesExactChild(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	s.mu.Lock()
	s.shellJobs["child-task"] = &shellJob{ID: "child-task", Server: "fixture", ToolName: "mcp_call", Arguments: map[string]any{"ref": "GrepMesh"}, Status: "queued", CreatedAt: nowFloat()}
	s.mu.Unlock()
	if _, err := s.mcpTaskGet("child-task", "mcp:shell:fixture:GrepMesh"); err != nil {
		t.Fatalf("own child task inaccessible: %v", err)
	}
	if _, err := s.mcpTaskGet("child-task", "mcp:shell:fixture:Other"); err == nil {
		t.Fatal("different child could inspect task")
	}
	if _, err := s.mcpTaskCancel("child-task", "mcp:shell:fixture:Other"); err == nil {
		t.Fatal("different child could cancel task")
	}
	if _, err := s.mcpTaskCancel("child-task", "mcp:shell:fixture:GrepMesh"); err != nil {
		t.Fatalf("own child cannot cancel task: %v", err)
	}
}
