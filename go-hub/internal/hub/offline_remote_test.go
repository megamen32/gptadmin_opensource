package hub

import (
	"context"
	"encoding/json"
	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOfflineRemoteUsesOnlyPinnedStaticTargetAndDeniesApproval(t *testing.T) {
	var hits atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get(nodePeerHopHeader) != "1" || r.Header.Get("X-GPTAdmin-Offline-Owner") != "ownerB" || r.Header.Get("X-GPTAdmin-Offline-Target") != "shell:b" {
			t.Error("missing bound peer identity")
		}
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("caller bearer lost")
		}
		var q map[string]any
		_ = json.NewDecoder(r.Body).Decode(&q)
		code := firstString(mapValue(mapValue(q["params"])["arguments"]), "code")
		if !strings.Contains(code, "mcp.tasks.start") || strings.Contains(code, "client-script") {
			t.Error("wrong peer operation")
		}
		id, _ := encodeOwnerHandle("task", "ownerB", "local")
		writeJSON(w, 200, map[string]any{"result": map[string]any{"structuredContent": map[string]any{"status": "completed", "result": map[string]any{"taskId": id}}}})
	}))
	defer peer.Close()
	s := New(Config{})
	defer s.Close()
	s.nodePeerClient = peer.Client()
	s.nodePeers["shell:b"] = nodePeerRoute{URL: peer.URL, NodeID: "ownerB"}
	s.membershipView = &nodeMembershipView{Nodes: []*nodeMember{{NodeID: "ownerB", Name: "shell:b", State: nodeStateReady, Capabilities: []string{"executor"}, LocalID: "id", LocalFingerprint: "fp"}}}
	s.membershipRoutes = map[string]nodePeerRoute{"shell:b": {URL: "https://untrusted.invalid", NodeID: "other"}}
	p := AccessProfile{ID: "p", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"shell:b"}, AllowedTools: []string{"shell_exec"}}
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	req = requestWithAccessProfile(req, p)
	req = req.WithContext(context.WithValue(req.Context(), offlineDurableBearerKey{}, true))
	got, status := s.offlineRemoteDispatch(req, "shell:b", "shell_exec", map[string]any{"cmd": "true"}, "", true)
	if status != 200 || firstString(got, "owner_id") != "ownerB" || hits.Load() != 1 {
		t.Fatalf("wrong delivery %d %v %d", status, got, hits.Load())
	}
	p.ApprovalMode = approvalModeAskBeforeWrite
	denied := requestWithAccessProfile(req, p)
	_, status = s.offlineRemoteDispatch(denied, "shell:b", "shell_exec", map[string]any{"cmd": "true"}, "", true)
	if status != http.StatusForbidden || hits.Load() != 1 {
		t.Fatalf("approval profile reached peer: %d %d", status, hits.Load())
	}
	_, status = s.offlineRemoteDispatch(req, "shell:b", "shell_exec", map[string]any{"cmd": "true"}, "", false)
	if status != http.StatusForbidden || hits.Load() != 1 {
		t.Fatal("synchronous request reached peer")
	}
	s.membershipView.Nodes[0].State = nodeStateDisabled
	if s.offlineRemoteAllowed(req, "shell:b") {
		t.Fatal("disabled reader owner fell back to static")
	}
	s.membershipView.Nodes[0].State = nodeStateReady
	req.Header.Set(nodePeerHopHeader, "1")
	if s.offlineRemoteAllowed(req, "shell:b") {
		t.Fatal("second hop permitted")
	}
}

func TestOfflineDurableWildcardAcceptedOnSinglePeerHop(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r = requestWithAccessProfile(r, AccessProfile{ID: "p", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}})
	r = r.WithContext(context.WithValue(r.Context(), offlineDurableBearerKey{}, true))
	r.Header.Set(nodePeerHopHeader, "1")
	if !offlineShellProfileAllows(r, "shell:b") {
		t.Fatal("managed wildcard ACL rejected on peer")
	}
	r = requestWithAccessProfile(r, AccessProfile{ID: "p", AccessMode: accessModeFull, ApprovalMode: approvalModeAskBeforeWrite, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}})
	if offlineShellProfileAllows(r, "shell:b") {
		t.Fatal("approval profile admitted offline")
	}
}

func TestOfflineMarkedHopRefusesWrongOwnerBeforeHandler(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	var calls atomic.Int32
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set(nodePeerHopHeader, "1")
	r.Header.Set("X-GPTAdmin-Offline-Owner", "wrong")
	r.Header.Set("X-GPTAdmin-Offline-Target", "shell:b")
	w := httptest.NewRecorder()
	s.withQuorumAdmission(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) })).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("wrong owner reached handler: %d", w.Code)
	}
}

func TestOfflineRemoteLostReplyIsUnknownAndNeverResent(t *testing.T) {
	var hits atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer peer.Close()
	s := New(Config{})
	defer s.Close()
	s.nodePeerClient = peer.Client()
	s.nodePeers["shell:b"] = nodePeerRoute{URL: peer.URL, NodeID: "ownerB"}
	s.membershipView = &nodeMembershipView{Nodes: []*nodeMember{{NodeID: "ownerB", Name: "shell:b", State: nodeStateReady, Capabilities: []string{"executor"}}}}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer fixture")
	r = requestWithAccessProfile(r, AccessProfile{ID: "p", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}})
	r = r.WithContext(context.WithValue(r.Context(), offlineDurableBearerKey{}, true))
	out, status := s.offlineRemoteDispatch(r, "shell:b", "shell_exec", map[string]any{"cmd": "true"}, "", true)
	if status != http.StatusBadGateway || firstString(mapValue(out["error"]), "code") != "task_outcome_unknown" || hits.Load() != 1 {
		t.Fatalf("lost reply was resent or misclassified: %d calls=%d", status, hits.Load())
	}
}

