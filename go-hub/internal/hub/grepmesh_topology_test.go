package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGrepMeshTopologyNodeIsControlOnlyAndUsesRoutablePeerURL(t *testing.T) {
	agent := Agent{
		AgentID:      "server-88",
		Kind:         "grepmesh",
		Status:       "online",
		LastSeen:     42,
		Capabilities: []string{"search_text", "read_text"},
		Meta: map[string]any{
			"host_id":          "server-88",
			"service":          "grepmesh-mcp",
			"local_url":        "http://127.0.0.1:9419/mcp",
			"advertise_url":    "https://203.0.113.10:9419/mcp",
			"version":          "0.1.0",
			"protocol_version": "2025-11-25",
			"generation":       "43",
			"roots":            []any{"home", "opt", "etc"},
			"Authorization":    "must-not-leak",
		},
	}

	node, ok := grepMeshTopologyNode(agent)
	if !ok {
		t.Fatal("expected GrepMesh agent to produce a topology node")
	}
	if got := node["host_id"]; got != "server-88" {
		t.Fatalf("host_id = %#v", got)
	}
	if got := node["local_url"]; got != "http://127.0.0.1:9419/mcp" {
		t.Fatalf("local_url = %#v", got)
	}
	if got := node["peer_url"]; got != "https://203.0.113.10:9419/mcp" {
		t.Fatalf("peer_url = %#v", got)
	}
	if got := node["generation"]; got != int64(43) {
		t.Fatalf("generation = %#v", got)
	}
	if _, leaked := node["Authorization"]; leaked {
		t.Fatal("topology projection leaked credential-like metadata")
	}
	if _, executable := node["tools"]; executable {
		t.Fatal("topology projection exposed executable tool metadata")
	}
}

func TestGrepMeshTopologyNodeIgnoresOtherAgents(t *testing.T) {
	if _, ok := grepMeshTopologyNode(Agent{AgentID: "shell:server-100", Kind: "virtual_shell"}); ok {
		t.Fatal("non-GrepMesh agent was projected")
	}
}

func TestGrepMeshTopologyEndpointIsReadOnlyAndFiltered(t *testing.T) {
	s := New(Config{CtlToken: "ctl"})
	s.mu.Lock()
	s.agents["server-88"] = &Agent{
		AgentID:      "server-88",
		Kind:         "grepmesh",
		Status:       "online",
		Capabilities: []string{"search_text", "read_text"},
		Meta: map[string]any{
			"host_id":       "server-88",
			"local_url":     "http://127.0.0.1:9419/mcp",
			"peer_url":      "https://203.0.113.10:9419/mcp",
			"generation":    7,
			"Authorization": "do-not-return",
		},
	}
	s.agents["shell:server-100"] = &Agent{AgentID: "shell:server-100", Kind: "virtual_shell"}
	s.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/mcp-relay/grepmesh", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Nodes) != 1 || body.Nodes[0]["host_id"] != "server-88" {
		t.Fatalf("nodes = %#v", body.Nodes)
	}
	if _, ok := body.Nodes[0]["Authorization"]; ok {
		t.Fatal("credential-like metadata leaked")
	}
	if _, ok := body.Nodes[0]["tools"]; ok {
		t.Fatal("executable tool metadata leaked")
	}
}
