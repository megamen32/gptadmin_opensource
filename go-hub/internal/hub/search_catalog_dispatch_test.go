package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchBootstrapBoundsQueuedRefreshes(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	cache := s.childSearchCache()
	for n := 0; n < childSearchConcurrency; n++ {
		cache.entries[fmt.Sprint(n)] = &childSearchSchemaEntry{refreshing: true, done: make(chan struct{})}
	}
	r := httptest.NewRequest("POST", "/mcp", nil)
	for n := 0; n < 200; n++ {
		if done := cache.startChildSearchRefresh(s, r, child, fmt.Sprintf("pending-%d", n), "generation"); done != nil {
			t.Fatal("overflow spawned queued refresh")
		}
	}
	if len(cache.entries) != childSearchConcurrency {
		t.Fatalf("overflow allocated entries: %d", len(cache.entries))
	}
}

func TestSearchGenerationChangesDuringInflightRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var count atomic.Int32
	s, child, _ := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		name := "new_generation_tool"
		if count.Add(1) == 1 {
			close(started)
			<-release
			name = "old_generation_tool"
		}
		writeJSON(w, 200, map[string]any{"status": "completed", "response": map[string]any{"tools": []any{map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}}}}})
	})
	setGeneration := func(value string) {
		s.mu.Lock()
		descriptors := sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])
		mapValue(descriptors[0])["catalog_digest"] = value
		s.mu.Unlock()
	}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	setGeneration("one")
	cache := s.childSearchCache()
	cache.subscribe(s, r)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("bootstrap not started")
	}
	setGeneration("two")
	cache.refreshParent(s, "shell:peer")
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	key := childSearchCacheKey(r, child)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		e := cache.entries[key]
		ready := e != nil && !e.refreshing && len(e.tools) == 1 && firstString(e.tools[0], "name") == "new_generation_tool"
		cache.mu.Unlock()
		if ready {
			if count.Load() != 2 {
				t.Fatalf("refresh count=%d", count.Load())
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("generation race failed to refill; calls=%d", count.Load())
}

func TestSearchBackgroundRejectsNewRestrictedBinding(t *testing.T) {
	s := New(Config{CtlToken: "ctl", ConfigDir: t.TempDir(), OAuthClientSecret: "test-only-search-secret"})
	defer s.Close()
	issued := accessProfileTestRequest(t, s, http.MethodPost, "/admin/api/mcp/issue-token", map[string]any{"client_id": "search-subscriber", "ttl_days": 1}, nil)
	if issued.Code != 200 {
		t.Fatalf("issue status=%d", issued.Code)
	}
	var token struct {
		ID    string `json:"token_id"`
		Value string `json:"access_token"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+token.Value)
	if !s.mcpAuth(httptest.NewRecorder(), r) {
		t.Fatal("initial auth failed")
	}
	key := childSearchScope(r)
	cache := s.childSearchCache()
	cache.subscribers = map[string]searchCatalogSubscriber{key: {request: r, seen: time.Now()}}
	profile := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/access-profiles/restricted", map[string]any{"id": "restricted", "access_mode": "full", "allowed_targets": []string{"hub"}, "allowed_tools": []string{"search"}}, map[string]string{"If-Match": "*"})
	if profile.Code != 200 {
		t.Fatalf("profile status=%d", profile.Code)
	}
	bound := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/client-bindings/"+token.ID, map[string]string{"profile_id": "restricted"}, nil)
	if bound.Code != 200 {
		t.Fatalf("bind status=%d", bound.Code)
	}
	cache.refreshParent(s, "")
	cache.mu.Lock()
	_, stillSubscribed := cache.subscribers[key]
	cache.mu.Unlock()
	if stillSubscribed {
		t.Fatal("captured unbound authority reused after restricted binding")
	}
}

func TestCatalogDoesNotSilentlyDropToolsAfterTwoHundred(t *testing.T) {
	tools := make([]map[string]any, 900)
	for i := range tools {
		tools[i] = map[string]any{"name": fmt.Sprintf("capability_%03d", i), "description": "metadata", "inputSchema": map[string]any{"type": "object"}}
	}
	got := boundedSearchChildTools(tools)
	if len(got) != len(tools) || firstString(got[len(got)-1], "name") != "capability_899" {
		t.Fatalf("catalog lost tools: retained=%d expected=%d", len(got), len(tools))
	}
}

func TestCatalogRetainsMetadataWhenSchemaExceedsSearchBudget(t *testing.T) {
	got := boundedSearchChildTools([]map[string]any{{"name": "large_schema_capability", "description": "discover this tool", "inputSchema": map[string]any{"type": "object", "description": strings.Repeat("x", childSearchSchemaLimit+1)}}})
	if len(got) != 1 || firstString(got[0], "name") != "large_schema_capability" {
		t.Fatal("large schema hid tool metadata")
	}
	encoded, _ := json.Marshal(got[0]["inputSchema"])
	if len(encoded) > childSearchSchemaLimit {
		t.Fatal("schema output budget exceeded")
	}
	if !truthy(mapValue(got[0]["inputSchema"])["schema_truncated"]) {
		t.Fatal("schema summary not marked incomplete")
	}
}

func TestSearchMetadataGenerationSurvivesRuntimeRestartNotConfigChange(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	s.mu.Lock()
	parent := s.agents["shell:fixture"]
	descriptor := mapValue(sliceValue(parent.Meta["mcp_agents"])[0])
	descriptor["catalog_config_digest"] = "same-definition"
	descriptor["catalog_digest"] = "same-tools"
	descriptor["connection_instance"] = "runtime-one"
	before := childMCPAgents(*parent)[0]
	s.mu.Unlock()
	fingerprint := childSearchFingerprint(s, before)
	entry := &childSearchSchemaEntry{fingerprint: fingerprint, catalogDigest: "same-tools", loadedAt: time.Now()}
	s.mu.Lock()
	descriptor["connection_instance"] = "runtime-two"
	delete(descriptor, "catalog_digest")
	after := childMCPAgents(*parent)[0]
	s.mu.Unlock()
	if childSearchFingerprint(s, after) != fingerprint || s.childSearchGenerationChanged(entry, after) {
		t.Fatal("restart discarded trusted same-definition metadata")
	}
	s.mu.Lock()
	descriptor["catalog_digest"] = "changed-tools"
	s.mu.Unlock()
	if !s.childSearchGenerationChanged(entry, after) {
		t.Fatal("changed catalog did not invalidate")
	}
	s.mu.Lock()
	descriptor["catalog_config_digest"] = "changed-credentials"
	s.mu.Unlock()
	if childSearchFingerprint(s, after) == fingerprint {
		t.Fatal("definition/credential change reused metadata")
	}
	_ = child
}
