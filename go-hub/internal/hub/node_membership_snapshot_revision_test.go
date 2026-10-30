package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestControlSnapshotAdvancesAfterOAuthClientMutation(t *testing.T) {
	writer, member, _, nodeKey := bearerAdmissionNodes(t)
	read := func() controlSnapshot {
		response := membershipHTTP(writer, http.MethodGet, "/admin/api/nodes/control-snapshot", nodeKey, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("snapshot: %d", response.Code)
		}
		var snapshot controlSnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	before := read()
	writer.mu.Lock()
	writer.oauthClients["new-client"] = oauthClientMetadata{CreatedAt: time.Now().Unix(), ProfileID: "fixture"}
	err := writer.saveOAuthClientsStateLocked()
	writer.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.Revision <= before.Revision {
		t.Fatalf("changed control snapshot reused revision %d", after.Revision)
	}
	if _, ok := after.Bundle.Clients.Clients["new-client"]; !ok {
		t.Fatal("new client missing")
	}
	if again := read(); again.Revision != after.Revision {
		t.Fatal("unchanged authority advanced repeatedly")
	}
	member.membershipMemberTick(&http.Client{Timeout: 5 * time.Second}, true)
	member.mu.Lock()
	_, present := member.oauthClients["new-client"]
	revision := member.authState.Generation
	member.mu.Unlock()
	if !present || revision != after.Revision {
		t.Fatalf("reader did not apply changed client at generation %d", after.Revision)
	}

	_, issued, err := writer.issueOAuthRefreshToken("new-client", writer.cfg.MCPResource, "gptadmin.read")
	if err != nil {
		t.Fatal(err)
	}
	withToken := read()
	if withToken.Revision <= after.Revision {
		t.Fatal("new refresh token did not advance generation")
	}
	if _, ok := withToken.Bundle.Tokens.Tokens[issued.ID]; !ok {
		t.Fatal("refresh token missing from replica snapshot")
	}
	_, err = writer.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
		profiles["second-profile"] = AccessProfile{ID: "second-profile", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"shell:member-a"}, AllowedTools: []string{"shell_exec"}, Version: 1, UpdatedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	withProfile := read()
	if withProfile.Revision <= withToken.Revision {
		t.Fatal("profile mutation did not advance generation")
	}
	member.membershipMemberTick(&http.Client{Timeout: 5 * time.Second}, true)
	member.mu.Lock()
	_, tokenPresent := member.managedMCP[issued.ID]
	_, profilePresent := member.accessProfiles["second-profile"]
	member.mu.Unlock()
	if !tokenPresent || !profilePresent {
		t.Fatal("reader did not apply token and access profile")
	}
}
