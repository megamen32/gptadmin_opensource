package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStandbyStartupDoesNotFailLiveOwnersQueuedTask(t *testing.T) {
	dir := t.TempDir()
	primary := New(Config{ConfigDir: dir})
	response := primary.callShellTool("shell:host", "shell_exec", map[string]any{"cmd": "echo owned"}, true, time.Second)
	id := firstString(response, "job_id")
	if id == "" {
		t.Fatalf("missing job: %v", response)
	}
	standby := New(Config{ConfigDir: dir})
	state, err := standby.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Shell[id].Status != "queued" {
		t.Fatalf("standby corrupted live primary queue: %s", state.Shell[id].Status)
	}
	w := httptest.NewRecorder()
	primary.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/shell/queue/host?timeout=1", nil), "host")
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("owner lost dispatch: %d %s", w.Code, w.Body.String())
	}
}

func TestShellEnqueueFailureDoesNotPublishTask(t *testing.T) {
	s := New(Config{})
	blocked := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.ConfigDir = blocked
	response := s.callShellTool("shell:host", "shell_exec", map[string]any{"cmd": "echo must-not-run"}, true, time.Second)
	if firstString(response, "status") != "failed" {
		t.Errorf("uncommitted enqueue returned success: %v", response)
	}
	if len(s.shellQueues["host"]) != 0 {
		t.Fatal("uncommitted task escaped to execution queue")
	}
}

func TestCancellationFailureRestoresRunningTaskAndControls(t *testing.T) {
	s := New(Config{})
	blocked := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.ConfigDir = blocked
	job := &shellJob{ID: "running", Server: "host", Status: "running", CreatedAt: nowFloat() - 2, StartedAt: nowFloat() - 1}
	s.shellJobs[job.ID] = job
	if _, rpcErr := s.mcpTaskCancel(job.ID, "hub"); rpcErr == nil {
		t.Error("uncommitted cancellation was acknowledged")
	}
	if job.Status != "running" || len(s.shellControls["host"]) != 0 {
		t.Fatalf("failed cancellation leaked into memory/controls: %s %v", job.Status, s.shellControls)
	}
}

func TestCancellationOnStandbyCannotBeOverwrittenByLatePrimaryResult(t *testing.T) {
	dir := t.TempDir()
	primary := New(Config{ConfigDir: dir})
	defer primary.Close()
	out := primary.callShellTool("shell:host", "shell_exec", map[string]any{"cmd": "echo result"}, true, time.Second)
	id := firstString(out, "job_id")
	poll := httptest.NewRecorder()
	primary.pollShellQueue(poll, httptest.NewRequest(http.MethodGet, "/shell/queue/host?timeout=1", nil), "host")
	standby := New(Config{ConfigDir: dir})
	defer standby.Close()
	if _, err := standby.mcpTaskCancel(id, "hub"); err != nil {
		t.Fatal(err)
	}
	result := httptest.NewRecorder()
	primary.shellQueueResult(result, httptest.NewRequest(http.MethodPost, "/result", strings.NewReader(`{"id":"`+id+`","result":"too late"}`)), "host")
	if result.Code != 200 {
		t.Fatalf("result status: %d %s", result.Code, result.Body.String())
	}
	state, err := primary.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Shell[id].Status != "cancelled" {
		t.Fatal("late result resurrected cancellation")
	}
}

func TestResultCommitFailureRollsBackMemoryAndCanRetry(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	j := &shellJob{ID: "retry", Server: "host", Status: "running", CreatedAt: nowFloat(), StartedAt: nowFloat()}
	s.shellJobs[j.ID] = j
	bad := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(bad, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.ConfigDir = bad
	w := httptest.NewRecorder()
	s.shellQueueResult(w, httptest.NewRequest(http.MethodPost, "/result", strings.NewReader(`{"id":"retry","result":"one"}`)), "host")
	if w.Code != 503 || j.Status != "running" {
		t.Fatalf("failed commit leaked: %d %s", w.Code, j.Status)
	}
	s.cfg.ConfigDir = t.TempDir()
	w = httptest.NewRecorder()
	s.shellQueueResult(w, httptest.NewRequest(http.MethodPost, "/result", strings.NewReader(`{"id":"retry","result":"one"}`)), "host")
	if w.Code != 200 || j.Status != "completed" {
		t.Fatalf("retry failed: %d %s", w.Code, j.Status)
	}
}

func TestSameIdempotencyKeyAcrossLiveHubsCreatesOneTask(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{ConfigDir: dir})
	defer a.Close()
	b := New(Config{ConfigDir: dir})
	defer b.Close()
	request := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	x, xs := a.executeMCPTool(request, "shell:host", "shell_exec", map[string]any{"cmd": "echo once"}, true, time.Second, "same-key")
	y, ys := b.executeMCPTool(request, "shell:host", "shell_exec", map[string]any{"cmd": "echo once"}, true, time.Second, "same-key")
	if xs != 200 || ys != 200 || firstString(x, "job_id") == "" || firstString(x, "job_id") != firstString(y, "job_id") {
		t.Fatalf("duplicate task: %d %v ; %d %v", xs, x, ys, y)
	}
	state, err := a.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Shell) != 1 {
		t.Fatalf("task count=%d", len(state.Shell))
	}
}

