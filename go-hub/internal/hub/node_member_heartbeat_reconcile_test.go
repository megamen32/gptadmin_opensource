package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func joinedMember44ForReconcile(t *testing.T, writer *Server, localID string) (string, string) {
	t.Helper()
	nodeID := strings.Repeat("a1", 16)
	nodeKey := strings.Repeat("b2", 32)
	grant := membershipIssueGrant(t, writer, "shell:node-44")
	request := nodeJoinRequest{
		NodeID: nodeID, NodeKey: nodeKey, Name: "shell:node-44",
		LocalID:   localID,
		Endpoints: []string{"https://node44.example:9001"},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	got := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/join", grant, body)
	if got.Code != http.StatusOK {
		t.Fatalf("join status %d body=%s", got.Code, got.Body.String())
	}
	return nodeID, nodeKey
}

func member44Heartbeat(t *testing.T, writer *Server, key, endpoint string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"revision":          1,
		"state":             nodeStateReady,
		"snapshot_revision": writer.writerAuthGeneration(),
		"endpoint":          endpoint, "local_name": "shell:node-44",
		"local_id": "stable-shell-id",
	})
	return membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/heartbeat", key, body)
}

func TestNodeMemberHeartbeatRefreshesPinnedPortOnly(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	id, key := joinedMember44ForReconcile(t, writer, "stable-shell-id")
	heartbeat := member44Heartbeat(t, writer, key, "https://node44.example:9443")
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("same host port update: %d %s", heartbeat.Code, heartbeat.Body.String())
	}
	writer.mu.Lock()
	route, routed := writer.membershipRoutes["shell:node-44"]
	writer.mu.Unlock()
	if !routed || route.URL != "https://node44.example:9443" || route.LocalID != "stable-shell-id" {
		t.Fatalf("routed member with wrong identity/port: %+v found=%v", route, routed)
	}
	writer.membershipMu.Lock()
	firstRevision := writer.membership.Revision
	writer.membershipMu.Unlock()
	same := member44Heartbeat(t, writer, key, "https://node44.example:9443")
	if same.Code != http.StatusOK {
		t.Fatalf("same endpoint status %d", same.Code)
	}
	writer.membershipMu.Lock()
	secondRevision := writer.membership.Revision
	writer.membershipMu.Unlock()
	if firstRevision != secondRevision {
		t.Fatalf("unchanged endpoint bumped revision %d -> %d", firstRevision, secondRevision)
	}
	for _, tc := range []struct {
		endpoint string
		status   int
	}{
		{"https://evil.example:9443", http.StatusForbidden},
		{"http://node44.example:9443", http.StatusForbidden},
		{"https://node44.example:9443/steal", http.StatusBadRequest},
	} {
		resp := member44Heartbeat(t, writer, key, tc.endpoint)
		if resp.Code != tc.status {
			t.Fatalf("unexpected origin %q status=%d expected=%d", tc.endpoint, resp.Code, tc.status)
		}
	}
	writer.membershipMu.Lock()
	old := writer.membership.Nodes[id].Endpoints[0]
	writer.membershipMu.Unlock()
	if old != "https://node44.example:9443" {
		t.Fatalf("unauthorized endpoint altered route: %q", old)
	}
	invalid := member44Heartbeat(t, writer, "not-a-node-key", "https://node44.example:9333")
	if invalid.Code != http.StatusUnauthorized {
		t.Fatalf("unbound credential changed endpoint, got %d", invalid.Code)
	}
}

func findAgentForReconcile(agents []Agent, id string) (Agent, bool) {
	for _, a := range agents {
		if a.AgentID == id {
			return a, true
		}
	}
	return Agent{}, false
}

func visibleMember44ForReconcile(t *testing.T, writer *Server) []Agent {
	t.Helper()
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.publicAgentsLocked(httptest.NewRequest(http.MethodGet, "/mcp-relay/servers", nil))
}

