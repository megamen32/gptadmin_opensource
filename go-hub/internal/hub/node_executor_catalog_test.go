package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMembershipExecutorCatalogHTTP(t *testing.T) {
	s := membershipWriterNode(t, "writer")
	key := strings.Repeat("cd", 32)
	grant := membershipIssueGrant(t, s, "shell:peer")
	joined := membershipJoin(t, s, grant, strings.Repeat("ab", 16), key, "shell:peer", "http://peer.example")
	if joined["__status"] != http.StatusOK {
		t.Fatal(joined)
	}
	s.mu.Lock()
	s.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "mcp_agents": []any{map[string]any{"ref": "old", "enabled": true}}}}
	s.mu.Unlock()
	post := func(catalog any, include bool) int {
		body := map[string]any{"state": "joining"}
		if include {
			body["executor_catalog"] = catalog
		}
		raw, _ := json.Marshal(body)
		return membershipHTTP(s, http.MethodPost, "/admin/api/nodes/heartbeat", key, raw).Code
	}
	if code := post(nil, false); code != 200 {
		t.Fatal(code)
	}
	if len(sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])) != 1 {
		t.Fatal("legacy erased catalog")
	}
	catalog := []any{map[string]any{"ref": "VibeBrowser", "name": "Vibe Chrome", "enabled": true, "transport": "streamable-http", "catalog_digest": "new"}}
	if code := post(catalog, true); code != 200 {
		t.Fatal(code)
	}
	items := sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])
	if len(items) != 1 || firstString(mapValue(items[0]), "ref") != "VibeBrowser" {
		t.Fatalf("remote add/remove not applied: %v", items)
	}
	if code := post([]any{}, true); code != 200 {
		t.Fatal(code)
	}
	if len(sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])) != 0 {
		t.Fatal("explicit empty did not remove")
	}
}

func TestMembershipExecutorCatalogValidation(t *testing.T) {
	s := membershipWriterNode(t, "writer")
	key := strings.Repeat("cd", 32)
	grant := membershipIssueGrant(t, s, "shell:peer")
	if joined := membershipJoin(t, s, grant, strings.Repeat("ab", 16), key, "shell:peer", "http://peer.example"); joined["__status"] != 200 {
		t.Fatal(joined)
	}
	s.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "mcp_agents": []any{map[string]any{"ref": "private", "enabled": true, "owner_profile_id": "alice"}}}}
	post := func(token string, catalog any) int {
		raw, _ := json.Marshal(map[string]any{"state": "joining", "executor_catalog": catalog})
		return membershipHTTP(s, http.MethodPost, "/admin/api/nodes/heartbeat", token, raw).Code
	}
	for _, tc := range []struct {
		name    string
		catalog any
	}{
		{"null", nil},
		{"foreign parent", []any{map[string]any{"ref": "new", "parent_server_id": "shell:other", "enabled": true}}},
		{"secret fields", []any{map[string]any{"ref": "new", "url": "http://example", "enabled": true}}},
		{"owner downgrade", []any{map[string]any{"ref": "private", "enabled": true}}},
		{"owner transfer", []any{map[string]any{"ref": "private", "owner_profile_id": "bob", "enabled": true}}},
		{"unknown owner", []any{map[string]any{"ref": "new", "owner_profile_id": "alice", "enabled": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status := post(key, tc.catalog); status < 400 {
				t.Fatalf("accepted %d", status)
			}
		})
	}
	if status := post("bad", []any{}); status != 401 {
		t.Fatalf("unauthenticated %d", status)
	}
	items := []any{map[string]any{"ref": "private", "enabled": true, "owner_profile_id": "alice", "transport": "streamable-http"}}
	if status := post(key, items); status != 200 {
		t.Fatalf("exact owner rejected %d", status)
	}
	child := childMCPAgents(*s.agents["shell:peer"])[0]
	if agentOwnerProfileAllowed(nil, child) {
		t.Fatal("private owner ACL widened")
	}
	if status := post(key, []any{}); status != 200 {
		t.Fatal(status)
	}
	if status := post(key, []any{map[string]any{"ref": "private", "enabled": true}}); status != 403 {
		t.Fatalf("removed private resurrected public: %d", status)
	}
	s.agents["shell:peer"].Meta["approved"] = false
	if status := post(key, []any{}); status != 403 {
		t.Fatalf("unapproved: %d", status)
	}
	s.agents["shell:peer"].Meta["approved"] = true
	many := make([]any, nodeExecutorCatalogLimit+1)
	for i := range many {
		many[i] = map[string]any{"ref": fmt.Sprintf("ref%d", i), "enabled": true}
	}
	if status := post(key, many); status != 400 {
		t.Fatalf("count limit: %d", status)
	}
	raw := []byte("{\"state\":\"joining\",\"padding\":\"" + strings.Repeat("x", nodeHeartbeatBodyLimit) + "\"}")
	if status := membershipHTTP(s, http.MethodPost, "/admin/api/nodes/heartbeat", key, raw).Code; status != 413 {
		t.Fatalf("body limit: %d", status)
	}
	s.membershipMu.Lock()
	s.membership.Nodes[strings.Repeat("ab", 16)].State = nodeStateDisabled
	s.membershipMu.Unlock()
	if status := post(key, []any{}); status < 400 {
		t.Fatalf("disabled node accepted: %d", status)
	}
}

