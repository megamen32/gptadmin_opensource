package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestChildSearchCacheProfileIsolationConcurrent(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	profiles := []AccessProfile{
		{ID: "alpha", Version: 1, AccessMode: "full", AllowedTargets: []string{child.AgentID}, AllowedTools: []string{"alpha_tool"}},
		{ID: "beta", Version: 1, AccessMode: "full", AllowedTargets: []string{child.AgentID}, AllowedTools: []string{"beta_tool"}},
	}
	cache := s.childSearchCache()
	for _, profile := range profiles {
		r := requestWithAuthClaims(requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile), map[string]any{"sub": profile.ID, "client_id": "client-" + profile.ID, "scope": "gptadmin.exec"})
		key := childSearchCacheKey(r, child)
		if _, exists := cache.entries[key]; exists {
			t.Fatalf("distinct principals shared cache key %q", key)
		}
		cache.mu.Lock()
		cache.entries[key] = &childSearchSchemaEntry{
			childID: child.AgentID, parentID: "shell:fixture", fingerprint: childSearchFingerprint(s, child), loadedAt: time.Now(),
			tools: []map[string]any{{"name": profile.AllowedTools[0], "description": "profile scoped tool", "inputSchema": map[string]any{"type": "object"}}},
		}
		cache.mu.Unlock()
	}
	results := make(chan string, 2)
	for _, profile := range profiles {
		profile := profile
		go func() {
			r := requestWithAuthClaims(requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile), map[string]any{"sub": profile.ID, "client_id": "client-" + profile.ID, "scope": "gptadmin.exec"})
			defs := childSearchToolDefinitions(s, child.AgentID)
			if len(defs) != 2 {
				// The raw lookup can enumerate authorized cache partitions; consumers
				// must apply both target and tool authorization before exposing rows.
				results <- "raw-cache-count"
				return
			}
			visible := toolsForRequest(r, child.AgentID, defs)
			if len(visible) != 1 {
				results <- "wrong-visible-count"
				return
			}
			results <- firstString(visible[0], "name")
		}()
	}
	got := map[string]bool{<-results: true, <-results: true}
	if !got["alpha_tool"] || !got["beta_tool"] {
		t.Fatalf("profile searches crossed or hid cached schemas: %v", got)
	}
}

func TestConcurrentSearchUsesOnlyEachPrincipalChildSchemaPartition(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	profiles := []AccessProfile{
		{ID: "search-alpha", Version: 3, AccessMode: "full", AllowedTargets: []string{child.AgentID}, AllowedTools: []string{"search", "alpha_tool"}},
		{ID: "search-beta", Version: 3, AccessMode: "full", AllowedTargets: []string{child.AgentID}, AllowedTools: []string{"search", "beta_tool"}},
	}
	cache := s.childSearchCache()
	requests := make([]*http.Request, 0, len(profiles))
	for _, profile := range profiles {
		r := requestWithAuthClaims(requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile), map[string]any{"sub": profile.ID, "client_id": profile.ID, "scope": "gptadmin.read gptadmin.exec"})
		requests = append(requests, r)
		s.mu.Lock()
		var visibleChild Agent
		for _, candidate := range s.publicAgentsLocked(r) {
			if candidate.AgentID == child.AgentID {
				visibleChild = candidate
				break
			}
		}
		s.mu.Unlock()
		if visibleChild.AgentID == "" {
			t.Fatalf("fixture child is not visible to %s", profile.ID)
		}
		name := profile.AllowedTools[1]
		key := childSearchCacheKey(r, visibleChild)
		cache.entries[key] = &childSearchSchemaEntry{childID: visibleChild.AgentID, parentID: "shell:fixture", fingerprint: childSearchFingerprint(s, visibleChild), loadedAt: time.Now(), tools: []map[string]any{{"name": name, "description": "unique quartz catalog phrase", "inputSchema": map[string]any{"type": "object"}}}}
	}
	start := make(chan struct{})
	results := make(chan string, len(requests))
	var wg sync.WaitGroup
	for _, r := range requests {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result := s.searchToolsForRequest(r, map[string]any{"query": "quartz catalog phrase"})
			hits := outputRows(result["results"])
			if len(hits) != 1 {
				encoded, _ := json.Marshal(result)
				results <- "wrong-hit-count:" + string(encoded)
				return
			}
			results <- firstString(mapValue(hits[0]), "name")
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for name := range results {
		seen[name] = true
	}
	if !seen["alpha_tool"] || !seen["beta_tool"] || len(seen) != 2 {
		t.Fatalf("parallel FTS searches mixed principal schema caches: %v", seen)
	}
}

