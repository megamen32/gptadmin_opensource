package hub

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestLocalPendingSurvivesOwnerRestartEncrypted(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir})
	owner, err := s.ensureTaskOwnerLocked()
	if err != nil {
		t.Fatal(err)
	}
	now := nowFloat()
	j := &shellJob{ID: "durable", OwnerID: owner, Server: "host", Status: "queued", ToolName: "shell_exec", CreatedAt: now, Cmd: "printf secret-canary", Caller: &taskCaller{ClientID: "client"}, FilesystemACL: &filesystemACLPolicy{ProfileID: "profile"}}
	s.shellJobs[j.ID] = j
	if err := s.saveTaskStateLocked(j.ID); err != nil {
		t.Fatal(err)
	}
	s.EnableNodeDrain("test")
	if !s.NodeDrainStatus().ReadyToStop {
		t.Error("durably queued task blocks drain")
	}
	s.Close()
	raw, _ := os.ReadFile(s.taskDatabasePath())
	if strings.Contains(string(raw), "printf secret-canary") {
		t.Fatal("plaintext pending input")
	}
	next := New(Config{ConfigDir: dir})
	defer next.Close()
	if next.shellJobs[j.ID].Status != "queued" {
		t.Fatalf("lost queued task: %s", next.shellJobs[j.ID].Status)
	}
	if err := next.recoverQuorumPendingShellLocked(context.Background(), "host"); err != nil {
		t.Fatal(err)
	}
	got := next.shellJobs[j.ID]
	if got.Cmd != j.Cmd || got.CreatedAt != now || got.RequestKey != j.RequestKey || got.Caller.ClientID != "client" || got.FilesystemACL.ProfileID != "profile" || len(next.shellQueues["host"]) != 1 {
		t.Fatalf("pending restore cmd=%q time=%v/%v caller=%v acl=%v queue=%v owner=%s", got.Cmd, got.CreatedAt, now, got.Caller, got.FilesystemACL, next.shellQueues["host"], got.OwnerID)
	}
}

func TestLocalPendingPreservesTTLAndNeverReplaysStarted(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir})
	owner, _ := s.ensureTaskOwnerLocked()
	created := nowFloat() - 10801
	s.shellJobs["expired"] = &shellJob{ID: "expired", Server: "host", OwnerID: owner, CreatedAt: created, Status: "queued", Cmd: "must-not-run"}
	s.shellJobs["started"] = &shellJob{ID: "started", Server: "host", OwnerID: owner, CreatedAt: nowFloat() - 10, StartedAt: nowFloat() - 9, Status: "running", Cmd: "must-not-replay"}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	n := New(Config{ConfigDir: dir})
	defer n.Close()
	if err := n.recoverQuorumPendingShellLocked(context.Background(), "host"); err != nil {
		t.Fatal(err)
	}
	if n.shellJobs["expired"].CreatedAt != created {
		t.Fatal("age reset")
	}
	if ok, err := n.expireQueuedRequestLocked("expired", created); err != nil || !ok {
		t.Fatalf("expiry %v %v", ok, err)
	}
	if n.shellJobs["expired"].Status != "cancelled" || n.shellJobs["started"].Status != "running" {
		t.Fatal("wrong lifecycle")
	}
	for _, id := range n.shellQueues["host"] {
		if id == "started" {
			t.Fatal("running replayed")
		}
	}
	n.shellJobs["started"].OwnerID = n.taskOwner.id
	n.EnableNodeDrain("test")
	if n.NodeDrainStatus().ReadyToStop {
		t.Fatal("started task bypassed drain")
	}
}

func TestLocalPendingLiveOwnerAndTamperFailClosed(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir})
	defer s.Close()
	owner, _ := s.ensureTaskOwnerLocked()
	j := &shellJob{ID: "owned", Server: "host", OwnerID: owner, CreatedAt: nowFloat(), Status: "queued", Cmd: "private"}
	s.shellJobs[j.ID] = j
	if err := s.saveTaskStateLocked(j.ID); err != nil {
		t.Fatal(err)
	}
	other := New(Config{ConfigDir: dir})
	defer other.Close()
	if err := other.recoverQuorumPendingShellLocked(context.Background(), "host"); err != nil {
		t.Fatal(err)
	}
	if len(other.shellQueues["host"]) != 0 {
		t.Fatal("live owner stolen")
	}
	st, _ := s.readTaskRecords(j.ID)
	if _, err := s.openPendingValue(quorumPendingShellNamespace, "wrong-id", st.Shell[j.ID].SealedPending); err == nil {
		t.Fatal("ciphertext not bound to task")
	}
	j.Revision++
	s.EnableNodeDrain("test")
	if s.NodeDrainStatus().ReadyToStop {
		t.Fatal("uncommitted state bypassed drain")
	}
}