func TestLocalExecutorCatalogSanitizedAndUnchanged(t *testing.T) {
	s := membershipWriterNode(t, "writer")
	s.agents["shell:writer"].Meta["mcp_agents"] = []any{map[string]any{
		"ref": "Vibe", "name": "Vibe Chrome", "enabled": true, "transport": "streamable-http",
		"url": "https://secret", "headers": map[string]any{"Authorization": "secret"}, "env": map[string]any{"SECRET": "secret"},
		"catalog_digest": "digest", "health": map[string]any{"protocol": map[string]any{"state": "ready", "error": "secret"}},
	}}
	raw, err := s.localNodeExecutorCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "https") {
		t.Fatalf("unsanitized: %s", raw)
	}
	items, err := decodeNodeExecutorCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	s.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Status: "online", Meta: map[string]any{"approved": true, "server_id": "identity"}}
	member := &nodeMember{NodeID: "peer", Name: "shell:peer", LocalID: "identity"}
	s.membership.Nodes["peer"] = member
	changed, err := s.applyNodeExecutorCatalog(member, items)
	if err != nil || !changed {
		t.Fatalf("first %v %v", changed, err)
	}
	changed, err = s.applyNodeExecutorCatalog(member, items)
	if err != nil || changed {
		t.Fatalf("unchanged %v %v", changed, err)
	}
	member.LocalID = "foreign"
	if _, err = s.applyNodeExecutorCatalog(member, items); err == nil {
		t.Fatal("foreign identity")
	}
	member.Name = "shell:writer"
	if _, err = s.applyNodeExecutorCatalog(member, items); err == nil {
		t.Fatal("local parent overwritten")
	}
}