func TestChildSearchCacheDescriptorChangeAndDisableInvalidation(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	r := httptest.NewRequest("POST", "/mcp", nil)
	cache := s.childSearchCache()
	key := childSearchCacheKey(r, child)
	oldFingerprint := childSearchFingerprint(s, child)
	cache.entries[key] = &childSearchSchemaEntry{childID: child.AgentID, parentID: "shell:fixture", fingerprint: oldFingerprint, loadedAt: time.Now(), tools: []map[string]any{{"name": "old_tool"}}}

	// A changed descriptor produces a new fingerprint and the stale schema is
	// discarded before it can become a search row.
	s.mu.Lock()
	parent := s.agents["shell:fixture"]
	parent.Meta["mcp_agents"] = []any{map[string]any{"ref": "fixture-child", "enabled": true, "transport": "stdio", "connection_instance": "generation-2", "connection_scopes_version": 2}}
	s.mu.Unlock()
	changed := childMCPAgents(*parent)[0]
	if childSearchFingerprint(s, changed) == oldFingerprint {
		t.Fatal("descriptor change did not change schema cache fingerprint")
	}
	s.invalidateChildSearchSchemas("shell:fixture")
	if got := childSearchToolDefinitions(s, child.AgentID); len(got) != 0 {
		t.Fatalf("parent invalidation retained stale schemas: %v", got)
	}

	// A disabled descriptor is omitted by childMCPAgents and the request cache
	// reconciliation removes it from the active profile partition.
	s.mu.Lock()
	parent.Meta["mcp_agents"] = []any{map[string]any{"ref": "fixture-child", "enabled": false}}
	s.mu.Unlock()
	s.childSearchCatalogForRequest(r, "")
	if got := childSearchToolDefinitions(s, child.AgentID); len(got) != 0 {
		t.Fatalf("disabled child schema remained searchable: %v", got)
	}
}

func TestChildSearchFingerprintIgnoresHeartbeatButTracksStableGeneration(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	before := childSearchFingerprint(s, child)
	s.mu.Lock()
	parent := s.agents["shell:fixture"]
	descriptor := mapValue(sliceValue(parent.Meta["mcp_agents"])[0])
	descriptor["health"] = map[string]any{"last_checked_at": "2026-10-22T10:00:00Z", "last_handshake_at": "2026-10-22T10:00:00Z", "state": "ok"}
	parent.Meta["mcp_agents"] = []any{descriptor}
	s.mu.Unlock()
	if after := childSearchFingerprint(s, child); after != before {
		t.Fatal("health heartbeat invalidated the cached schema generation")
	}
	s.mu.Lock()
	descriptor["connection_instance"] = "replacement-generation"
	parent.Meta["mcp_agents"] = []any{descriptor}
	s.mu.Unlock()
	if after := childSearchFingerprint(s, child); after == before {
		t.Fatal("connection generation change did not invalidate the cached schema generation")
	}
}

func TestSearchFailedChildRefreshIsReportedAsUnreachable(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	r := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{
		"sub": "search-failed-child", "client_id": "search-failed-child",
		"scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull,
	})
	cache := s.childSearchCache()
	key := childSearchCacheKey(r, child)
	cache.mu.Lock()
	cache.entries[key] = &childSearchSchemaEntry{
		childID: child.AgentID, parentID: "shell:fixture",
		fingerprint: childSearchFingerprint(s, child), loadedAt: time.Now(),
		err: "upstream did not return a successful tools/list response",
	}
	cache.mu.Unlock()

	result := s.searchToolsForRequest(r, map[string]any{"query": "fixture tool"})
	coverage := mapValue(result["coverage"])
	if !truthy(coverage["complete"]) || firstString(coverage, "status") != "complete" {
		t.Fatalf("unreachable child made reachable search coverage incomplete: %#v", coverage)
	}
	if intFromAny(coverage["failed"]) != 1 || intFromAny(coverage["pending"]) != 0 {
		t.Fatalf("failed child counts were not preserved: %#v", coverage)
	}
	if !containsString(stringSlice(coverage["unreachable"]), child.AgentID) {
		t.Fatalf("failed child was omitted from coverage.unreachable: %#v", coverage)
	}
	if stringSlice(coverage["missing"]) != nil && len(stringSlice(coverage["missing"])) != 0 {
		t.Fatalf("unreachable child was also reported as missing: %#v", coverage)
	}
	if _, ok := coverage["retry_hint"]; ok {
		t.Fatalf("completed failed refresh got a retry hint despite no pending work: %#v", coverage)
	}
}
