package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// Fast unit fixtures for adapter logic only. Native HTTP/Codex/ZCode are the
// separate deployment acceptance; no fake service is counted as that proof.
func TestAgentMemoryRESTPreservesScopeAndCallerArgs(t *testing.T) {
	var path string
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&captured)
		if r.Header.Get("Authorization") != "Bearer fixture-owned" {
			t.Error("owner credential lost")
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	s := &agentMemoryRESTSession{httpClient: server.Client(), frozenHeaders: true, agent: supervisor.Agent{URL: server.URL, Headers: map[string]string{"Authorization": "Bearer fixture-owned"}, Env: map[string]string{"AGENT_ID": "codex"}}}
	args := map[string]any{"query": "  owning project  ", "project": " gptadmin ", "limit": float64(250), "token_budget": "32.8", "format": "COMPACT"}
	result, err := s.call(context.Background(), "tools/call", map[string]any{"name": "memory_recall", "arguments": args})
	if err != nil || result["isError"] != nil || path != "/agentmemory/search" {
		t.Fatal("real REST mapping failed", err, path)
	}
	if captured["project"] != "gptadmin" || captured["query"] != "owning project" || captured["limit"] != float64(100) || captured["token_budget"] != float64(32) || captured["format"] != "compact" {
		t.Fatal("shim scope/normalization changed", captured)
	}
	if args["project"] != " gptadmin " || args["limit"] != float64(250) {
		t.Fatal("caller arguments were mutated")
	}
	_, err = s.tool(context.Background(), "memory_save", map[string]any{"content": "fixture", "project": "project-a", "agentId": "caller-spoof", "concepts": "one,project:project-a"})
	if err != nil || path != "/agentmemory/remember" || captured["agentId"] != "codex" {
		t.Fatal("configured agent attribution lost", err, captured)
	}
	concepts := captured["concepts"].([]any)
	if len(concepts) != 3 || concepts[2] != "agent:codex" {
		t.Fatal("attribution concepts changed", concepts)
	}
	if _, err := s.tool(context.Background(), "memory_save", map[string]any{"content": false, "query": "must not substitute"}); err == nil {
		t.Fatal("non-string content incorrectly fell back to query")
	}
}

func TestAgentMemoryRESTBoundsListsAndAuditWithoutFallback(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		key := "sessions"
		if r.URL.Path == "/agentmemory/memories" {
			key = "memories"
		}
		items := make([]any, 105)
		for i := range items {
			items[i] = i
		}
		_ = json.NewEncoder(w).Encode(map[string]any{key: items})
	}))
	defer server.Close()
	s := &agentMemoryRESTSession{httpClient: server.Client(), agent: supervisor.Agent{URL: server.URL}}
	result, err := s.tool(context.Background(), "memory_sessions", map[string]any{"limit": float64(3)})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(result["content"].([]any)[0].(map[string]any)["text"].(string)), &payload)
	if len(payload["sessions"].([]any)) != 3 {
		t.Fatal("sessions exceeded requested bound")
	}
	result, err = s.tool(context.Background(), "memory_export", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(result["content"].([]any)[0].(map[string]any)["text"].(string)), &payload)
	if len(payload["sessions"].([]any)) != 100 || len(payload["memories"].([]any)) != 100 || payload["synthesized"] != true {
		t.Fatal("bounded export semantics lost")
	}
	before := requests
	result, err = s.tool(context.Background(), "memory_audit", nil)
	if err != nil || requests != before || !strings.Contains(fmt.Sprint(result), "degraded") {
		t.Fatal("disabled audit contacted broken backend")
	}
	if err = s.close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.call(context.Background(), "tools/list", nil); err == nil {
		t.Fatal("closed HTTP partition accepted request")
	}
}

func TestAgentMemoryRESTRefusesRedirectAndNoSecretErrors(t *testing.T) {
	destinationCalls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls++ }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer source.Close()
	s := &agentMemoryRESTSession{httpClient: source.Client(), agent: supervisor.Agent{URL: source.URL, Headers: map[string]string{"Authorization": "Bearer private-fixture"}}}
	_, err := s.request(context.Background(), "POST", "/agentmemory/search", map[string]any{"query": "fixture"})
	if err == nil || destinationCalls != 0 || strings.Contains(err.Error(), "private-fixture") {
		t.Fatal("redirect crossed credential boundary", err)
	}
}

func TestAgentMemoryRESTSchemaAndFailureAreTruthful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tools":[{"name":"memory_sessions","inputSchema":{}},{"name":"memory_recall","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}}]}`))
	}))
	defer server.Close()
	s := &agentMemoryRESTSession{httpClient: server.Client(), agent: supervisor.Agent{URL: server.URL}}
	result, err := s.call(context.Background(), "tools/list", nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatal("invented tool names")
	}
	props := tools[1].(map[string]any)["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if props["query"] == nil || props["project"] == nil {
		t.Fatal("project schema or native query lost")
	}
	result, err = s.call(context.Background(), "tools/call", map[string]any{"name": "memory_recall", "arguments": map[string]any{"query": ""}})
	if err != nil || result["isError"] != true {
		t.Fatal("invalid recall did not retain tool error semantics")
	}
	if _, err = s.call(context.Background(), "resources/list", nil); err == nil {
		t.Fatal("invented resource support")
	}
}

func TestAgentMemoryRESTGenericBackendErrorRemainsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"backend unavailable"}],"isError":true}`))
	}))
	defer server.Close()
	s := &agentMemoryRESTSession{httpClient: server.Client(), agent: supervisor.Agent{URL: server.URL}}
	result, err := s.call(context.Background(), "tools/call", map[string]any{"name": "memory_generic", "arguments": map[string]any{}})
	if err != nil || result["isError"] != true {
		t.Fatal("backend tool failure was presented as success", err)
	}
}
