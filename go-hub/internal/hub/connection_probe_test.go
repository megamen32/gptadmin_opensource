package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Fast units (expected <1s, maximum 10s): a modern discovery probe must
// negotiate the managed route's actual legacy initialize/SID contract.
// Fixtures prove routing/auth logic, not a deployed client or upstream.
func managedProbeFixture(t *testing.T) *Server {
	t.Helper()
	s := New(Config{PublicOrigin: "https://hub.example"})
	t.Cleanup(func() { _ = s.Close() })
	s.agents["shell:probe"] = &Agent{AgentID: "shell:probe", Kind: "virtual_shell", Transport: "heartbeat", Status: "online", Meta: map[string]any{
		"mcp_agents": []any{map[string]any{"ref": "private", "enabled": true, "transport": "streamable-http", "owner_profile_id": "probe-owner", "connection_scopes_version": 1, "connection_instance": "generation"}},
	}}
	for _, owner := range []string{"probe-owner", "other-owner"} {
		s.accessProfiles[owner] = AccessProfile{ID: owner, AccessMode: accessModeFull, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}, Version: 1}
		s.managedMCP[owner] = managedMCPToken{ID: owner, ClientID: owner, TokenKind: configuredMCPBearerTokenKind, TokenDigest: configuredMCPBearerDigest(owner), ProfileID: owner, Scope: "gptadmin.read gptadmin.exec", AccessMode: accessModeFull}
	}
	return s
}

func managedProbeRequest(s *Server, owner, method, routingMethod string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "server-discover-probe-1", "method": method, "params": map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": mcpProtocolVersion}}})
	r := httptest.NewRequest(http.MethodPost, "/connection/private/mcp", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+owner)
	r.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	r.Header.Set("Mcp-Method", routingMethod)
	w := httptest.NewRecorder()
	s.connectionMCPEndpoint(w, r)
	return w
}

func TestConnectionProbeReturnsLegacyVerdictWithoutSession(t *testing.T) {
	s := managedProbeFixture(t)
	w := managedProbeRequest(s, "probe-owner", "server/discover", "server/discover")
	var body struct {
		ID    string `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || body.ID != "server-discover-probe-1" || body.Error.Code != -32601 || len(body.Result) != 0 || w.Header().Get("Mcp-Session-Id") != "" {
		t.Fatalf("probe did not negotiate legacy initialization: status=%d body=%s", w.Code, w.Body.String())
	}
	if len(s.connectionSessions) != 0 || len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("probe allocated a session or upstream task")
	}
}

func TestConnectionProbeDoesNotForwardToBusyPeer(t *testing.T) {
	s := managedProbeFixture(t)
	peer := &nodeConn{name: "shell:probe", managedHTTP: true, pending: map[string]chan nodeConnResult{}}
	for n := 0; n < managedNodePendingLimit; n++ {
		peer.pending[string(rune('a'+n))] = make(chan nodeConnResult, 1)
	}
	s.nodeConns.put(peer)
	t.Cleanup(func() { s.nodeConns.mu.Lock(); delete(s.nodeConns.conns, peer.name); s.nodeConns.mu.Unlock() })
	w := managedProbeRequest(s, "probe-owner", "server/discover", "server/discover")
	if w.Code != http.StatusOK || len(peer.pending) != managedNodePendingLimit {
		t.Fatal("metadata probe entered peer capacity or changed foreign requests", w.Code)
	}
}

func TestConnectionProbeKeepsOwnerAndRoutingFences(t *testing.T) {
	s := managedProbeFixture(t)
	if w := managedProbeRequest(s, "other-owner", "server/discover", "server/discover"); w.Code != http.StatusForbidden {
		t.Fatal("foreign owner accepted", w.Code)
	}
	profile := s.accessProfiles["probe-owner"]
	profile.AllowedTargets = []string{"other-target"}
	s.accessProfiles[profile.ID] = profile
	if w := managedProbeRequest(s, "probe-owner", "server/discover", "server/discover"); w.Code != http.StatusForbidden {
		t.Fatal("matching owner bypassed target policy", w.Code)
	}
	profile.AllowedTargets = []string{"*"}
	s.accessProfiles[profile.ID] = profile
	if w := managedProbeRequest(s, "probe-owner", "server/discover", "tools/list"); w.Code != http.StatusBadRequest {
		t.Fatal("probe bypassed routing-header validation", w.Code)
	}
	if w := managedProbeRequest(s, "probe-owner", "tools/list", "tools/list"); w.Code != http.StatusNotFound {
		t.Fatal("legacy tools lookup no longer requires session", w.Code)
	}
	if len(s.connectionSessions) != 0 || len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("denied/probe request allocated state")
	}
}
