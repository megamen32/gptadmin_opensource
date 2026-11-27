package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserAliasRoutesToPhysicalRelayWithLogicalOwner(t *testing.T) {
	dir := t.TempDir()
	spec := `{"aliases":[{"owner":"shell:MacBook-Pro-User.local","ref":"Browser","name":"Browser — Mac M1","upstream":"shell:node-44","upstream_ref":"VibeBrowserMacM1","hide_backing":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "browser-mcp-aliases.json"), []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{ConfigDir: dir}, agents: map[string]*Agent{
		"shell:MacBook-Pro-User.local": {AgentID: "shell:MacBook-Pro-User.local", Status: "online", LastSeen: 1},
		"shell:node-44": {AgentID: "shell:node-44", Status: "online", Meta: map[string]any{
			"mcp_agents": []any{map[string]any{"ref": "VibeBrowserMacM1", "enabled": true, "transport": "streamable-http", "health": map[string]any{"protocol": map[string]any{"state": "ready"}}}},
		}},
	}}
	got := s.browserMCPAliasesLocked()
	if len(got) != 1 {
		t.Fatalf("expected one browser alias, got %d", len(got))
	}
	a := got[0]
	if a.AgentID != "mcp:shell:MacBook-Pro-User.local:Browser" ||
		firstString(a.Meta, "parent_server_id") != "shell:node-44" ||
		firstString(a.Meta, "child_ref") != "VibeBrowserMacM1" ||
		!isLogicalBrowserAgent(a) {
		t.Fatalf("wrong alias route: %#v", a)
	}
	physical := childMCPAgents(*s.agents["shell:node-44"])
	if len(physical) != 1 || !backingHiddenByBrowserAliases(physical[0], got) {
		t.Fatal("physical descriptor was not hidden by verified alias")
	}
	list := hideBrowserRetargetTool([]map[string]any{{"name": "list_pages"}, {"name": "set_remote"}, {"name": "take_snapshot"}})
	if len(list) != 2 || firstString(list[0], "name") != "list_pages" || firstString(list[1], "name") != "take_snapshot" {
		t.Fatalf("retarget tool leaked: %#v", list)
	}
	// A versioned GPTAdmin Node advertises its child MCP registry separately
	// from an old standalone ShellMCP heartbeat. Missing heartbeat metadata
	// must not make a verified fixed route vanish from the logical catalog.
	s.agents["shell:node-44"].Meta = map[string]any{"mcp_agents": []any{}}
	s.agents["shell:node-44"].Status = "stale"
	fallback := s.browserMCPAliasesLocked()
	if len(fallback) != 1 || fallback[0].Status != "online" ||
		fallback[0].Transport != "streamable-http" {
		t.Fatalf("logical Browser disappeared with stale backing heartbeat: %#v", fallback)
	}
}

func TestLogicalBrowserRoutesThroughUpstreamNodePeer(t *testing.T) {
	calls := 0
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if firstString(body, "target") != "mcp:shell:node-44:VibeBrowserMacM1" {
			t.Errorf("target was not translated to the pinned upstream: %#v", body)
		}
		calls++
		switch r.URL.Path {
		case "/mcp-relay/tools":
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "completed", "response": map[string]any{"tools": []any{
					map[string]any{"name": "list_pages"},
					map[string]any{"name": "set_remote"},
				}},
			})
		case "/mcp-relay/call":
			if firstString(body, "tool") != "list_pages" {
				t.Error("wrong browser tool")
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "completed", "result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "pinned Mac M1"}},
				},
			})
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "unexpected"})
		}
	}))
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:node-44": peer.URL})
	agent := Agent{
		AgentID: "mcp:shell:MacBook-Pro-User.local:Browser",
		Kind:    "child_mcp",
		Meta: map[string]any{
			"parent_server_id":        "shell:node-44",
			"child_ref":               "VibeBrowserMacM1",
			"logical_owner_server_id": "shell:MacBook-Pro-User.local",
			"fixed_session":           true,
			"enabled":                 true,
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/server/browser/mcp", nil)
	req.Header.Set("Authorization", "Bearer token")
	schema, rpcErr := s.agentToolsListForRequest(req, agent)
	if rpcErr != nil {
		t.Fatalf("list tools failed: %#v", rpcErr)
	}
	tools, ok := mapValue(schema)["tools"].([]map[string]any)
	if !ok || len(tools) != 1 || firstString(tools[0], "name") != "list_pages" {
		t.Fatalf("unsafe or missing Browser schema: %#v", schema)
	}
	result, rpcErr := s.agentToolCall(req, agent, "list_pages", map[string]any{})
	if rpcErr != nil {
		t.Fatalf("list_pages failed: %#v", rpcErr)
	}
	if calls != 2 || len(s.shellJobs) != 0 {
		t.Fatalf("Browser fell back to legacy queue: calls=%d queued=%d", calls, len(s.shellJobs))
	}
	text := ""
	for _, part := range sliceValue(mapValue(result)["content"]) {
		text += firstString(mapValue(part), "text")
	}
	if text != "pinned Mac M1" {
		t.Fatalf("unexpected upstream response: %#v", result)
	}
	_, rpcErr = s.agentToolCall(req, agent, "set_remote", map[string]any{})
	if firstString(mapValue(rpcErr), "message") != "browser retargeting is disabled" || calls != 2 {
		t.Fatalf("set_remote was not denied before dispatch: %#v", rpcErr)
	}
}
