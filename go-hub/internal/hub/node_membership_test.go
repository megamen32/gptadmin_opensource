package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The membership tests exercise the real HTTP surface between two in-process
// nodes: one writer and one member, mirroring the portable manager flow
// (identity file, grant, join, heartbeat, control bootstrap, routing).

func membershipWriterNode(t *testing.T, name string) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GPTADMIN_AUTH_MODE", "writer")
	t.Setenv("GPTADMIN_CONFIG_DIR", dir)
	cfg := FromEnv()
	cfg.CtlToken = "fixture-owner"
	cfg.OAuthClientSecret = "fixture-signing"
	cfg.PublicOrigin = "https://writer.example"
	cfg.MCPResource = "https://writer.example/mcp"
	cfg.ExistingMCPBearers = nil
	s := New(cfg)
	t.Cleanup(func() { s.Close() })
	if err := s.RegisterLocalExecutor(name, map[string]string{"server_id": name + "-identity", "public_key": name + "-public", "fingerprint": name + "-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	s.ensureWriterSelfEntry()
	return s
}

func membershipMemberNode(t *testing.T, name, writerURL, writerID string) (*Server, string, string) {
	t.Helper()
	dir := t.TempDir()
	nodeID := strings.Repeat("ab", 16)
	nodeKey := strings.Repeat("cd", 32)
	identity := nodeIdentity{Schema: nodeIdentityTag, NodeID: nodeID, NodeKey: nodeKey,
		Created: time.Now().UTC(), WriterURL: writerURL, WriterID: writerID}
	if err := saveNodeIdentityFile(filepath.Join(dir, "node-identity.json"), identity); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GPTADMIN_AUTH_MODE", "reader")
	t.Setenv("GPTADMIN_CONFIG_DIR", dir)
	cfg := FromEnv()
	cfg.NodeIdentityFile = filepath.Join(dir, "node-identity.json")
	cfg.CtlToken = "member-owner"
	cfg.OAuthClientSecret = "member-signing"
	cfg.PublicOrigin = "https://member.example"
	cfg.MCPResource = "https://member.example/mcp"
	cfg.ExistingMCPBearers = nil
	s := New(cfg)
	t.Cleanup(func() { s.Close() })
	if err := s.RegisterLocalExecutor(name, map[string]string{"server_id": name + "-identity", "public_key": name + "-public", "fingerprint": name + "-fingerprint"}, strings.Repeat("y", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	return s, nodeID, nodeKey
}

func membershipHTTP(s *Server, method, path, token string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func membershipIssueGrant(t *testing.T, writer *Server, name string) string {
	t.Helper()
	response := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/grant", "fixture-owner",
		[]byte(fmt.Sprintf(`{"name":%q,"ttl_minutes":30}`, name)))
	if response.Code != 200 {
		t.Fatalf("grant issue failed: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Grant string `json:"grant"`
	}
	if json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Grant == "" {
		t.Fatalf("grant response malformed: %s", response.Body.String())
	}
	return payload.Grant
}

func membershipJoin(t *testing.T, writer *Server, grant, nodeID, nodeKey, name, endpoint string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(nodeJoinRequest{NodeID: nodeID, NodeKey: nodeKey, Name: name,
		Endpoints: []string{endpoint}, Build: nodeJoinBuild{GitCommit: "test-commit"}})
	response := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/join", grant, body)
	var payload map[string]any
	if json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatalf("join response not JSON: %d %s", response.Code, response.Body.String())
	}
	payload["__status"] = response.Code
	return payload
}

func TestMembershipGrantJoinReadyFlow(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	// Production writers advertise an explicit public origin; a proxy-visible
	// Host header must not define member routing.
	writer.cfg.NodePublicURL = writerServer.URL
	writer.ensureWriterSelfEntry()

	// Grant issue is owner-only and the grant secret is returned exactly once.
	if response := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/grant", "", []byte(`{"name":"shell:member-a"}`)); response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
		t.Fatalf("anonymous grant issued: %d", response.Code)
	}
	grant := membershipIssueGrant(t, writer, "shell:member-a")

	member, nodeID, nodeKey := membershipMemberNode(t, "member-a", writerServer.URL, "")
	memberServer := httptest.NewServer(member.Handler())
	defer memberServer.Close()

	joined := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:member-a", memberServer.URL)
	if joined["__status"] != http.StatusOK || joined["state"] != nodeStateJoining {
		t.Fatalf("join failed: %v", joined)
	}
	if joined["writer_id"] != "writer-node-identity" {
		t.Fatalf("join writer identity missing: %v", joined)
	}
	// A single-use grant cannot enroll a second identity.
	other := membershipJoin(t, writer, grant, strings.Repeat("ef", 16), strings.Repeat("12", 32), "shell:member-a", memberServer.URL)
	if other["__status"] != http.StatusForbidden {
		t.Fatalf("grant reuse accepted: %v", other)
	}

	// The member bootstraps control state and syncs membership over real HTTP.
	client := &http.Client{Timeout: 5 * time.Second}
	member.membershipMemberTick(client, true)
	if member.membershipView == nil || member.membershipView.Revision == 0 {
		t.Fatal("member did not accept a membership view")
	}
	if member.writerAuthGeneration() == 0 || member.writerAuthGeneration() != writer.writerAuthGeneration() {
		t.Fatalf("member control generation %d does not match writer %d", member.writerAuthGeneration(), writer.writerAuthGeneration())
	}
	// The second heartbeat reports the applied snapshot and flips the member ready.
	member.membershipMemberTick(client, false)
	writer.membershipMu.Lock()
	record := writer.membership.Nodes[nodeID]
	state, lease, snapshotRev := record.State, record.LeaseExpires, record.SnapshotRev
	writer.membershipMu.Unlock()
	if state != nodeStateReady || snapshotRev != writer.writerAuthGeneration() || !lease.After(time.Now()) {
		t.Fatalf("member not ready on writer: state=%s snapshot=%d lease=%v", state, snapshotRev, lease)
	}

	// Both sides derive routes from membership: writer routes to the member,
	// member routes to the writer's shell target.
	writer.mu.Lock()
	writerRoute, writerRouted := writer.membershipRoutes["shell:member-a"]
	writer.mu.Unlock()
	if !writerRouted || writerRoute.URL != memberServer.URL {
		t.Fatalf("writer membership route missing: %v %v", writerRouted, writerRoute)
	}
	member.mu.Lock()
	memberRoute, memberRouted := member.membershipRoutes["shell:writer-node"]
	member.mu.Unlock()
	if !memberRouted || memberRoute.URL != writerServer.URL {
		t.Fatalf("member membership route missing: %v %v", memberRouted, memberRoute)
	}

	// The accepted view persists for restart continuity.
	raw, err := os.ReadFile(member.memberViewPath())
	if err != nil || !json.Valid(raw) {
		t.Fatalf("member view not persisted: %v", err)
	}
}

func TestMembershipJoinValidations(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()

	expired := &nodeJoinGrant{Name: "shell:late", SecretHash: hashNodeSecret("expired-grant-secret"),
		IssuedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)}
	writer.membershipMu.Lock()
	writer.membership.Grants[expired.SecretHash] = expired
	writer.membershipMu.Unlock()

	if response := membershipJoin(t, writer, "expired-grant-secret", strings.Repeat("ab", 16), strings.Repeat("cd", 32), "shell:late", "http://127.0.0.1:1"); response["__status"] != http.StatusForbidden {
		t.Fatalf("expired grant accepted: %v", response)
	}
	if response := membershipJoin(t, writer, "unknown-grant", strings.Repeat("ab", 16), strings.Repeat("cd", 32), "shell:ghost", "http://127.0.0.1:1"); response["__status"] != http.StatusForbidden {
		t.Fatalf("unknown grant accepted: %v", response)
	}

	grant := membershipIssueGrant(t, writer, "shell:member-a")
	if response := membershipJoin(t, writer, grant, strings.Repeat("ab", 16), strings.Repeat("cd", 32), "shell:other-name", "http://127.0.0.1:1"); response["__status"] != http.StatusForbidden {
		t.Fatalf("grant name mismatch accepted: %v", response)
	}
	// Node ids and names must follow the mesh contract.
	grant = membershipIssueGrant(t, writer, "shell:member-a")
	if response := membershipJoin(t, writer, grant, "short", strings.Repeat("cd", 32), "shell:member-a", "http://127.0.0.1:1"); response["__status"] != http.StatusBadRequest {
		t.Fatalf("short node_id accepted: %v", response)
	}
	grant = membershipIssueGrant(t, writer, "shell:member-a")
	if response := membershipJoin(t, writer, grant, strings.Repeat("ab", 16), strings.Repeat("cd", 32), "member-a", "http://127.0.0.1:1"); response["__status"] != http.StatusBadRequest {
		t.Fatalf("invalid member name accepted: %v", response)
	}
	grant = membershipIssueGrant(t, writer, "shell:member-a")
	if response := membershipJoin(t, writer, grant, strings.Repeat("ab", 16), strings.Repeat("cd", 32), "shell:member-a", "ftp://example/path"); response["__status"] != http.StatusBadRequest {
		t.Fatalf("invalid endpoint accepted: %v", response)
	}
}

func TestMembershipIdentityKeyIsBound(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	grant := membershipIssueGrant(t, writer, "shell:member-a")
	nodeID := strings.Repeat("ab", 16)
	if response := membershipJoin(t, writer, grant, nodeID, strings.Repeat("cd", 32), "shell:member-a", "http://127.0.0.1:1"); response["__status"] != http.StatusOK {
		t.Fatalf("initial join failed: %v", response)
	}
	// A rejoin with a different node_key must not hijack the enrolled member.
	grant = membershipIssueGrant(t, writer, "shell:member-a")
	if response := membershipJoin(t, writer, grant, nodeID, strings.Repeat("ee", 32), "shell:member-a", "http://127.0.0.1:1"); response["__status"] != http.StatusForbidden {
		t.Fatalf("identity hijack accepted: %v", response)
	}
}

func TestMembershipHeartbeatAuthAndDisable(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	grant := membershipIssueGrant(t, writer, "shell:member-a")
	nodeID, nodeKey := strings.Repeat("ab", 16), strings.Repeat("cd", 32)
	if response := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:member-a", "http://127.0.0.1:19001"); response["__status"] != http.StatusOK {
		t.Fatalf("join failed: %v", response)
	}
	if response := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/heartbeat", "unknown-key", []byte(`{"state":"joining"}`)); response.Code != http.StatusUnauthorized {
		t.Fatalf("unknown node key accepted: %d", response.Code)
	}
	heartbeat := func(key string) int {
		return membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/heartbeat", key,
			[]byte(fmt.Sprintf(`{"revision":1,"state":%q,"snapshot_revision":%d}`, nodeStateReady, writer.writerAuthGeneration()))).Code
	}
	if heartbeat(nodeKey) != http.StatusOK {
		t.Fatal("member heartbeat rejected")
	}
	if code := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/disable", "fixture-owner", []byte(fmt.Sprintf(`{"node_id":%q}`, nodeID))).Code; code != http.StatusOK {
		t.Fatalf("disable failed: %d", code)
	}
	if heartbeat(nodeKey) != http.StatusForbidden {
		t.Fatal("disabled member heartbeat accepted")
	}
	if code := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/enable", "fixture-owner", []byte(fmt.Sprintf(`{"node_id":%q}`, nodeID))).Code; code != http.StatusOK {
		t.Fatalf("enable failed: %d", code)
	}
	if heartbeat(nodeKey) != http.StatusOK {
		t.Fatal("enabled member heartbeat rejected")
	}
}

