package hub

import (
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
		FailureDomain: "node-44", HubURL: "https://quorum44-node.example",
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
