package hub

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestChildCatalogFailureRetainsLastSuccessfulRead(t *testing.T) {
	var unavailable atomic.Bool
	s, child, _ := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		writeJSON(w, 200, map[string]any{"status": "completed", "response": map[string]any{"tools": []any{map[string]any{"name": "retained_tool", "inputSchema": map[string]any{"type": "object"}}}}})
	})
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	rows, _ := s.childSearchCatalogForRequest(r, child.AgentID)
	if len(rows) != 1 {
		t.Fatal("successful catalog missing")
	}
	cache := s.childSearchCache()
	key := childSearchCacheKey(r, child)
	cache.mu.Lock()
	success := time.Now().Add(-10 * time.Minute)
	cache.entries[key].loadedAt = success
	cache.mu.Unlock()
	unavailable.Store(true)
	rows, coverage := s.childSearchCatalogForRequest(r, child.AgentID)
	if len(rows) != 1 || len(stringSlice(coverage["stale"])) != 1 {
		t.Fatalf("lost fallback: %v %v", rows, coverage)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	restored := s.loadSearchSnapshot("child")[key]
	if restored == nil || restored.err == "" || !restored.loadedAt.Equal(success) {
		t.Fatal("restart lost stale catalog state")
	}
	if !cache.entries[key].loadedAt.Equal(success) {
		t.Fatal("failed refresh overwrote last successful load time")
	}
	if !childSearchEntryFresh(cache.entries[key], childSearchFingerprint(s, child)) {
		t.Fatal("failure retry backoff missing")
	}
}

func TestColdChildCatalogFailureIsNotComplete(t *testing.T) {
	s, child, _ := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	rows, coverage := s.childSearchCatalogForRequest(r, child.AgentID)
	if len(rows) != 0 || truthy(coverage["complete"]) {
		t.Fatalf("cold failure advertised complete catalog: %v", coverage)
	}
}
