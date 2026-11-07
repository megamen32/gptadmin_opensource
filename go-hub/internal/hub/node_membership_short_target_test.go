package hub

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestMembershipOnlyShortTargetPinsAuthenticatedFingerprint(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	_, key := joinedMember44ForReconcile(t, writer, "stable-shell-id")
	heartbeat := func(token, fp string) int {
		body, _ := json.Marshal(map[string]any{"state": nodeStateReady, "snapshot_revision": writer.writerAuthGeneration(), "local_name": "shell:node-44", "local_id": "stable-shell-id", "local_fingerprint": fp})
		return membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/heartbeat", token, body).Code
	}
	if got := heartbeat(key, "key-A"); got != 200 {
		t.Fatalf("heartbeat: %d", got)
	}
	local := New(Config{})
	defer local.Close()
	local.agents["shell:node-44"] = &Agent{AgentID: "shell:node-44", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"server_id": "stable-shell-id", "fingerprint": "key-A"}}
	alias := local.shortTarget("shell:node-44")
	if got := writer.shortTarget("shell:node-44"); got != alias {
		t.Fatalf("membership alias %s != local %s", got, alias)
	}
	if _, status, _ := writer.selectMCPRelayTarget(alias); status != 200 {
		t.Fatalf("central alias resolution: %d", status)
	}
	if got := heartbeat(key, "key-B"); got != 403 {
		t.Fatalf("replacement fingerprint accepted: %d", got)
	}
	if got := heartbeat("unrelated-key", "key-A"); got != 401 {
		t.Fatalf("unauthenticated fingerprint accepted: %d", got)
	}
	if got := heartbeat(key, ""); got != 200 {
		t.Fatalf("legacy heartbeat: %d", got)
	}
	if got := writer.shortTarget("shell:node-44"); got != alias {
		t.Fatal("legacy heartbeat lost pinned alias")
	}
}

func TestMemberLocalFingerprintComesFromPinnedExecutor(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	s.localExecutors = map[string]map[string]string{}
	s.localExecutors["shell:pinned"] = map[string]string{"server_id": "persisted-id", "fingerprint": "pinned-key"}
	s.agents["shell:pinned"] = &Agent{AgentID: "shell:pinned", Meta: map[string]any{"hostname": "renamed", "fingerprint": "untrusted-display"}}
	name, id, fp := s.memberLocalExecutorIdentity()
	if name != "shell:pinned" || id != "persisted-id" || fp != "pinned-key" {
		t.Fatalf("untrusted identity: %q %q %q", name, id, fp)
	}
}
