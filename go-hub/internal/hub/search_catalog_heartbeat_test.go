package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHeartbeatGenerationRefreshAndCachedColdSearch(t *testing.T) {
	s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "completed", "response": map[string]any{"tools": []any{map[string]any{"name": "browser_tabs", "description": "Vibe MCP tabs", "inputSchema": map[string]any{"type": "object"}}}}})
	})
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	setGeneration := func(value string) {
		s.mu.Lock()
		descriptors := sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])
		mapValue(descriptors[0])["catalog_digest"] = value
		s.mu.Unlock()
	}
	setGeneration("generation-1")
	cache := s.childSearchCache()
	key := childSearchCacheKey(r, child)
	cache.entries[key] = &childSearchSchemaEntry{childID: child.AgentID, parentID: "shell:peer", fingerprint: childSearchFingerprint(s, child), loadedAt: time.Now().Add(-24 * time.Hour), catalogDigest: "generation-1", versioned: true, tools: []map[string]any{{"name": "old_tool", "description": "old tool"}}}
	cache.subscribers = map[string]searchCatalogSubscriber{childSearchScope(r): {request: r, seen: time.Now()}}
	cache.refreshParent(s, "shell:peer")
	if calls.Load() != 0 {
		t.Fatal("unchanged generation refetched despite valid persisted cache")
	}
	result := s.searchToolsForRequest(r, map[string]any{"query": "old_tool"})
	if len(outputRows(result["results"])) != 1 {
		t.Fatalf("cold restored catalog unavailable: %v", result)
	}
	setGeneration("generation-2")
	cache.refreshParent(s, "shell:peer")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		e := cache.entries[key]
		done := e != nil && !e.refreshing && len(e.tools) > 0 && firstString(e.tools[0], "name") == "browser_tabs"
		cache.mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cache.refreshParent(s, "shell:peer")
	result = s.searchToolsForRequest(r, map[string]any{"query": "browser_tabs"})
	if len(outputRows(result["results"])) != 1 || calls.Load() != 1 {
		t.Fatalf("changed heartbeat did not refresh exactly once: calls=%d result=%v", calls.Load(), result)
	}
	result = s.searchToolsForRequest(r, map[string]any{"query": "old_tool"})
	if searchContainsName(result, "old_tool") {
		t.Fatalf("removed tool survived new generation: %v", result)
	}
}
func TestPersistentCatalogCurrentACLAndCorruption(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	r := httptest.NewRequest("POST", "/mcp", nil)
	cache := s.childSearchCache()
	key := childSearchCacheKey(r, child)
	cache.entries[key] = &childSearchSchemaEntry{childID: child.AgentID, parentID: "shell:fixture", fingerprint: childSearchFingerprint(s, child), loadedAt: time.Now(), tools: []map[string]any{{"name": "sensitive_tool", "description": "private marker"}}}
	s.saveSearchSnapshot("child", cache.entries)
	childSearchCaches.Delete(s)
	loaded := s.childSearchCache()
	if loaded.entries[key] == nil {
		t.Fatal("lazy restart failed")
	}
	denied := requestWithAccessProfile(r, AccessProfile{ID: "restricted", Version: 2, AccessMode: "full", AllowedTargets: []string{"hub"}})
	result := s.searchToolsForRequest(denied, map[string]any{"query": "private marker"})
	if searchContainsName(result, "sensitive_tool") {
		t.Fatalf("revoked profile leaked snapshot: %v", result)
	}
	s.mu.Lock()
	delete(s.agents, "shell:fixture")
	s.mu.Unlock()
	result = s.searchToolsForRequest(r, map[string]any{"query": "sensitive_tool"})
	if searchContainsName(result, "sensitive_tool") {
		t.Fatalf("removed target leaked snapshot: %v", result)
	}
}

func searchContainsName(result map[string]any, name string) bool {
	for _, raw := range outputRows(result["results"]) {
		if firstString(mapValue(raw), "name") == name {
			return true
		}
	}
	return false
}
