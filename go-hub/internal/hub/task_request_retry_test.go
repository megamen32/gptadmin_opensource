package hub

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestArchitectureSameKeyRetriesKnownUncommittedFailure(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`CREATE TRIGGER reject_enqueue BEFORE INSERT ON task_records WHEN NEW.kind IN ('shell','relay') BEGIN SELECT RAISE(ABORT,'temporary enqueue failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/mcp-relay/call", nil)
	args := map[string]any{"cmd": "fixture-not-executed"}
	failed, code := s.executeMCPTool(request, "shell:host", "shell_exec", args, true, time.Second, "retry-after-write-failure")
	if code != 503 || len(s.shellQueues["host"]) != 0 {
		t.Fatalf("unexpected failed enqueue: %d %v", code, failed)
	}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error { _, err := tx.tx.Exec(`DROP TRIGGER reject_enqueue`); return err }); err != nil {
		t.Fatal(err)
	}
	retried, code := s.executeMCPTool(request, "shell:host", "shell_exec", args, true, time.Second, "retry-after-write-failure")
	if code != 200 || firstString(retried, "job_id") == "" || len(s.shellQueues["host"]) != 1 {
		t.Fatalf("known uncommitted failure stuck in replay cache: %d %v", code, retried)
	}
}

func TestArchitectureTransientResponseFailureReplaysAssociatedTask(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	id := ""
	response, code := s.executeDurableTaskRequest("associated-key", "fp", func() (map[string]any, int) {
		created := s.callShellToolWithTraceParentAndSecrets("shell:host", "shell_exec", map[string]any{"cmd": "fixture"}, true, time.Second, "", "", nil, "associated-key")
		id = firstString(created, "job_id")
		return taskPersistenceFailure(errors.New("temporary result-read failure")), 503
	})
	if id == "" || code != 503 {
		t.Fatalf("fixture failure: %d %v", code, response)
	}
	called := false
	replay, code := s.executeDurableTaskRequest("associated-key", "fp", func() (map[string]any, int) { called = true; return nil, 200 })
	if called || code != 200 || firstString(replay, "job_id") != id {
		t.Fatalf("associated task hidden by cached temporary error: %d %v executed=%v", code, replay, called)
	}
}