func TestOfflineRemoteReturnsPeerInstructionRefusal(t *testing.T) {
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": map[string]any{"structuredContent": map[string]any{"status": "failed", "executed": false, "error": map[string]any{"code": "instructions_required"}, "context": "peer-context", "instructions": "read this"}}})
	}))
	defer peer.Close()
	s := New(Config{})
	defer s.Close()
	s.nodePeerClient = peer.Client()
	s.nodePeers["shell:b"] = nodePeerRoute{URL: peer.URL, NodeID: "ownerB"}
	s.membershipView = &nodeMembershipView{Nodes: []*nodeMember{{NodeID: "ownerB", Name: "shell:b", State: nodeStateReady, Capabilities: []string{"executor"}}}}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer fixture")
	r = requestWithAccessProfile(r, AccessProfile{ID: "p", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}})
	r = r.WithContext(context.WithValue(r.Context(), offlineDurableBearerKey{}, true))
	out, status := s.offlineRemoteDispatch(r, "shell:b", "shell_exec", map[string]any{"cmd": "true"}, "", true)
	if status != http.StatusPreconditionRequired || out["executed"] != false || out["context"] != "peer-context" || out["owner_target"] != "shell:b" {
		t.Fatalf("lost peer refusal: %d %v", status, out)
	}
}

func TestOfflineRemoteReturnsOwnerAuthorizationDenial(t *testing.T) {
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", http.StatusForbidden) }))
	defer peer.Close()
	s := New(Config{})
	defer s.Close()
	s.nodePeerClient = peer.Client()
	s.nodePeers["shell:b"] = nodePeerRoute{URL: peer.URL, NodeID: "ownerB"}
	s.membershipView = &nodeMembershipView{Nodes: []*nodeMember{{NodeID: "ownerB", Name: "shell:b", State: nodeStateReady, Capabilities: []string{"executor"}}}}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer fixture")
	r = requestWithAccessProfile(r, AccessProfile{ID: "p", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}})
	r = r.WithContext(context.WithValue(r.Context(), offlineDurableBearerKey{}, true))
	out, status := s.offlineRemoteDispatch(r, "shell:b", "shell_exec", map[string]any{"cmd": "true"}, "", true)
	if status != http.StatusForbidden || out["executed"] != false {
		t.Fatalf("owner denial misclassified: %d %v", status, out)
	}
}

func TestOfflineRemoteQuorumUsesAppliedIdentityAndConfiguredPrivateEndpoint(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	s.quorumPrivateTLS = &quorumPrivateTLS{peers: map[string]quorum.Peer{"peerB": {ID: "peerB", InternalHubURL: "https://127.0.0.1:1234", NodeKeyHash: "keyhash"}}}
	p := AccessProfile{ID: "p", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"shell:b"}, AllowedTools: []string{"shell_exec"}}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r = requestWithAccessProfile(r, p)
	r = r.WithContext(context.WithValue(r.Context(), offlineDurableBearerKey{}, true))
	a := offlineShellAuthority{members: map[string]*nodeMember{"peerB": {NodeID: "peerB", Name: "shell:b", State: nodeStateReady, Capabilities: []string{"executor"}, LocalID: "localB", LocalFingerprint: "fpB", KeyHash: "keyhash", Endpoints: []string{"https://untrusted.invalid"}}}, registry: map[string]Agent{"shell:b": {Meta: map[string]any{"approved": true, "server_id": "localB", "fingerprint": "fpB"}}}}
	r = r.WithContext(context.WithValue(r.Context(), offlineShellAuthorityKey{}, a))
	a.members["retired"] = &nodeMember{NodeID: "retired", Name: "shell:b", State: nodeStateDisabled}
	route, ok := s.offlineRemoteRoute(r, "shell:b")
	if !ok || !route.private || route.URL != "https://127.0.0.1:1234" {
		t.Fatalf("incorrect private owner route: %v %v", route, ok)
	}
	s.nodePeers["shell:b"] = nodePeerRoute{URL: "https://static.invalid", NodeID: "peerB"}
	a.members["peerB"].State = nodeStateDisabled
	if _, ok = s.offlineRemoteRoute(r, "shell:b"); ok {
		t.Fatal("disabled owner fell back to static")
	}
	a.members["peerB"].State = nodeStateReady
	a.members["peerB"].State = nodeStateJoining
	if _, ok = s.offlineRemoteRoute(r, "shell:b"); ok {
		t.Fatal("joining owner admitted")
	}
	a.members["peerB"].State = nodeStateReady
	a.members["other"] = &nodeMember{NodeID: "other", Name: "shell:b", State: nodeStateReady}
	if _, ok = s.offlineRemoteRoute(r, "shell:b"); ok {
		t.Fatal("ambiguous active owners accepted")
	}
	delete(a.members, "other")
	a.registry["shell:b"] = Agent{Meta: map[string]any{"approved": true, "server_id": "localB", "fingerprint": "bad"}}
	if _, ok = s.offlineRemoteRoute(r, "shell:b"); ok {
		t.Fatal("conflicting executor pin accepted")
	}
}