func TestMembershipExecutorCatalogRoundTrip(t *testing.T) {
	writer := membershipWriterNode(t, "writer")
	ws := httptest.NewServer(writer.Handler())
	defer ws.Close()
	writer.cfg.NodePublicURL = ws.URL
	writer.ensureWriterSelfEntry()
	member, nodeID, key := membershipMemberNode(t, "peer", ws.URL, "")
	ms := httptest.NewServer(member.Handler())
	defer ms.Close()
	grant := membershipIssueGrant(t, writer, "shell:peer")
	if joined := membershipJoin(t, writer, grant, nodeID, key, "shell:peer", ms.URL); joined["__status"] != 200 {
		t.Fatal(joined)
	}
	writer.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true}}
	member.agents["shell:peer"].Meta["mcp_agents"] = []any{map[string]any{"ref": "Vibe", "name": "Vibe Chrome", "enabled": true, "transport": "streamable-http", "catalog_digest": "v1"}}
	client := &http.Client{Timeout: 5 * time.Second}
	member.membershipMemberTick(client, true)
	descriptors := sliceValue(writer.agents["shell:peer"].Meta["mcp_agents"])
	if len(descriptors) != 1 || firstString(mapValue(descriptors[0]), "catalog_digest") != "v1" {
		t.Fatalf("member catalog not received: %v", descriptors)
	}
	member.mu.Lock()
	oversized := make([]any, nodeExecutorCatalogLimit+1)
	for i := range oversized {
		oversized[i] = map[string]any{"ref": fmt.Sprintf("extra%d", i), "enabled": true}
	}
	member.agents["shell:peer"].Meta["mcp_agents"] = oversized
	member.mu.Unlock()
	member.membershipMemberTick(client, true)
	if firstString(writer.agents["shell:peer"].Meta, "membership_catalog_status") != "incomplete" {
		t.Fatal("incomplete status not propagated")
	}
	if len(sliceValue(writer.agents["shell:peer"].Meta["mcp_agents"])) != 1 {
		t.Fatal("incomplete catalog replaced known catalog")
	}
	writer.membershipMu.Lock()
	state := writer.membership.Nodes[nodeID].State
	writer.membershipMu.Unlock()
	if state != nodeStateReady {
		t.Fatalf("catalog failure blocked membership readiness: %s", state)
	}
	member.mu.Lock()
	member.agents["shell:peer"].Meta["mcp_agents"] = []any{}
	member.mu.Unlock()
	member.membershipMemberTick(client, true)
	if len(sliceValue(writer.agents["shell:peer"].Meta["mcp_agents"])) != 0 {
		t.Fatal("member removal not received")
	}
}

func TestMembershipIncompleteCatalogSearchCoverage(t *testing.T) {
	s := membershipWriterNode(t, "writer")
	s.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "membership_catalog_status": "incomplete", "mcp_agents": []any{}}}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "catalog", AccessMode: "full", AllowedTargets: []string{"shell:peer"}, AllowedTools: []string{"*"}})
	_, coverage := s.childSearchCatalogForRequest(r, "")
	if truthy(coverage["complete"]) || firstString(coverage, "status") != "incomplete" {
		t.Fatalf("missing zero-child parent: %v", coverage)
	}
	if coverage["failure_reasons"].(map[string]string)["shell:peer"] != "member executor catalog incomplete" {
		t.Fatalf("missing bounded reason: %v", coverage)
	}
	_, coverage = s.childSearchCatalogForRequest(r, "mcp:shell:other:tool")
	if !truthy(coverage["complete"]) {
		t.Fatalf("unrelated target affected: %v", coverage)
	}
	denied := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "denied", AccessMode: "full", AllowedTargets: []string{"shell:other"}, AllowedTools: []string{"*"}})
	_, coverage = s.childSearchCatalogForRequest(denied, "")
	if !truthy(coverage["complete"]) {
		t.Fatalf("unauthorized parent leaked: %v", coverage)
	}
	delete(s.agents["shell:peer"].Meta, "membership_catalog_status")
	_, coverage = s.childSearchCatalogForRequest(r, "")
	if !truthy(coverage["complete"]) {
		t.Fatalf("legacy status changed: %v", coverage)
	}
}

func TestMembershipIncompleteCatalogPreservesCachedSchemas(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	parentID := firstString(child.Meta, "parent_server_id")
	s.agents[parentID].Meta["membership_catalog_status"] = "incomplete"
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "catalog", Version: 1, AccessMode: "full", AllowedTargets: []string{parentID, child.AgentID}, AllowedTools: []string{"*"}})
	cache := s.childSearchCache()
	cache.mu.Lock()
	cache.entries[childSearchCacheKey(r, child)] = &childSearchSchemaEntry{childID: child.AgentID, parentID: parentID, fingerprint: childSearchFingerprint(s, child), loadedAt: time.Now(), tools: []map[string]any{{"name": "retained_tool", "inputSchema": map[string]any{"type": "object"}}}}
	cache.mu.Unlock()
	rows, coverage := s.childSearchCatalogForRequest(r, child.AgentID)
	if len(rows) != 1 || firstString(rows[0], "name") != "retained_tool" {
		t.Fatalf("cached tools lost: %v", rows)
	}
	if truthy(coverage["complete"]) {
		t.Fatalf("incomplete transport reported complete: %v", coverage)
	}
}

