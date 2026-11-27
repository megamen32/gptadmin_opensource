package hub

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The caller supplies an elected three-voter cluster with its committed baseline.
func testQuorumExecutorEnrollment(t *testing.T, s *Server) {
	const local = "shell:node-44"
	const remote = "shell:node-88"
	const aliasKey = "historical-node-fixture"
	nodes := map[string]*nodeMember{}
	add := func(id, target, state, localID, fingerprint string) *nodeMember {
		m := &nodeMember{NodeID: id, Name: target, State: state, LocalID: localID, LocalFingerprint: fingerprint, Capabilities: []string{"executor"}}
		if peer, ok := s.quorumPrivateTLS.peers[id]; ok {
			m.KeyHash = peer.NodeKeyHash
		}
		nodes[id] = m
		return m
	}
	add("n0", local, nodeStateOffline, "executor-44", "fp44")
	alias44 := add("executor-44", local, nodeStateReady, "executor-44", "fp44")
	voter88 := add("n1", remote, nodeStateReady, "executor-88", "fp88")
	add("archive", remote, nodeStateDisabled, "", "")

	oldMembership, oldView, oldLocal, oldTokens, oldAgents := s.membership, s.membershipView, s.localExecutors, s.localExecutorTokens, s.agents
	t.Cleanup(func() {
		s.membership, s.membershipView, s.localExecutors, s.localExecutorTokens, s.agents = oldMembership, oldView, oldLocal, oldTokens, oldAgents
	})
	s.membership = &nodeMembershipState{Nodes: nodes}
	s.localExecutors = map[string]map[string]string{local: {"server_id": "executor-44", "fingerprint": "fp44", "public_key": "public44"}}
	s.localExecutorTokens = map[string]string{local: strings.Repeat("l", 32)}
	s.agents = maps.Clone(oldAgents)
	s.agents[remote] = &Agent{Status: "online", Meta: map[string]any{"approved": true, "server_id": "executor-88", "fingerprint": "fp88"}}

	route := func(t *testing.T, target string, wantQueue, wantError bool) {
		t.Helper()
		use, err := s.quorumExecutorUsesLeaderQueue(context.Background(), target)
		if use != wantQueue || (err != nil) != wantError {
			t.Errorf("%s: leader queue=%v error=%v; want queue=%v error=%v", target, use, err, wantQueue, wantError)
		}
	}
	guarded := func(target string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.remoteMemberExecutorLocked(target)
	}
	attests := func(peer, target string) bool {
		r := httptest.NewRequest(http.MethodPost, "/queue/"+strings.TrimPrefix(target, "shell:")+"/result", nil)
		return s.voterOwnsRemoteExecutor(peer, target, r)
	}
	t.Run("pinned local and enrolled remote", func(t *testing.T) {
		for range 24 {
			route(t, local, false, false)
			route(t, remote, true, false)
		}
		if guarded(local) || !guarded(remote) || !attests("n1", remote) || attests("n2", remote) {
			t.Fatal("queue credentials do not match the physical owners")
		}
	})
	t.Run("conflicting live owner", func(t *testing.T) {
		for _, field := range []*string{&alias44.LocalID, &alias44.LocalFingerprint} {
			old := *field
			*field = "different"
			route(t, local, false, true)
			if !guarded(local) {
				t.Error("ambiguous target admitted a public poll")
			}
			*field = old
		}
		add("n2", remote, nodeStateReady, "executor-88", "fp88")
		defer delete(nodes, "n2")
		route(t, remote, false, true)
		if attests("n1", remote) || attests("n2", remote) {
			t.Error("two configured voters authorized for one queue")
		}
	})
	t.Run("registry pin and enrollment", func(t *testing.T) {
		for _, field := range []*string{&voter88.LocalID, &voter88.LocalFingerprint, &voter88.KeyHash} {
			old := *field
			for _, invalid := range []string{"", "different"} {
				*field = invalid
				route(t, remote, false, true)
				if attests("n1", remote) {
					t.Error("mismatched voter authorized")
				}
			}
			*field = old
		}
	})
	t.Run("ready voter view may omit private key hash", func(t *testing.T) {
		viewNodes := make([]*nodeMember, 0, len(nodes))
		for _, member := range nodes {
			clone := *member
			clone.KeyHash = ""
			viewNodes = append(viewNodes, &clone)
		}
		s.membership = nil
		s.membershipView = &nodeMembershipView{Nodes: viewNodes}
		route(t, remote, true, false)
	})
	t.Run("historical member credential", func(t *testing.T) {
		alias := add("executor-88", remote, nodeStateReady, "executor-88", "fp88")
		defer delete(nodes, alias.NodeID)
		alias.KeyHash = hashNodeSecret(aliasKey)
		route(t, remote, true, false)
		request := httptest.NewRequest(http.MethodPost, memberQueuePrefix+"node-88/result", strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+aliasKey)
		response := httptest.NewRecorder()
		s.memberExecutorQueue(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("historical credential reached the result handler: HTTP %d", response.Code)
		}
	})
}
