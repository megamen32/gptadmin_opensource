package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCatalogHealthRecoveryAfterSuccessfulSchema(t *testing.T) {
	s, child, _ := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"tools": []any{map[string]any{"name": "recovered_tool", "inputSchema": map[string]any{"type": "object"}}}}})
	})
	s.mu.Lock()
	descriptor := mapValue(sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])[0])
	descriptor["health"] = map[string]any{"protocol": map[string]any{"state": "failed", "last_checked_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), "last_error": "old failure"}, "process": map[string]any{"state": "remote"}}
	s.mu.Unlock()
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer schema-test-token")
	_, coverage := s.childSearchCatalogForRequest(req, child.AgentID)
	s.mu.Lock()
	protocol := mapValue(mapValue(descriptor["health"])["protocol"])
	state := firstString(protocol, "state")
	s.mu.Unlock()
	if state != "ready" {
		t.Fatalf("successful tools/list did not recover protocol health: state=%s coverage=%v", state, coverage)
	}
	if len(stringSlice(coverage["stale"])) != 0 {
		t.Fatalf("targeted search retained stale health after recovery: %v", coverage)
	}
	rows, coverage := s.childSearchCatalogForRequest(req, "")
	if len(rows) != 1 || len(stringSlice(coverage["stale"])) != 0 || len(stringSlice(coverage["unreachable"])) != 0 || !truthy(coverage["complete"]) {
		t.Fatalf("ordinary search still reports stale health: rows=%v coverage=%v", rows, coverage)
	}
}

func TestCatalogHealthRecoveryPreservesNewFailureAndOffline(t *testing.T) {
	for _, tc := range []struct {
		name, parentStatus, process string
		newer, fail                 bool
	}{
		{name: "newer failure", newer: true},
		{name: "failure during request"},
		{name: "changed failure with old timestamp"},
		{name: "undated failure"},
		{name: "offline parent", parentStatus: "offline"},
		{name: "exited process", process: "exited"},
		{name: "failed schema", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s *Server
			s, child, _ := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "failure during request" {
					s.mu.Lock()
					d := mapValue(sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])[0])
					mapValue(mapValue(d["health"])["protocol"])["last_checked_at"] = time.Now().UTC().Format(time.RFC3339Nano)
					s.mu.Unlock()
				}
				if tc.name == "changed failure with old timestamp" {
					s.mu.Lock()
					d := mapValue(sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])[0])
					mapValue(mapValue(d["health"])["protocol"])["last_error"] = "new failure"
					s.mu.Unlock()
				}
				if tc.newer {
					s.mu.Lock()
					d := mapValue(sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])[0])
					mapValue(mapValue(d["health"])["protocol"])["last_checked_at"] = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
					s.mu.Unlock()
				}
				if tc.fail {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				writeJSON(w, 200, map[string]any{"status": "completed", "response": map[string]any{"tools": []any{map[string]any{"name": "guard_tool"}}}})
			})
			s.cfg.ConfigDir = t.TempDir()
			s.mu.Lock()
			parent := s.agents["shell:peer"]
			if tc.parentStatus != "" {
				parent.Status = tc.parentStatus
			}
			descriptor := mapValue(sliceValue(parent.Meta["mcp_agents"])[0])
			timestamp := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
			if tc.name == "undated failure" {
				timestamp = ""
			}
			descriptor["health"] = map[string]any{"protocol": map[string]any{"state": "failed", "last_checked_at": timestamp, "last_error": "failure"}, "process": map[string]any{"state": tc.process}}
			s.mu.Unlock()
			req := httptest.NewRequest("POST", "/mcp", nil)
			req.Header.Set("Authorization", "Bearer schema-test-token")
			s.childSearchCatalogForRequest(req, child.AgentID)
			s.mu.Lock()
			state := firstString(mapValue(mapValue(descriptor["health"])["protocol"]), "state")
			projected := childMCPAgents(*parent)[0].Status
			s.mu.Unlock()
			if (tc.newer || tc.fail || tc.name == "undated failure" || tc.name == "changed failure with old timestamp" || tc.name == "failure during request") && state != "failed" {
				t.Fatalf("failure overwritten: %s", state)
			}
			if tc.name == "failure during request" {
				_, coverage := s.childSearchCatalogForRequest(req, "")
				if len(stringSlice(coverage["stale"])) != 1 {
					t.Fatalf("in-flight failure hidden by warm cache: %v", coverage)
				}
				s.childSearchCache().close()
				childSearchCaches.Delete(s)
				_, coverage = s.childSearchCatalogForRequest(req, "")
				if len(stringSlice(coverage["stale"])) != 1 {
					t.Fatalf("in-flight failure hidden after snapshot reload: %v", coverage)
				}
			}
			if tc.parentStatus != "" && projected != "offline" {
				t.Fatalf("offline parent hidden: %s", projected)
			}
			if tc.process == "exited" && projected != "failed" {
				t.Fatalf("exited process hidden: %s", projected)
			}
		})
	}
}

