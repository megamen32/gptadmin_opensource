package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemberReplicatesInstructionSetChanges(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	writer.cfg.NodePublicURL = writerServer.URL
	writer.ensureWriterSelfEntry()
	grant := membershipIssueGrant(t, writer, "shell:member-a")
	member, id, key := membershipMemberNode(t, "member-a", writerServer.URL, "")
	memberServer := httptest.NewServer(member.Handler())
	defer memberServer.Close()
	if joined := membershipJoin(t, writer, grant, id, key, "shell:member-a", memberServer.URL); joined["__status"] != http.StatusOK {
		t.Fatalf("join failed: %v", joined)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	member.membershipMemberTick(client, true)
	for _, content := range []string{"first instruction", "updated instruction"} {
		set := InstructionSet{ID: "replication-check", Content: content, Version: instructionSetVersion(content)}
		if err := writer.updateInstructionSets(func(sets map[string]InstructionSet) error { sets[set.ID] = set; return nil }); err != nil {
			t.Fatal(err)
		}
		member.membershipMemberTick(client, false)
		got, found := member.namedInstructionSetSnapshot(set.ID)
		if !found || got.Content != content {
			t.Fatalf("instruction not replicated: found=%t got=%q want=%q", found, got.Content, content)
		}
	}
	// Force a real filesystem error, then verify admission remains closed and
	// the same snapshot is retried successfully after the obstruction clears.
	oldPath := member.cfg.InstructionSetsStateFile
	blockedPath := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(blockedPath, 0700); err != nil {
		t.Fatal(err)
	}
	member.cfg.InstructionSetsStateFile = blockedPath
	content := "recover after write failure"
	set := InstructionSet{ID: "replication-check", Content: content, Version: instructionSetVersion(content)}
	if err := writer.updateInstructionSets(func(sets map[string]InstructionSet) error { sets[set.ID] = set; return nil }); err != nil {
		t.Fatal(err)
	}
	member.membershipMemberTick(client, false)
	if !member.authState.Pending {
		t.Fatal("write failure did not close admission")
	}
	member.cfg.InstructionSetsStateFile = oldPath
	member.membershipMemberTick(client, false)
	got, found := member.namedInstructionSetSnapshot(set.ID)
	if member.authState.Pending || !found || got.Content != content {
		t.Fatalf("retry failed: pending=%t found=%t", member.authState.Pending, found)
	}
}

func TestMemberBootstrapAcceptsProfileWithNewInstructionSet(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	set := InstructionSet{ID: "ops", Content: "operate safely", Version: instructionSetVersion("operate safely")}
	if err := writer.updateInstructionSets(func(sets map[string]InstructionSet) error { sets[set.ID] = set; return nil }); err != nil {
		t.Fatal(err)
	}
	profile := map[string]any{"id": "ops", "name": "Operations", "instruction_set_id": "ops", "access_mode": accessModeFull, "approval_mode": approvalModeBoundedAutonomous}
	payload, _ := json.Marshal(profile)
	req := httptest.NewRequest(http.MethodPut, "/admin/api/access-profiles/ops", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer fixture-owner")
	req.Header.Set("If-Match", "*")
	res := httptest.NewRecorder()
	writer.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("profile creation failed: %d %s", res.Code, res.Body.String())
	}
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	writer.cfg.NodePublicURL = writerServer.URL
	writer.ensureWriterSelfEntry()
	grant := membershipIssueGrant(t, writer, "shell:member-a")
	member, id, key := membershipMemberNode(t, "member-a", writerServer.URL, "")
	memberServer := httptest.NewServer(member.Handler())
	defer memberServer.Close()
	if joined := membershipJoin(t, writer, grant, id, key, "shell:member-a", memberServer.URL); joined["__status"] != http.StatusOK {
		t.Fatalf("join failed: %v", joined)
	}
	member.membershipMemberTick(&http.Client{Timeout: 5 * time.Second}, true)
	got, ok := member.namedInstructionSetSnapshot("ops")
	if !ok || got.Content != set.Content || member.writerAuthGeneration() != writer.writerAuthGeneration() {
		t.Fatalf("bootstrap failed: set=%t member=%d writer=%d", ok, member.writerAuthGeneration(), writer.writerAuthGeneration())
	}
}