func TestNodeMembershipOverlayPreservesApprovedIdentityAndStaleChild(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	oldTime := float64(time.Now().Add(-5 * time.Hour).Unix())
	writer.mu.Lock()
	writer.agents["shell:node-44"] = &Agent{
		AgentID: "shell:node-44", Name: "Shell: node-44",
		Kind: "virtual_shell", Status: "stale", Transport: "long_poll", LastSeen: oldTime,
		Meta: map[string]any{
			"approved":      true,
			"build_version": 1032,
			"server_id":     "stable-shell-id",
			"base_url":      "http://node44.example:25900",
			"mcp_agents": []any{map[string]any{
				"ref": "GrepMesh", "name": "GrepMesh", "enabled": true, "transport": "stdio",
			}},
		},
	}
	writer.mu.Unlock()
	_, key := joinedMember44ForReconcile(t, writer, "")
	if result := member44Heartbeat(t, writer, key, "https://node44.example:9443"); result.Code != http.StatusOK {
		t.Fatalf("member heartbeat %d %s", result.Code, result.Body.String())
	}
	agents := visibleMember44ForReconcile(t, writer)
	shell, exists := findAgentForReconcile(agents, "shell:node-44")
	if !exists || shell.Status != "online" {
		t.Fatalf("approved member not shown via live lease %+v exists=%v", shell, exists)
	}
	if shell.Meta["base_url"] != "https://node44.example:9443" || shell.Meta["membership_transport"] != "authenticated_node_lease" {
		t.Fatalf("discovery did not reflect current port: %+v", shell.Meta)
	}
	for _, id := range []string{"file:node-44", "control:node-44"} {
		surface, exists := findAgentForReconcile(agents, id)
		if !exists || surface.Status != "online" {
			t.Fatalf("paired surface %s not refreshed via owner lease: %+v", id, surface)
		}
	}
	child, exists := findAgentForReconcile(agents, "mcp:shell:node-44:GrepMesh")
	if !exists || child.Status != "stale" {
		t.Fatalf("old child catalog incorrectly revived by shell lease: %+v exists=%v", child, exists)
	}
	// A complete catalog received independently from the authenticated node
	// may become live on the same valid membership lease.
	writer.mu.Lock()
	writer.agents["shell:node-44"].Meta["membership_catalog_status"] = "complete"
	writer.mu.Unlock()
	// A persisted "complete" catalog without a fresh accepted heartbeat
	// must not revive a child after writer restart.
	agents = visibleMember44ForReconcile(t, writer)
	child, exists = findAgentForReconcile(agents, "mcp:shell:node-44:GrepMesh")
	if !exists || child.Status != "stale" {
		t.Fatalf("persisted catalog incorrectly revived after restart: %+v", child)
	}
	writer.membershipMu.Lock()
	for _, member := range writer.membership.Nodes {
		if member.Name == "shell:node-44" {
			member.CatalogFresh = true
		}
	}
	writer.membershipMu.Unlock()
	writer.rebuildMembershipRoutes()
	agents = visibleMember44ForReconcile(t, writer)
	child, exists = findAgentForReconcile(agents, "mcp:shell:node-44:GrepMesh")
	if !exists || child.Status != "online" {
		t.Fatalf("member catalog not made live after complete receipt: %+v exists=%v", child, exists)
	}
	writer.mu.Lock()
	writer.agents["shell:node-44"].Meta["membership_catalog_status"] = "incomplete"
	writer.mu.Unlock()
	writer.membershipMu.Lock()
	for _, member := range writer.membership.Nodes {
		if member.Name == "shell:node-44" {
			member.CatalogFresh = false
		}
	}
	writer.membershipMu.Unlock()
	writer.rebuildMembershipRoutes()
	agents = visibleMember44ForReconcile(t, writer)
	child, exists = findAgentForReconcile(agents, "mcp:shell:node-44:GrepMesh")
	if !exists || child.Status != "stale" {
		t.Fatalf("incomplete member catalog was advertised live: %+v exists=%v", child, exists)
	}
	writer.mu.Lock()
	durable := *writer.agents["shell:node-44"]
	writer.mu.Unlock()
	if durable.Status != "stale" || durable.LastSeen != oldTime ||
		durable.Meta["base_url"] != "http://node44.example:25900" {
		t.Fatalf("membership overlay mutated durable shell state: %+v", durable)
	}
	writer.mu.Lock()
	writer.agents["shell:node-44"].Meta["approved"] = false
	writer.mu.Unlock()
	agents = visibleMember44ForReconcile(t, writer)
	shell, _ = findAgentForReconcile(agents, "shell:node-44")
	if shell.Status != "stale" {
		t.Fatalf("unapproved shell revived: %s", shell.Status)
	}
	writer.mu.Lock()
	writer.agents["shell:node-44"].Meta["approved"] = true
	writer.agents["shell:node-44"].Meta["server_id"] = "other-identity"
	writer.mu.Unlock()
	agents = visibleMember44ForReconcile(t, writer)
	shell, _ = findAgentForReconcile(agents, "shell:node-44")
	if shell.Status != "stale" {
		t.Fatalf("wrong shell identity revived: %s", shell.Status)
	}
	writer.mu.Lock()
	writer.agents["shell:node-44"].Meta["server_id"] = "stable-shell-id"
	writer.mu.Unlock()
	writer.membershipMu.Lock()
	for _, member := range writer.membership.Nodes {
		if member.Name == "shell:node-44" {
			member.LeaseExpires = time.Now().Add(-time.Minute)
			member.State = nodeStateOffline
		}
	}
	writer.membershipMu.Unlock()
	writer.rebuildMembershipRoutes()
	agents = visibleMember44ForReconcile(t, writer)
	shell, _ = findAgentForReconcile(agents, "shell:node-44")
	if shell.Status != "stale" {
		t.Fatalf("expired member lease revived shell: %s", shell.Status)
	}
}

