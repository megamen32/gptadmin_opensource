package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func createChildSlugTestServer(names ...string) *Server {
	server := New(Config{CtlToken: "ctl"})
	for _, name := range names {
		id := "shell:" + name
		server.agents[id] = &Agent{
			AgentID: id, Name: "Shell: " + name, Kind: "virtual_shell", Status: "online",
			Meta: map[string]any{"mcp_agents": []any{
				map[string]any{"ref": "AgentBrowser", "name": "Browser " + name, "enabled": true, "transport": "stdio"},
				map[string]any{"ref": "DisabledBrowser", "enabled": false, "transport": "stdio"},
			}},
		}
	}
	return server
}

func TestChildSlugQualifiedAliasPreservesUniqueLegacyURL(t *testing.T) {
	server := createChildSlugTestServer("mac-mini-2012.lan")
	for _, slug := range []string{"agentbrowser", "shell-mac-mini-2012-lan-agentbrowser"} {
		agent, ok := server.resolveExposedAgent(slug)
		if !ok || agent.AgentID != "mcp:shell:mac-mini-2012.lan:AgentBrowser" {
			t.Fatalf("slug %q resolves %q, found=%v", slug, agent.AgentID, ok)
		}
	}
	if _, ok := server.resolveExposedAgent("shell-mac-mini-2012-lan-disabledbrowser"); ok {
		t.Fatal("Disabled child is exposed")
	}
}

func TestChildSlugDuplicateReferencesPublishStableQualifiedURLs(t *testing.T) {
	server := createChildSlugTestServer("mac-mini-2012.lan", "node-44")
	expected := map[string]string{
		"mcp:shell:mac-mini-2012.lan:AgentBrowser": "shell-mac-mini-2012-lan-agentbrowser",
		"mcp:shell:node-44:AgentBrowser":         "shell-node-44-agentbrowser",
	}
	for attempt := 0; attempt < 64; attempt++ {
		server.mu.Lock()
		agents := server.publicAgentsLocked(nil)
		server.mu.Unlock()
		found := 0
		for _, agent := range agents {
			slug, ok := expected[agent.AgentID]
			if !ok {
				continue
			}
			found++
			if got := exposedAgentSlug(agent); got != slug {
				t.Fatalf("attempt %d: child %q slug=%q, want=%q", attempt, agent.AgentID, got, slug)
			}
			resolved, ok := server.resolveExposedAgent(slug)
			if !ok || resolved.AgentID != agent.AgentID {
				t.Fatalf("attempt %d: slug %q resolves %q, found=%v", attempt, slug, resolved.AgentID, ok)
			}
		}
		if found != len(expected) {
			t.Fatalf("Published children=%d, want=%d", found, len(expected))
		}
	}
	server.mu.Lock()
	delete(server.agents, "shell:node-44")
	server.mu.Unlock()
	agent, ok := server.resolveExposedAgent("shell-mac-mini-2012-lan-agentbrowser")
	if !ok || agent.AgentID != "mcp:shell:mac-mini-2012.lan:AgentBrowser" {
		t.Fatalf("Retained qualified URL resolves %q, found=%v", agent.AgentID, ok)
	}
}

func TestChildSlugQualifiedEndpointRequiresAuthentication(t *testing.T) {
	server := createChildSlugTestServer("mac-mini-2012.lan")
	request := httptest.NewRequest(http.MethodGet, "/server/shell-mac-mini-2012-lan-agentbrowser/card", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("Unauthenticated qualified endpoint status=%d, want=%d", response.Code, http.StatusUnauthorized)
	}
}