func TestArchitectureCancellationIsDeliveredByAnotherHubAndRetried(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{ConfigDir: dir})
	defer a.Close()
	response := a.callShellTool("shell:host", "shell_exec", map[string]any{"cmd": "fixture-not-executed"}, true, time.Second)
	id := firstString(response, "job_id")
	poll := httptest.NewRecorder()
	a.pollShellQueue(poll, httptest.NewRequest(http.MethodGet, "/queue/host?timeout=1", nil), "host")
	b := New(Config{ConfigDir: dir})
	defer b.Close()
	if _, err := b.mcpTaskCancel(id, "hub"); err != nil {
		t.Fatal(err)
	}
	first, err := a.nextDurableTaskControlLocked("host")
	if err != nil || first != id {
		t.Fatalf("cross-Hub control: %q %v", first, err)
	}
	next, err := a.nextDurableTaskControlLocked("host")
	if err != nil || next != "" {
		t.Fatalf("control must back off: %q %v", next, err)
	}
	if err := a.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`UPDATE task_controls SET next_attempt=0 WHERE task_id=?`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	retried, err := b.nextDurableTaskControlLocked("host")
	if err != nil || retried != id {
		t.Fatalf("lost delivery not retried: %q %v", retried, err)
	}
	result := httptest.NewRecorder()
	a.shellQueueResult(result, httptest.NewRequest(http.MethodPost, "/queue/host/result", strings.NewReader(`{"id":"`+id+`","result":"cancelled by worker"}`)), "host")
	if result.Code != 200 {
		t.Fatalf("acknowledgement: %d", result.Code)
	}
	if err := a.withTaskDatabase(func(tx *taskTransaction) error {
		var count int
		err := tx.tx.QueryRow(`SELECT count(*) FROM task_controls WHERE task_id=?`, id).Scan(&count)
		if count != 0 {
			t.Error("acknowledged control retained")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArchitectureUnknownRequestOutcomeIsNotExecutedAgain(t *testing.T) {
	dir := t.TempDir()
	owner := New(Config{ConfigDir: dir})
	decision, err := owner.reserveDurableTaskRequest("key", "fp")
	if err != nil || decision.replay {
		t.Fatalf("reserve: %v %v", decision, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	b := New(Config{ConfigDir: dir})
	defer b.Close()
	called := false
	response, status := b.executeDurableTaskRequest("key", "fp", func() (map[string]any, int) { called = true; return map[string]any{}, 200 })
	if called || status != 409 || firstString(mapValue(response["error"]), "code") != "task_outcome_unknown" {
		t.Fatalf("uncertain operation repeated: %v %d %v", called, status, response)
	}
}

func TestArchitecturePendingRequestFindsAtomicallyLinkedTask(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{ConfigDir: dir})
	defer a.Close()
	if _, err := a.reserveDurableTaskRequest("pending-key", "fp"); err != nil {
		t.Fatal(err)
	}
	first := a.callShellToolWithTraceParentAndSecrets("shell:host", "shell_exec", map[string]any{"cmd": "fixture"}, true, time.Second, "", "", nil, "pending-key")
	id := firstString(first, "job_id")
	if id == "" {
		t.Fatal(first)
	}
	b := New(Config{ConfigDir: dir})
	defer b.Close()
	called := false
	response, status := b.executeDurableTaskRequest("pending-key", "fp", func() (map[string]any, int) { called = true; return nil, 200 })
	if called || status != 200 || firstString(response, "job_id") != id {
		t.Fatalf("association lost: %v %d %v", called, status, response)
	}
}

func TestArchitectureSQLiteDurabilityVersion(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		var version, mode string
		var sync int
		if err := tx.tx.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
			return err
		}
		if err := tx.tx.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
			return err
		}
		if err := tx.tx.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
			return err
		}
		if mode != "wal" || sync != 2 {
			t.Errorf("durability changed: %s %d", mode, sync)
		}
		t.Logf("SQLite=%s journal=%s synchronous=%d", version, mode, sync)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