func TestMembershipMemberTickReconcilesExistingShellWithoutRejoin(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	writer.cfg.NodePublicURL = writerServer.URL
	writer.ensureWriterSelfEntry()
	member, nodeID, nodeKey := membershipMemberNode(t, "node-44", writerServer.URL, "writer-node-identity")
	member.cfg.NodePublicURL = "http://127.0.0.1:19443"
	writer.mu.Lock()
	writer.agents["shell:node-44"] = &Agent{
		AgentID: "shell:node-44", Kind: "virtual_shell", Status: "stale",
		Meta: map[string]any{"approved": true, "server_id": "node-44-identity"},
	}
	writer.mu.Unlock()
	grant := membershipIssueGrant(t, writer, "shell:node-44")
	if joined := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:node-44", "http://127.0.0.1:9001"); joined["__status"] != http.StatusOK {
		t.Fatalf("initial join failed: %+v", joined)
	}
	member.membershipMemberTick(&http.Client{Timeout: 5 * time.Second}, true)
	member.membershipMemberTick(&http.Client{Timeout: 5 * time.Second}, false)
	writer.membershipMu.Lock()
	record := *writer.membership.Nodes[nodeID]
	writer.membershipMu.Unlock()
	if record.LocalID != "node-44-identity" || len(record.Endpoints) != 1 ||
		record.Endpoints[0] != "http://127.0.0.1:19443" || record.State != nodeStateReady {
		t.Fatalf("heartbeat did not reconcile identity, port and ready lease: %+v", record)
	}
	shell, found := findAgentForReconcile(visibleMember44ForReconcile(t, writer), "shell:node-44")
	if !found || shell.Status != "online" {
		t.Fatalf("writer discovery kept approved connected shell stale: %+v found=%v", shell, found)
	}
}

func TestMembershipHeartbeatWithoutLegacyShellNeverPinsUnapprovedIdentity(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	grant := membershipIssueGrant(t, writer, "shell:node-44")
	nodeID, nodeKey := strings.Repeat("a3", 16), strings.Repeat("b4", 32)
	if got := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:node-44", "http://127.0.0.1:9001"); got["__status"] != http.StatusOK {
		t.Fatalf("join failed: %+v", got)
	}
	heartbeat := member44Heartbeat(t, writer, nodeKey, "http://127.0.0.1:9002")
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("unregistered new member heartbeat rejected %d", heartbeat.Code)
	}
	writer.membershipMu.Lock()
	actual := *writer.membership.Nodes[nodeID]
	writer.membershipMu.Unlock()
	if actual.LocalID != "" {
		t.Fatalf("unapproved legacy shell identity bound by heartbeat %q", actual.LocalID)
	}
}

func TestNewGrantedMemberAppearsInSchemaAndDiscovery(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	id, key := joinedMember44ForReconcile(t, writer, "stable-shell-id")
	if got := member44Heartbeat(t, writer, key, "https://node44.example:9001"); got.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d %s", got.Code, got.Body.String())
	}
	agent, ok := findAgentForReconcile(visibleMember44ForReconcile(t, writer), "shell:node-44")
	if !ok || agent.Status != "online" || firstString(agent.Meta, "server_id") != "stable-shell-id" {
		t.Fatalf("enrolled member absent from discovery: %+v found=%v", agent, ok)
	}
	if _, status, detail := writer.selectMCPRelayTarget("shell:node-44"); status != http.StatusOK {
		t.Fatalf("enrolled member schema target unavailable: %d %s", status, detail)
	}
	schema := membershipHTTP(writer, http.MethodPost, "/mcp-relay/tools", "fixture-owner", []byte(`{"target":"shell:node-44"}`))
	if schema.Code != http.StatusOK || !strings.Contains(schema.Body.String(), `"shell_exec"`) {
		t.Fatalf("enrolled member schema missing shell_exec: %d %s", schema.Code, schema.Body.String())
	}
	writer.mu.Lock()
	_, persisted := writer.agents["shell:node-44"]
	writer.mu.Unlock()
	if persisted {
		t.Fatal("membership projection must not change the approval registry")
	}
	writer.membershipMu.Lock()
	writer.membership.Nodes[id].LocalID = ""
	writer.membershipMu.Unlock()
	writer.rebuildMembershipRoutes()
	if _, found := findAgentForReconcile(visibleMember44ForReconcile(t, writer), "shell:node-44"); found {
		t.Fatal("member without an owner-pinned executor identity was exposed")
	}
	writer.membershipMu.Lock()
	writer.membership.Nodes[id].LocalID = "stable-shell-id"
	writer.membershipMu.Unlock()
	writer.rebuildMembershipRoutes()
	writer.mu.Lock()
	writer.agents["shell:node-44"] = &Agent{AgentID: "shell:node-44", Kind: "virtual_shell", Status: "awaiting_approval", Meta: map[string]any{"approved": false}}
	writer.mu.Unlock()
	if agent, found := findAgentForReconcile(visibleMember44ForReconcile(t, writer), "shell:node-44"); !found || agent.Status != "awaiting_approval" {
		t.Fatalf("pending legacy approval was overwritten: %+v", agent)
	}
	writer.mu.Lock()
	delete(writer.agents, "shell:node-44")
	writer.mu.Unlock()
	writer.membershipMu.Lock()
	writer.membership.Nodes[id].State = nodeStateDisabled
	writer.membershipMu.Unlock()
	writer.rebuildMembershipRoutes()
	if _, found := findAgentForReconcile(visibleMember44ForReconcile(t, writer), "shell:node-44"); found {
		t.Fatal("disabled member remained discoverable")
	}
}
