package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Fail an actual SQLite write after all preliminary reads have succeeded.
// An invalid ConfigDir only tests read failure, not transaction rollback.
func rejectTaskWrites(t *testing.T, s *Server) {
	t.Helper()
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`CREATE TRIGGER fail_task_update BEFORE UPDATE ON task_records WHEN NEW.kind IN ('shell','relay') BEGIN SELECT RAISE(ABORT,'injected task write failure'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArchitectureCancellationRollbackAfterSuccessfulRead(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	job := &shellJob{ID: "running", Server: "host", Status: "running", CreatedAt: nowFloat() - 2, StartedAt: nowFloat() - 1}
	s.shellJobs[job.ID] = job
	if err := s.saveTaskStateLocked(job.ID); err != nil {
		t.Fatal(err)
	}
	rejectTaskWrites(t, s)
	if _, err := s.mcpTaskCancel(job.ID, "hub"); err == nil {
		t.Fatal("failed transaction acknowledged")
	}
	if job != s.shellJobs[job.ID] || job.Status != "running" || job.DoneAt != 0 || len(s.shellControls["host"]) != 0 {
		t.Fatalf("memory/control rollback failed: %+v %v", job, s.shellControls)
	}
	state, err := s.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Shell[job.ID].Status != "running" {
		t.Fatal("disk transition leaked")
	}
}

func TestArchitectureResultRollbackAfterSuccessfulRead(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	job := &shellJob{ID: "running", Server: "host", Status: "running", CreatedAt: nowFloat() - 2, StartedAt: nowFloat() - 1}
	s.shellJobs[job.ID] = job
	if err := s.saveTaskStateLocked(job.ID); err != nil {
		t.Fatal(err)
	}
	rejectTaskWrites(t, s)
	w := httptest.NewRecorder()
	s.shellQueueResult(w, httptest.NewRequest(http.MethodPost, "/result", strings.NewReader(`{"id":"running","result":"not committed"}`)), "host")
	if w.Code != 503 || job.Status != "running" || job.Result != nil || job.DoneAt != 0 {
		t.Fatalf("result rollback failed: HTTP %d %+v", w.Code, job)
	}
}

func TestArchitectureGroupCancellationIsAllOrNothing(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	now := nowFloat()
	s.shellJobs["a"] = &shellJob{ID: "a", Server: "host", Status: "running", CreatedAt: now - 2, StartedAt: now - 1, ParentTaskID: "parent"}
	s.shellJobs["b"] = &shellJob{ID: "b", Server: "host", Status: "running", CreatedAt: now - 2, StartedAt: now - 1, ParentTaskID: "parent"}
	group := &relayJob{ID: "parent", AgentID: "hub", Method: "task/group", Status: "running", CreatedAt: now - 3, Params: map[string]any{"children": []any{"a", "b"}}}
	s.relayJobs[group.ID] = group
	if err := s.saveTaskStateLocked(group.ID); err != nil {
		t.Fatal(err)
	}
	rejectTaskWrites(t, s)
	if _, err := s.mcpTaskCancel(group.ID, "hub"); err == nil {
		t.Fatal("failed group transaction acknowledged")
	}
	if group.Status != "running" || s.shellJobs["a"].Status != "running" || s.shellJobs["b"].Status != "running" || len(s.shellControls["host"]) != 0 {
		t.Fatal("partial group cancellation leaked")
	}
}

func TestArchitectureApprovalCommitFailureDoesNotConsumeOrDispatch(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	args := map[string]any{"ref": "fixture", "name": "tool"}
	payload, _ := json.Marshal(args)
	job := &shellJob{ID: "approval-task", Server: "host", ToolName: "mcp_call", Status: "input_required", CreatedAt: nowFloat(), Arguments: args, ApprovalID: "approved", ApprovalActor: "tester"}
	s.shellJobs[job.ID] = job
	approval := &approvalRequest{ID: job.ApprovalID, Actor: "tester", Target: "shell:host", Tool: "mcp_call", ArgumentsDigest: sha256Hex(payload), Status: "approved", ExpiresAt: time.Now().Add(time.Hour)}
	s.approvals[approval.ID] = approval
	if err := s.saveTaskStateLocked(job.ID); err != nil {
		t.Fatal(err)
	}
	rejectTaskWrites(t, s)
	input := map[string]any{"approval": map[string]any{"action": "accept", "content": map[string]any{"approved": true}}}
	if _, err := s.mcpTaskUpdate(nil, job.ID, input, "hub"); err == nil {
		t.Error("failed approval transaction acknowledged")
	}
	if approval.Status != "approved" || job.Status != "input_required" || job.ApprovalID != "approved" || len(s.shellQueues["host"]) != 0 {
		t.Fatalf("approval/dispatch leaked: approval=%s job=%s queue=%v", approval.Status, job.Status, s.shellQueues)
	}
}

func TestArchitectureCancelledRelayWaitIsTerminal(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	s.relayJobs["cancelled"] = &relayJob{ID: "cancelled", Status: "cancelled"}
	response := s.waitRelay("cancelled", 0)
	if firstString(response, "status") != "cancelled" {
		t.Fatalf("cancelled relay still running: %v", response)
	}
}

func TestArchitectureActiveRequestOutlivesReplayTTL(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	if _, err := s.reserveDurableTaskRequest("long-request", "fp"); err != nil {
		t.Fatal(err)
	}
	now := nowFloat()
	job := &shellJob{ID: "long-running", RequestKey: "long-request", Server: "host", Status: "running", CreatedAt: now - idempotencyTTL.Seconds() - 3600, StartedAt: now - 5}
	s.shellJobs[job.ID] = job
	if err := s.saveTaskStateLocked(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		if err := tx.finishRequest("long-request", map[string]any{"job_id": job.ID, "status": "running"}, 200); err != nil {
			return err
		}
		_, err := tx.tx.Exec(`UPDATE task_requests SET created=? WHERE key=?`, now-idempotencyTTL.Seconds()-3600, "long-request")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.reserveDurableTaskRequest("another-request", "fp2"); err != nil {
		t.Fatal(err)
	}
	job.Status = "completed"
	job.DoneAt = nowFloat()
	job.Result = "long result"
	if err := s.saveTaskStateLocked(job.ID); err != nil {
		t.Fatalf("live task lost its reservation when TTL elapsed: %v", err)
	}
}

func TestArchitectureMaintenanceDoesNotExpireLiveForeignOwner(t *testing.T) {
	dir := t.TempDir()
	owner := New(Config{ConfigDir: dir})
	defer owner.Close()
	id, err := owner.ensureTaskOwnerLocked()
	if err != nil {
		t.Fatal(err)
	}
	now := nowFloat()
	job := &shellJob{ID: "foreign-long-task", OwnerID: id, Server: "host", Status: "running", CreatedAt: now - 7201, StartedAt: now - 7200, Timeout: 10000}
	owner.shellJobs[job.ID] = job
	if err := owner.saveTaskStateLocked(job.ID); err != nil {
		t.Fatal(err)
	}
	standby := New(Config{ConfigDir: dir})
	defer standby.Close()
	if expired := standby.expireOrphanedShellJobsLocked(); len(expired) > 0 {
		t.Fatalf("live foreign owner's task expired: %v", expired)
	}
	if standby.shellJobs[job.ID].Status != "running" {
		t.Fatal("standby damaged a live task")
	}
}

func TestArchitectureMaintenanceRollbackAndRetry(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	now := nowFloat()
	job := &shellJob{ID: "orphan", Server: "host", Status: "running", CreatedAt: now - 1001, StartedAt: now - 1000}
	s.shellJobs[job.ID] = job
	if err := s.saveTaskStateLocked(job.ID); err != nil {
		t.Fatal(err)
	}
	rejectTaskWrites(t, s)
	if _, err := s.maintainTaskStateLocked(); err == nil {
		t.Fatal("failed maintenance commit accepted")
	}
	if job.Status != "running" || len(s.shellControls["host"]) != 0 {
		t.Fatal("failed maintenance changed memory/controls")
	}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error { _, err := tx.tx.Exec(`DROP TRIGGER fail_task_update`); return err }); err != nil {
		t.Fatal(err)
	}
	expired, err := s.maintainTaskStateLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || job.Status != "failed" || len(s.shellControls["host"]) != 1 {
		t.Fatalf("maintenance retry failed: %v %+v", expired, job)
	}
}

func TestArchitectureStandbyRecoversExitedOwnerWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	owner := New(Config{ConfigDir: dir})
	out := owner.callShellTool("shell:host", "shell_exec", map[string]any{"cmd": "fixture"}, true, time.Second)
	id := firstString(out, "job_id")
	if id == "" {
		t.Fatalf("enqueue failed: %v", out)
	}
	standby := New(Config{ConfigDir: dir})
	defer standby.Close()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := standby.maintainTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	if standby.shellJobs[id].Status != "failed" {
		t.Fatal("already-running standby left an abandoned queued task")
	}
}

func TestArchitectureDurableCancelControlSurvivesLostPoll(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{ConfigDir: dir})
	defer a.Close()
	now := nowFloat()
	a.shellJobs["cancel"] = &shellJob{ID: "cancel", Server: "host", Status: "running", CreatedAt: now - 2, StartedAt: now - 1}
	if err := a.saveTaskStateLocked("cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.mcpTaskCancel("cancel", "hub"); err != nil {
		t.Fatal(err)
	}
	first, err := a.nextDurableTaskControlLocked("host")
	if err != nil || first != "cancel" {
		t.Fatalf("first delivery: %s %v", first, err)
	}
	if err := a.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`UPDATE task_controls SET next_attempt=0`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	b := New(Config{ConfigDir: dir})
	defer b.Close()
	second, err := b.nextDurableTaskControlLocked("host")
	if err != nil || second != first {
		t.Fatalf("lost delivery not replayed: %s %v", second, err)
	}
	if err := b.acknowledgeTaskControlLocked("host", second); err != nil {
		t.Fatal(err)
	}
	if id, err := a.nextDurableTaskControlLocked("host"); err != nil || id != "" {
		t.Fatalf("acknowledged control redelivered: %s %v", id, err)
	}
}

func TestArchitectureStoredTimeoutSurvivesOwnerExit(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{ConfigDir: dir})
	owner, err := a.ensureTaskOwnerLocked()
	if err != nil {
		t.Fatal(err)
	}
	now := nowFloat()
	a.shellJobs["long"] = &shellJob{ID: "long", OwnerID: owner, Server: "host", Status: "running", CreatedAt: now - 7201, StartedAt: now - 7200, Timeout: 10000}
	if err := a.saveTaskStateLocked("long"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := New(Config{ConfigDir: dir})
	defer b.Close()
	if _, err := b.maintainTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	if j := b.shellJobs["long"]; j.Status != "running" || j.Timeout != 10000 {
		t.Fatalf("long timeout lost: %+v", j)
	}
}

func TestArchitectureMigratesVersionOneAndUsesFixedSQLite(t *testing.T) {
	cfg := Config{ConfigDir: t.TempDir()}
	s := &Server{cfg: cfg}
	db, err := openTaskDatabase(s.taskDatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE task_records(kind TEXT NOT NULL,key TEXT NOT NULL,updated REAL NOT NULL,done REAL NOT NULL DEFAULT 0,payload BLOB NOT NULL,PRIMARY KEY(kind,key)) WITHOUT ROWID; CREATE TABLE task_store_meta(id INTEGER PRIMARY KEY,version INTEGER NOT NULL); INSERT INTO task_store_meta(id,version) VALUES(1,1);`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	raw, err := json.Marshal(persistedShellTask{ID: "v1", Status: "completed", CreatedAt: nowFloat() - 1, DoneAt: nowFloat(), Result: "preserved"})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO task_records(kind,key,updated,done,payload) VALUES('shell','v1',?,?,?)`, nowFloat(), nowFloat(), raw)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	restored := New(cfg)
	defer restored.Close()
	if j := restored.shellJobs["v1"]; j == nil || j.Result != "preserved" {
		t.Fatalf("v1 migration lost result: %+v", j)
	}
	if err := restored.withTaskDatabase(func(tx *taskTransaction) error {
		var version int
		var sqliteVersion string
		if err := tx.tx.QueryRow(`SELECT version FROM task_store_meta WHERE id=1`).Scan(&version); err != nil {
			return err
		}
		if version != 2 {
			t.Errorf("schema=%d", version)
		}
		if err := tx.tx.QueryRow(`SELECT sqlite_version()`).Scan(&sqliteVersion); err != nil {
			return err
		}
		var major, minor, patch int
		if _, err := fmt.Sscanf(sqliteVersion, "%d.%d.%d", &major, &minor, &patch); err != nil {
			return err
		}
		if major < 3 || (major == 3 && (minor < 51 || (minor == 51 && patch < 3))) {
			t.Errorf("SQLite %s lacks the required WAL-reset fix", sqliteVersion)
		}
		t.Logf("SQLite runtime=%s, task schema=%d", sqliteVersion, version)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
