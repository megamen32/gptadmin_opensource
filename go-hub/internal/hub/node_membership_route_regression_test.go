package hub

import (
	"testing"
	"time"
)

func TestMembershipRoutesUseHeartbeatWriterURLWithoutEndpoints(t *testing.T) {
	const writerID = "writer-identity"
	const writerURL = "http://127.0.0.1:9191"
	member, _, _ := membershipMemberNode(t, "member-a", writerURL, writerID)
	member.membershipView = &nodeMembershipView{Nodes: []*nodeMember{
		{NodeID: writerID, Name: "shell:writer", State: nodeStateReady, LeaseExpires: time.Now().Add(time.Minute)},
		{NodeID: "other-node", Name: "shell:other", State: nodeStateReady, LeaseExpires: time.Now().Add(time.Minute)},
	}}
	member.rebuildMembershipRoutes()
	route, found := member.membershipRoutes["shell:writer"]
	if !found || route.URL != writerURL {
		t.Fatalf("writer route = %#v, found = %v", route, found)
	}
	if _, found := member.membershipRoutes["shell:other"]; found {
		t.Fatal("member without an endpoint acquired a route")
	}
}
