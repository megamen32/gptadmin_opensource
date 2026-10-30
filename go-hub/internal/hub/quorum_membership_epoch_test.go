package hub

import (
	"testing"
	"time"
)

func TestQuorumBootstrapsLegacyZeroMembershipEpoch(t *testing.T) {
	s := membershipWriterNode(t, "writer-node")
	s.cfg.NodePublicURL = "https://writer.example"
	writerID := s.authLocalID
	state := &nodeMembershipState{
		Schema: nodeMembershipSchema, Revision: 2, WriterID: writerID,
		Grants: map[string]*nodeJoinGrant{},
		Nodes:  map[string]*nodeMember{writerID: {NodeID: writerID, Name: "shell:writer-node"}},
	}
	if err := s.promoteQuorumMembership(state); err != nil {
		t.Fatalf("legacy epoch must initialize on the existing writer: %v", err)
	}
	if state.Epoch != 1 || state.Revision != 3 {
		t.Fatalf("initial epoch/revision = %d/%d", state.Epoch, state.Revision)
	}
	if err := s.promoteQuorumMembership(state); err != nil {
		t.Fatal(err)
	}
	if state.Epoch != 1 || state.Revision != 3 {
		t.Fatalf("repeated materialization changed epoch/revision to %d/%d", state.Epoch, state.Revision)
	}
}

func TestQuorumPromotesLegacyZeroEpochToNewWriter(t *testing.T) {
	s := membershipWriterNode(t, "new-writer")
	s.cfg.NodePublicURL = "https://new-writer.example"
	writerID := s.authLocalID
	state := &nodeMembershipState{
		Schema: nodeMembershipSchema, Revision: 2, WriterID: "old-writer",
		Grants: map[string]*nodeJoinGrant{},
		Nodes:  map[string]*nodeMember{writerID: {NodeID: writerID, Name: "shell:new-writer"}},
	}
	if err := s.promoteQuorumMembership(state); err != nil {
		t.Fatal(err)
	}
	if state.WriterID != writerID || state.Epoch != 2 || state.Revision != 4 {
		t.Fatalf("writer/epoch/revision = %s/%d/%d", state.WriterID, state.Epoch, state.Revision)
	}
}

func TestQuorumPromotionRejectsEpochAndRevisionOverflow(t *testing.T) {
	for _, tc := range []struct{ epoch, revision uint64 }{
		{0, ^uint64(0)}, {0, ^uint64(0) - 1}, {^uint64(0), 2}, {1, ^uint64(0)},
	} {
		s := membershipWriterNode(t, "new-writer")
		s.cfg.NodePublicURL = "https://new-writer.example"
		id := s.authLocalID
		state := &nodeMembershipState{Schema: nodeMembershipSchema, Epoch: tc.epoch, Revision: tc.revision,
			WriterID: "old-writer", Nodes: map[string]*nodeMember{id: {NodeID: id}}}
		if err := s.promoteQuorumMembership(state); err == nil {
			t.Fatalf("accepted epoch=%d revision=%d", tc.epoch, tc.revision)
		}
	}
}

func TestQuorumMaterializationKeepsCurrentLeaderLeases(t *testing.T) {
	s := membershipWriterNode(t, "writer-node")
	s.cfg.NodePublicURL = "https://writer.example"
	writerID := s.authLocalID
	expires := time.Now().Add(time.Minute)
	prior := &nodeMember{NodeID: "peer", Name: "shell:peer", KeyHash: "same-key", Generation: 7,
		State: nodeStateReady, LeaseExpires: expires, LastSeen: time.Now()}
	s.membership = &nodeMembershipState{Schema: nodeMembershipSchema, Revision: 2, Epoch: 1, WriterID: writerID,
		Grants: map[string]*nodeJoinGrant{}, Nodes: map[string]*nodeMember{writerID: {NodeID: writerID}, "peer": prior}}
	state := &nodeMembershipState{Schema: nodeMembershipSchema, Revision: 3, Epoch: 1, WriterID: writerID,
		Grants: map[string]*nodeJoinGrant{}, Nodes: map[string]*nodeMember{
			writerID: {NodeID: writerID}, "peer": {NodeID: "peer", Name: "shell:peer", KeyHash: "same-key", Generation: 7, State: nodeStateOffline},
		}}
	if err := s.promoteQuorumMembership(state); err != nil {
		t.Fatal(err)
	}
	if peer := state.Nodes["peer"]; peer.State != nodeStateReady || !peer.LeaseExpires.Equal(expires) {
		t.Fatalf("current leader lease lost: state=%s expiry=%s", peer.State, peer.LeaseExpires)
	}
}

func TestQuorumMaterializationRejectsStaleLeaseIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, key, state string
		epoch            uint64
	}{
		{"new epoch", "same-key", nodeStateOffline, 2},
		{"different identity", "different-key", nodeStateOffline, 1},
		{"disabled", "same-key", nodeStateDisabled, 1},
		{"draining", "same-key", nodeStateDraining, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := membershipWriterNode(t, "writer-node")
			s.cfg.NodePublicURL = "https://writer.example"
			writerID := s.authLocalID
			prior := &nodeMember{NodeID: "peer", KeyHash: "same-key", Generation: 1, State: nodeStateReady, LeaseExpires: time.Now().Add(time.Minute)}
			s.membership = &nodeMembershipState{Schema: nodeMembershipSchema, Revision: 2, Epoch: 1, WriterID: writerID,
				Grants: map[string]*nodeJoinGrant{}, Nodes: map[string]*nodeMember{writerID: {NodeID: writerID}, "peer": prior}}
			state := &nodeMembershipState{Schema: nodeMembershipSchema, Revision: 3, Epoch: tc.epoch, WriterID: writerID,
				Grants: map[string]*nodeJoinGrant{}, Nodes: map[string]*nodeMember{
					writerID: {NodeID: writerID}, "peer": {NodeID: "peer", KeyHash: tc.key, Generation: 1, State: tc.state},
				}}
			if err := s.promoteQuorumMembership(state); err != nil {
				t.Fatal(err)
			}
			if peer := state.Nodes["peer"]; peer.State != tc.state || !peer.LeaseExpires.IsZero() {
				t.Fatalf("stale lease retained for %s", tc.name)
			}
		})
	}
}
