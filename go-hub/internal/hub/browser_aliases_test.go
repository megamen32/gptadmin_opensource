package hub

import (
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
	s.agents["shell:node-44"].Meta = map[string]any{"mcp_agents": []any{}}
	if len(s.browserMCPAliasesLocked()) != 0 {
		t.Fatal("alias survives removal of physical MCP")
	}
}
