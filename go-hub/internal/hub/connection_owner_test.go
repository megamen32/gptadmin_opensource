package hub

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// Fast unit: personal child authority must fail before dispatch. These fixtures
// exercise policy only and do not claim a working remote service or consumer.
func ownerBoundChild() Agent {
	return Agent{AgentID: "mcp:shell:test:private", Kind: "child_mcp", Transport: "streamable-http", Meta: map[string]any{
		"parent_server_id": "shell:test", "child_ref": "private", "enabled": true, "owner_profile_id": "zcode-owner",
	}}
}

func TestConnectionOwnerRejectsOtherWideAndUnboundProfiles(t *testing.T) {
	s := &Server{}
	child := ownerBoundChild()
	for _, r := range []*http.Request{nil, connectionTestRequest("foreign", "other-owner", 1), connectionTestRequest("unbound", "", 1)} {
		result, rpcErr, _ := s.agentMCPJSONRPC(r, child, map[string]any{"method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26"}})
		if rpcErr == nil || result != nil {
			t.Fatal("personal MCP initialization accepted foreign owner")
		}
		result, rpcErr = s.agentResourcesList(r, child)
		if rpcErr == nil || result != nil {
			t.Fatal("resource capability fallback bypassed personal owner")
		}
		receipt := s.childSchemaForRequest(r, child)
		if firstString(receipt, "status") != "failed" || firstString(receipt, "job_id") != "" {
			t.Fatal("foreign schema allocated receipt")
		}
	}
	matching := connectionTestRequest("same", "zcode-owner", 1)
	if _, rpcErr, _ := s.agentMCPJSONRPC(matching, child, map[string]any{"method": "initialize"}); rpcErr == nil {
		t.Fatal("personal ref accepted legacy unscoped owner")
	}
	matching = matching.WithContext(context.WithValue(matching.Context(), connectionContextKey{}, connectionScope{Target: child.AgentID, Key: "trusted", Instance: "generation"}))
	if _, rpcErr, _ := s.agentMCPJSONRPC(matching, child, map[string]any{"method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26"}}); rpcErr != nil {
		t.Fatal("matching personal owner refused", rpcErr)
	}
}

func TestConnectionOwnerParentShellCannotBypassPrivateRef(t *testing.T) {
	s := &Server{agents: map[string]*Agent{"shell:test": {AgentID: "shell:test", Meta: map[string]any{"mcp_agents": []any{
		map[string]any{"ref": "private", "enabled": true, "owner_profile_id": "zcode-owner"},
	}}}}}
	foreign := connectionTestRequest("foreign", "other-owner", 1)
	for _, tool := range []string{"mcp_tools", "mcp_call", "mcp_request", "mcp_connection_close"} {
		response, status := s.executeMCPTool(foreign, "shell:test", tool, map[string]any{"ref": "private"}, false, time.Second, "")
		if status != http.StatusForbidden || firstString(response, "job_id") != "" {
			t.Fatal("parent shell bypassed owner fence", tool, status)
		}
	}
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("foreign owner created task")
	}
}

func TestConnectionOwnerLegacyAndDisabledSemantics(t *testing.T) {
	s := &Server{}
	legacy := ownerBoundChild()
	delete(legacy.Meta, "owner_profile_id")
	unbound, _ := http.NewRequest(http.MethodPost, "http://localhost/server/test/mcp", nil)
	if _, rpcErr, _ := s.agentMCPJSONRPC(unbound, legacy, map[string]any{"method": "initialize"}); rpcErr != nil {
		t.Fatal("legacy child behavior changed", rpcErr)
	}
	private := ownerBoundChild()
	private.Meta["enabled"] = false
	if _, rpcErr, _ := s.agentMCPJSONRPC(connectionTestRequest("same", "zcode-owner", 1), private, map[string]any{"method": "initialize"}); rpcErr == nil {
		t.Fatal("disabled personal child accepted")
	}
}
