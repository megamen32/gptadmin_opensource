package hub

import (
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestChildSearchSnapshotSurvivesRestartAndScopes(t *testing.T) {
	dir := t.TempDir()
	s, child := childSchemaFixture(t, dir)
	r := httptest.NewRequest("POST", "/mcp", nil)
	cache := s.childSearchCache()
	key := childSearchCacheKey(r, child)
	cache.entries[key] = &childSearchSchemaEntry{childID: child.AgentID, parentID: "shell:fixture", fingerprint: childSearchFingerprint(s, child), loadedAt: time.Now(), tools: []map[string]any{{"name": "browser_tabs", "description": "Vibe MCP"}}}
	s.saveSearchSnapshot("child", cache.entries)
	loaded := s.loadSearchSnapshot("child")
	if len(loaded) != 1 || firstString(loaded[key].tools[0], "name") != "browser_tabs" {
		t.Fatalf("snapshot lost: %v", loaded)
	}
	if info, err := os.Stat(s.searchSnapshotPath("child")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe snapshot permissions %v %v", info, err)
	}
	changed := requestWithAuthClaims(r, map[string]any{"sub": "other"})
	if loaded[childSearchCacheKey(changed, child)] != nil {
		t.Fatal("another principal reused catalog")
	}
}