func TestMembershipLeaseExpiryRemovesRouteOnly(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	grant := membershipIssueGrant(t, writer, "shell:member-a")
	nodeID, nodeKey := strings.Repeat("ab", 16), strings.Repeat("cd", 32)
	if response := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:member-a", "http://127.0.0.1:19001"); response["__status"] != http.StatusOK {
		t.Fatalf("join failed: %v", response)
	}
	writer.membershipMu.Lock()
	writer.membership.Nodes[nodeID].State = nodeStateReady
	writer.membership.Nodes[nodeID].LeaseExpires = time.Now().Add(-time.Minute)
	writer.membershipMu.Unlock()
	writer.rebuildMembershipRoutes()
	writer.mu.Lock()
	_, routed := writer.membershipRoutes["shell:member-a"]
	writer.mu.Unlock()
	if routed {
		t.Fatal("expired lease still routed")
	}
	writer.membershipWriterTick()
	writer.membershipMu.Lock()
	record := writer.membership.Nodes[nodeID]
	writer.membershipMu.Unlock()
	if record == nil || record.State != nodeStateOffline {
		t.Fatal("lease expiry deleted membership instead of marking offline")
	}
}

func TestMembershipReaderRedirectsJoinToWriter(t *testing.T) {
	reader, _, _ := membershipMemberNode(t, "reader-only", "https://writer.example", "writer-node-identity")
	// A fresh reader has no accepted snapshot yet, so the admission gate 503s
	// every inbound call; the redirect branch is exercised at the handler.
	body, _ := json.Marshal(nodeJoinRequest{NodeID: strings.Repeat("ab", 16), NodeKey: strings.Repeat("cd", 32), Name: "shell:member-a"})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/nodes/join", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer any-grant")
	w := httptest.NewRecorder()
	reader.nodeMembershipHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("reader accepted join: %d", w.Code)
	}
	var payload map[string]any
	if json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload["writer_url"] != "https://writer.example" {
		t.Fatalf("reader did not advertise the writer: %s", w.Body.String())
	}
}