func TestMembershipRejectedCatalogStillRebuildsReadyRoute(t *testing.T) {
	s := membershipWriterNode(t, "writer")
	nodeID, key := strings.Repeat("ab", 16), strings.Repeat("cd", 32)
	grant := membershipIssueGrant(t, s, "shell:peer")
	if joined := membershipJoin(t, s, grant, nodeID, key, "shell:peer", "http://peer.example"); joined["__status"] != 200 {
		t.Fatal(joined)
	}
	s.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true}}
	body, _ := json.Marshal(map[string]any{"state": "ready", "snapshot_revision": s.writerAuthGeneration(), "executor_catalog": []any{map[string]any{"ref": "private", "owner_profile_id": "unknown", "enabled": true}}})
	result := membershipHTTP(s, http.MethodPost, "/admin/api/nodes/heartbeat", key, body)
	if result.Code != http.StatusForbidden {
		t.Fatal(result.Code)
	}
	s.membershipMu.Lock()
	state := s.membership.Nodes[nodeID].State
	s.membershipMu.Unlock()
	if state != nodeStateReady {
		t.Fatal("expected independent liveness transition")
	}
	s.mu.Lock()
	_, routed := s.membershipRoutes["shell:peer"]
	s.mu.Unlock()
	if !routed {
		t.Fatal("committed ready state has no route after catalog rejection")
	}
}

func TestMembershipIncompleteCatalogChildOnlyCoverage(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	parentID := firstString(child.Meta, "parent_server_id")
	s.agents[parentID].Meta["membership_catalog_status"] = "incomplete"
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "child-only", Version: 1, AccessMode: "full", AllowedTargets: []string{child.AgentID}, AllowedTools: []string{"*"}})
	cache := s.childSearchCache()
	cache.mu.Lock()
	cache.entries[childSearchCacheKey(r, child)] = &childSearchSchemaEntry{childID: child.AgentID, parentID: parentID, fingerprint: childSearchFingerprint(s, child), loadedAt: time.Now(), tools: []map[string]any{{"name": "retained_tool", "inputSchema": map[string]any{"type": "object"}}}}
	cache.mu.Unlock()
	rows, coverage := s.childSearchCatalogForRequest(r, child.AgentID)
	if len(rows) != 1 {
		t.Fatal("cached schema lost")
	}
	if truthy(coverage["complete"]) {
		t.Fatalf("child-only caller sees false complete: %v", coverage)
	}
	reasons := coverage["failure_reasons"].(map[string]string)
	if reasons[child.AgentID] != "member executor catalog incomplete" {
		t.Fatalf("missing authorized child reason: %v", reasons)
	}
	if _, leaked := reasons[parentID]; leaked {
		t.Fatal("denied parent identity leaked")
	}
}

func TestMembershipOwnerTombstoneProtectsParentFacade(t *testing.T) {
	s := membershipWriterNode(t, "writer")
	s.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Meta: map[string]any{"approved": true, "mcp_agents": []any{}, "membership_catalog_owners": map[string]any{"private": "alice"}}}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "bob", AccessMode: "full", AllowedTargets: []string{"shell:peer"}, AllowedTools: []string{"*"}})
	for _, tool := range []string{"mcp_call", "mcp_tools", "mcp_request", "mcp_connection_close", "mcp_manage"} {
		if err := s.authorizeMCPConnectionOwner(r, "shell:peer", tool, map[string]any{"ref": "private", "action": "get"}); err == nil {
			t.Fatalf("removed private ref bypassed owner via %s", tool)
		}
	}
	if err := s.authorizeMCPConnectionOwner(r, "shell:peer", "mcp_tools", map[string]any{"ref": "shared"}); err != nil {
		t.Fatalf("unrelated shared ref blocked: %v", err)
	}
}
