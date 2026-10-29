package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChildToolRoutesToNodeBeforeLegacyQueue(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/mcp-relay/call" || r.Header.Get("Authorization") != "Bearer token" || firstString(body, "target") != "mcp:shell:peer:grepmesh" || firstString(body, "tool") != "list_locations" {
			t.Errorf("incorrect child forwarding")
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"structuredContent": map[string]any{"ref": "grepmesh", "result": map[string]any{"ok": true}}}})
	}))
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
	agent := Agent{AgentID: "mcp:shell:peer:grepmesh", Kind: "child_mcp", Meta: map[string]any{"parent_server_id": "shell:peer", "child_ref": "grepmesh", "enabled": true}}
	r := httptest.NewRequest(http.MethodPost, "/server/grepmesh/mcp", nil)
	r.Header.Set("Authorization", "Bearer token")
	_, rpcErr := s.agentToolCall(r, agent, "list_locations", map[string]any{"hosts": "local"})
	if rpcErr != nil || calls.Load() != 1 {
		t.Fatalf("child route failed: error=%v calls=%d", rpcErr, calls.Load())
	}
	if len(s.shellJobs) != 0 {
		t.Fatal("child route allocated a legacy queue task")
	}
}

func TestChildSchemaRoutesToNodeBeforeLegacyQueue(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/mcp-relay/tools" || r.Header.Get("Authorization") != "Bearer token" || firstString(body, "target") != "mcp:shell:peer:grepmesh" {
			t.Error("incorrect schema forwarding")
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"tools": []any{map[string]any{"name": "list_locations"}}}})
	}))
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
	agent := Agent{AgentID: "mcp:shell:peer:grepmesh", Kind: "child_mcp", Meta: map[string]any{"parent_server_id": "shell:peer", "child_ref": "grepmesh", "enabled": true}}
	req := httptest.NewRequest(http.MethodPost, "/server/grepmesh/mcp", nil)
	req.Header.Set("Authorization", "Bearer token")
	result, rpcErr := s.agentToolsListForRequest(req, agent)
	if rpcErr != nil || calls.Load() != 1 || len(sliceValue(mapValue(result)["tools"])) != 1 {
		t.Fatalf("schema forwarding failed: error=%v calls=%d", rpcErr, calls.Load())
	}
	if len(s.shellJobs) != 0 {
		t.Fatal("schema allocated a legacy queue task")
	}
}

func TestChildPeerHopAcceptsOnlyLocalOwner(t *testing.T) {
	s := peerTestHub(t, "token", map[string]string{})
	identity := map[string]string{"server_id": "peer-id", "public_key": "peer-key", "fingerprint": "peer-fingerprint"}
	if err := s.RegisterLocalExecutor("peer", identity, strings.Repeat("t", 32)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.agents["shell:peer"].Meta["mcp_agents"] = []any{map[string]any{"ref": "grepmesh", "name": "grepmesh", "transport": "streamable-http", "enabled": true}}
	s.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set(nodePeerHopHeader, "1")
	recorder := httptest.NewRecorder()
	if s.forwardNodePeerCall(recorder, req, "mcp:shell:peer:grepmesh", []byte(`{"target":"mcp:shell:peer:grepmesh","tool":"list_locations"}`)) {
		t.Fatalf("local child was rejected or re-routed: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestChildPeerHopRejectsNonOwner(t *testing.T) {
	s := peerTestHub(t, "token", map[string]string{})
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set(nodePeerHopHeader, "1")
	recorder := httptest.NewRecorder()
	if !s.forwardNodePeerCall(recorder, req, "mcp:shell:foreign:child", []byte(`{"target":"mcp:shell:foreign:child","tool":"list_locations"}`)) || recorder.Code != http.StatusLoopDetected {
		t.Fatalf("foreign child was accepted: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(s.shellJobs) != 0 {
		t.Fatal("foreign child entered local queue")
	}
}