func TestMembershipControlSnapshotRequiresNodeCredential(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	grant := membershipIssueGrant(t, writer, "shell:member-a")
	nodeID, nodeKey := strings.Repeat("ab", 16), strings.Repeat("cd", 32)
	if response := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:member-a", "http://127.0.0.1:19001"); response["__status"] != http.StatusOK {
		t.Fatalf("join failed: %v", response)
	}
	if response := membershipHTTP(writer, http.MethodGet, "/admin/api/nodes/control-snapshot", "fixture-owner", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("owner credential accepted as node credential: %d", response.Code)
	}
	response := membershipHTTP(writer, http.MethodGet, "/admin/api/nodes/control-snapshot", nodeKey, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("control snapshot rejected: %d %s", response.Code, response.Body.String())
	}
	var snapshot controlSnapshot
	if json.Unmarshal(response.Body.Bytes(), &snapshot) != nil || snapshot.Generation == 0 || snapshot.SHA256 == "" {
		t.Fatalf("control snapshot malformed: %s", response.Body.String())
	}
	inner, _ := json.Marshal(map[string]any{"bundle": snapshot.Bundle, "instruction_sets": snapshot.InstructionSets})
	if hashNodeSecret(string(inner)) != snapshot.SHA256 {
		t.Fatal("control snapshot checksum mismatch")
	}
	// Owner listing shows membership without leaking key hashes.
	list := membershipHTTP(writer, http.MethodGet, "/admin/api/nodes", "fixture-owner", nil)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), nodeKey) {
		t.Fatalf("membership list leaked credentials: %d %s", list.Code, list.Body.String())
	}
}

