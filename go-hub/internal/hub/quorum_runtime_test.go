package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestQuorumMemberPromotionReadinessKeepsIdentityDomainsSeparate(t *testing.T) {
	now := time.Now()
	member := nodeMember{
		NodeID: "member-node-id", KeyHash: "membership-credential-hash",
		State: nodeStateReady, LeaseExpires: now.Add(time.Minute),
	}
	peer := quorum.Peer{
		ID: "member-node-id", NodeKeyHash: strings.Repeat("a", 64),
		FailureDomain: "server-44", HubURL: "https://quorum44-node.example",
	}
	if !quorumMemberPromotionReady(member, peer, now) {
		t.Fatal("ready enrolled membership must be eligible; its membership key and Raft transport key are distinct credentials")
	}

	member.State = nodeStateDisabled
	if quorumMemberPromotionReady(member, peer, now) {
		t.Fatal("disabled member must not be promoted")
	}
	member.State = nodeStateReady
	member.LeaseExpires = now
	if quorumMemberPromotionReady(member, peer, now) {
		t.Fatal("expired membership lease must not be promoted")
	}
	member.LeaseExpires = now.Add(time.Minute)
	member.NodeID = "different-node"
	if quorumMemberPromotionReady(member, peer, now) {
		t.Fatal("membership for another node must not be promoted")
	}
}

func TestPublicActionsSchemaWithoutLeader(t *testing.T) {
	s := reviewWriter(t, t.TempDir())
	t.Cleanup(func() { s.Close() })
	cfg := reviewQuorumConfig(t, t.TempDir())
	cfg.BootstrapperID = "n1"
	cfg.Bootstrap = false
	reviewOpenQuorum(t, s, cfg)
	handler := s.Handler()
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://customer.example"+path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "/actions/openapi.yaml"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "https://customer.example") {
		t.Fatalf("public specification unavailable on isolated Node: status=%d", w.Code)
	}
	if w := request(http.MethodHead, "/actions/openapi.yaml"); w.Code != http.StatusOK || w.Header().Get("Content-Length") == "" {
		t.Fatalf("public specification metadata unavailable: %d", w.Code)
	}
	if w := request(http.MethodPost, "/actions/openapi.yaml"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-read request escaped quorum admission: %d", w.Code)
	}
}
