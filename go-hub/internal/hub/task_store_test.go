package hub

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTaskStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := New(Config{ConfigDir: dir})
	s1.mu.Lock()
	now := nowFloat()
	s1.shellJobs["done-1"] = &shellJob{ID: "done-1", Server: "host", ToolName: "shell_exec", CreatedAt: now - 3, StartedAt: now - 2, DoneAt: now - 1, Status: "completed", Result: map[string]any{"_spilled": true, "stdout_path": "/tmp/spool/out", "stdout": "tail"}}
	s1.shellJobs["queued-1"] = &shellJob{ID: "queued-1", Server: "host", ToolName: "shell_exec", CreatedAt: now, Status: "queued"}
	s1.shellJobs["cancel-1"] = &shellJob{ID: "cancel-1", Server: "host", ToolName: "shell_exec", CreatedAt: now - 3, StartedAt: now - 2, DoneAt: now - 1, Status: "cancelled"}
	s1.shellJobs["input-1"] = &shellJob{ID: "input-1", Server: "host", ToolName: "shell_exec", CreatedAt: now, Status: "input_required", ApprovalID: "approval-1", Arguments: map[string]any{"cmd": "must-not-persist"}}
	if err := s1.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	s1.mu.Unlock()

	s2 := New(Config{ConfigDir: dir})
	if got := s2.shellJobs["done-1"]; got == nil || got.Status != "completed" || mapValue(got.Result)["stdout_path"] != "/tmp/spool/out" {
		t.Fatalf("completed task not restored: %#v", got)
	}
	if got := s2.shellJobs["queued-1"]; got == nil || got.Status != "failed" {
		t.Fatalf("queued task must fail closed after restart: %#v", got)
	}
	if controls := s2.shellControls["host"]; len(controls) != 1 || controls[0].TaskID != "cancel-1" {
		t.Fatalf("cancel intent not restored: %v", controls)
	}
	if got := s2.shellJobs["input-1"]; got == nil || got.Status != "failed" || firstString(mapValue(got.Error), "code") != "hub_restarted_during_input_required" {
		t.Fatalf("input_required task must fail closed after restart: %#v", got)
	}
	if _, err := filepath.Abs(s2.taskStatePath()); err != nil {
		t.Fatal(err)
	}
}

func TestTaskBackedIdempotencySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := New(Config{ConfigDir: dir})
	done := make(chan struct{})
	close(done)
	s1.mu.Lock()
	s1.shellJobs["job-idem"] = &shellJob{ID: "job-idem", Server: "host", CreatedAt: nowFloat(), Status: "completed"}
	s1.idempotency["scope:key"] = &idempotencyEntry{Fingerprint: "fp", CreatedAt: time.Now(), Done: done, JobID: "job-idem", Response: map[string]any{"server_id": "shell:host", "status": "running", "background": true, "job_id": "job-idem"}, Status: 200}
	if err := s1.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	s1.mu.Unlock()

	s2 := New(Config{ConfigDir: dir})
	entry := s2.idempotency["scope:key"]
	if entry == nil || entry.JobID != "job-idem" || firstString(entry.Response, "job_id") != "job-idem" || entry.Status != 200 {
		t.Fatalf("idempotency not restored: %#v", entry)
	}
	select {
	case <-entry.Done:
	default:
		t.Fatal("restored idempotency entry must be completed")
	}
}

func TestTaskGroupSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := New(Config{ConfigDir: dir})
	now := nowFloat()
	s1.mu.Lock()
	s1.shellJobs["child"] = &shellJob{ID: "child", Server: "host", ToolName: "shell_exec", CreatedAt: now - 2, DoneAt: now - 1, Status: "completed", ParentTaskID: "parent"}
	s1.relayJobs["parent"] = &relayJob{ID: "parent", AgentID: "hub", Method: "task/group", Params: map[string]any{"label": "fleet", "children": []any{"child"}}, CreatedAt: now - 3, Status: "running"}
	if err := s1.saveTaskStateLocked(); err != nil {
		s1.mu.Unlock()
		t.Fatal(err)
	}
	s1.mu.Unlock()

	s2 := New(Config{ConfigDir: dir})
	parent := s2.relayJobs["parent"]
	child := s2.shellJobs["child"]
	if parent == nil || parent.Method != "task/group" || len(taskGroupChildIDs(parent)) != 1 {
		t.Fatalf("parent=%#v", parent)
	}
	if child == nil || child.ParentTaskID != "parent" {
		t.Fatalf("child=%#v", child)
	}
	got, rpcErr := s2.mcpTaskGet("parent", "hub")
	if rpcErr != nil {
		t.Fatalf("get err=%v", rpcErr)
	}
	if mapValue(got)["status"] != "completed" {
		t.Fatalf("group=%v", got)
	}
}