// The mesh summary names the topology the process runs — writer (central
// membership hub), member (replicated view), standalone (static peers only,
// no hub) — and always shows the static bootstrap peers and live member
// connections, so the operator can see which topology is active and where
// every route comes from.
func TestMeshSummaryModes(t *testing.T) {
	meshSummary := func(s *Server) map[string]any {
		t.Helper()
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.meshSummaryLocked()
	}

	// Standalone: static peers route, no writer, no identity.
	dir := t.TempDir()
	t.Setenv("GPTADMIN_AUTH_MODE", "reader")
	t.Setenv("GPTADMIN_CONFIG_DIR", dir)
	cfg := FromEnv()
	cfg.NodePeersJSON = `{"shell:nodeB":"https://node-b.example"}`
	cfg.CtlToken = "standalone-owner"
	cfg.OAuthClientSecret = "standalone-signing"
	cfg.PublicOrigin = "https://standalone.example"
	cfg.MCPResource = "https://standalone.example/mcp"
	cfg.ExistingMCPBearers = nil
	standalone := New(cfg)
	t.Cleanup(func() { standalone.Close() })
	if err := standalone.RegisterLocalExecutor("standalone-node", map[string]string{"server_id": "standalone-node-identity", "public_key": "standalone-public", "fingerprint": "standalone-fingerprint"}, strings.Repeat("z", 32)); err != nil {
		t.Fatal(err)
	}
	summary := meshSummary(standalone)
	if summary["mode"] != "standalone" || summary["enabled"] != false {
		t.Fatalf("standalone summary wrong: %v", summary)
	}
	peers, ok := summary["static_peers"].(map[string]nodePeerRoute)
	if !ok || len(peers) != 1 || peers["shell:nodeB"].URL != "https://node-b.example" {
		t.Fatalf("static peers missing from summary: %v", summary["static_peers"])
	}
	if _, hasNodes := summary["nodes"]; hasNodes {
		t.Fatalf("standalone summary must not invent membership nodes: %v", summary)
	}

	// Writer: the central membership hub.
	writer := membershipWriterNode(t, "writer-node")
	summary = meshSummary(writer)
	if summary["mode"] != "writer" || summary["enabled"] != true {
		t.Fatalf("writer summary wrong: %v", summary)
	}
	if _, hasRevision := summary["revision"]; !hasRevision {
		t.Fatalf("writer summary missing revision: %v", summary)
	}

	// Member: replicated view from the writer.
	member, _, _ := membershipMemberNode(t, "member-node", "https://writer.example", "writer")
	summary = meshSummary(member)
	if summary["mode"] != "member" {
		t.Fatalf("member summary wrong: %v", summary)
	}
}