func TestCatalogHealthOldHeartbeatAndSnapshotRecovery(t *testing.T) {
	for _, reload := range []bool{false, true} {
		name := "heartbeat"
		if reload {
			name = "snapshot_reload"
		}
		t.Run(name, func(t *testing.T) {
			s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, map[string]any{"status": "completed", "response": map[string]any{"tools": []any{map[string]any{"name": "heartbeat_recovered", "inputSchema": map[string]any{"type": "object"}}}}})
			})
			s.cfg.ConfigDir = t.TempDir()
			old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
			setFailed := func(checked string) {
				s.mu.Lock()
				d := mapValue(sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])[0])
				d["health"] = map[string]any{"protocol": map[string]any{"state": "failed", "last_checked_at": checked, "last_error": "old"}, "process": map[string]any{"state": "remote"}}
				s.mu.Unlock()
			}
			setFailed(old)
			req := httptest.NewRequest("POST", "/mcp", nil)
			req.Header.Set("Authorization", "Bearer schema-test-token")
			_, coverage := s.childSearchCatalogForRequest(req, child.AgentID)
			if !truthy(coverage["complete"]) || calls.Load() != 1 {
				t.Fatalf("initial tools/list: %v calls=%d", coverage, calls.Load())
			}
			if reload {
				s.childSearchCache().close()
				childSearchCaches.Delete(s)
				if len(s.childSearchCache().entries) == 0 {
					t.Fatal("snapshot did not reload")
				}
			}
			setFailed(old)
			rows, coverage := s.childSearchCatalogForRequest(req, "")
			if len(rows) != 1 || len(stringSlice(coverage["stale"])) != 0 || len(stringSlice(coverage["unreachable"])) != 0 || calls.Load() != 1 {
				t.Fatalf("old heartbeat returned stale cache or triggered tools/list: %v calls=%d", coverage, calls.Load())
			}
			setFailed(time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano))
			_, coverage = s.childSearchCatalogForRequest(req, "")
			if len(stringSlice(coverage["stale"])) != 1 || len(stringSlice(coverage["unreachable"])) != 1 {
				t.Fatalf("new failure hidden: %v", coverage)
			}
			setFailed(old)
			s.mu.Lock()
			s.agents["shell:peer"].Status = "offline"
			s.mu.Unlock()
			_, coverage = s.childSearchCatalogForRequest(req, "")
			if len(stringSlice(coverage["stale"])) != 1 || len(stringSlice(coverage["unreachable"])) != 1 {
				t.Fatalf("offline parent hidden by cached success: %v", coverage)
			}
			setFailed(old)
			s.mu.Lock()
			parent := s.agents["shell:peer"]
			parent.Status = "online"
			d := mapValue(sliceValue(parent.Meta["mcp_agents"])[0])
			mapValue(mapValue(d["health"])["process"])["state"] = "exited"
			s.mu.Unlock()
			_, coverage = s.childSearchCatalogForRequest(req, "")
			if len(stringSlice(coverage["stale"])) != 1 || len(stringSlice(coverage["unreachable"])) != 1 {
				t.Fatalf("exited process hidden by cached success: %v", coverage)
			}
			setFailed(old)
			cache := s.childSearchCache()
			cache.mu.Lock()
			cache.entries[childSearchCacheKey(req, child)].err = "new schema failure"
			cache.mu.Unlock()
			_, coverage = s.childSearchCatalogForRequest(req, "")
			if intFromAny(coverage["failed"]) != 1 || len(stringSlice(coverage["stale"])) != 1 {
				t.Fatalf("schema failure hidden by cached success: %v", coverage)
			}
		})
	}
}
