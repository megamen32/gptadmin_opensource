package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskSaveDoesNotRewriteLegacySnapshot(t *testing.T) {
	dir := t.TempDir()
	now := nowFloat()
	legacy := persistedTaskState{Shell: map[string]persistedShellTask{
		"history": {ID: "history", CreatedAt: now - 3, DoneAt: now - 2, Status: "completed", Result: map[string]any{"stdout": "preserve history"}},
	}}
	before, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, taskStateFilename)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{ConfigDir: dir})
	s.shellJobs["fresh"] = &shellJob{ID: "fresh", CreatedAt: now - 1, DoneAt: now, Status: "completed", Result: "new result"}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("saving one task rewrote the legacy full-history snapshot")
	}
	restored := New(Config{ConfigDir: dir})
	if restored.shellJobs["history"] == nil || restored.shellJobs["fresh"] == nil {
		t.Fatal("migration/restart lost old or new task")
	}
}

func TestTaskStoreOnlyUpdatesSelectedRecord(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	now := nowFloat()
	const historyCount = 3080
	for i := 0; i < historyCount; i++ {
		id := fmt.Sprintf("history-%04d", i)
		s.shellJobs[id] = &shellJob{ID: id, CreatedAt: now - 3, DoneAt: now - 2, Status: "completed", Result: strings.Repeat("x", 5600)}
	}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`CREATE TABLE changes (key TEXT); CREATE TRIGGER count_task_changes AFTER UPDATE ON task_records BEGIN INSERT INTO changes(key) VALUES(new.key); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := procWriteBytes()
	started := time.Now()
	for i := 0; i < 20; i++ {
		s.shellJobs["history-0000"].Result = fmt.Sprintf("updated-%d", i)
		s.shellJobs["history-0000"].DoneAt = now + float64(i)
		if err := s.saveTaskStateLocked("history-0000"); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(started)
	written := procWriteBytes() - before
	t.Logf("history=%d (~%.2f MiB results), 20 committed updates: %s, disk writes=%d bytes (%.0f bytes/update)", historyCount, float64(historyCount*5600)/(1024*1024), elapsed, written, float64(written)/20)
	if err := s.saveTaskStateLocked("history-0000"); err != nil {
		t.Fatal(err)
	}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		var count, keys int
		if err := tx.tx.QueryRow(`SELECT count(*),count(DISTINCT key) FROM changes`).Scan(&count, &keys); err != nil {
			return err
		}
		if count != 20 || keys != 1 {
			t.Errorf("expected 20 updates of one record, got count=%d keys=%d", count, keys)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Allows filesystem variation but rejects history-sized rewrites.
	if written > 20*1024*1024 {
		t.Fatalf("excessive write amplification: %d bytes for 20 small changes", written)
	}
}

func procWriteBytes() int64 {
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		var n int64
		if _, err := fmt.Sscanf(line, "write_bytes: %d", &n); err == nil {
			return n
		}
	}
	return 0
}

func TestTaskStoreStaleWriterCannotOverwriteCompletedTask(t *testing.T) {
	dir := t.TempDir()
	s1 := New(Config{ConfigDir: dir})
	now := nowFloat()
	s1.shellJobs["same"] = &shellJob{ID: "same", Status: "running", CreatedAt: now - 3, StartedAt: now - 2}
	if err := s1.saveTaskStateLocked("same"); err != nil {
		t.Fatal(err)
	}
	s2 := New(Config{ConfigDir: dir})
	s1.shellJobs["same"].Status = "completed"
	s1.shellJobs["same"].DoneAt = now
	s1.shellJobs["same"].Result = "authoritative result"
	if err := s1.saveTaskStateLocked("same"); err != nil {
		t.Fatal(err)
	}
	s2.shellJobs["same"].Result = "stale result"
	if err := s2.saveTaskStateLocked("same"); !errors.Is(err, errTaskConflict) {
		t.Fatalf("stale writer must receive a conflict, got %v", err)
	}
	state, err := s2.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Shell["same"]; got.Status != "completed" || got.Result != "authoritative result" {
		t.Fatalf("stale writer won: %#v", got)
	}
}

func TestTaskStoreAtomicRollback(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	now := nowFloat()
	s.shellJobs["good"] = &shellJob{ID: "good", Status: "completed", CreatedAt: now, DoneAt: now, Result: "result"}
	s.relayJobs["bad"] = &relayJob{ID: "bad", Status: "completed", CreatedAt: now, DoneAt: now, Result: map[string]any{"unserializable": make(chan int)}}
	if err := s.saveTaskStateLocked(); err == nil {
		t.Fatal("expected serialization failure")
	}
	state, err := s.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Shell) != 0 || len(state.Relay) != 0 {
		t.Fatal("failed transaction partially committed")
	}
}

func TestTaskStoreCorruptMigrationIsRetryable(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{ConfigDir: dir}}
	path := filepath.Join(dir, taskStateFilename)
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readTaskState(); err == nil {
		t.Fatal("corrupt migration accepted")
	}
	valid := []byte(`{"shell_jobs":{"recover":{"id":"recover","status":"completed","created_at":1,"completed_at":2,"result":"recovered"}}}`)
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := s.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Shell["recover"].Result != "recovered" {
		t.Fatal("migration marker committed before data")
	}
}

func TestTaskStoreCrashRecovery(t *testing.T) {
	if dir := os.Getenv("GPTADMIN_TASK_CRASH_TEST_DIR"); dir != "" {
		s := New(Config{ConfigDir: dir})
		// Keep a second live connection so the writer's close cannot checkpoint the
		// WAL. Exit without closing it: recovery must replay the committed WAL.
		db, err := openTaskDatabase(s.taskDatabasePath())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Ping(); err != nil {
			t.Fatal(err)
		}
		var journal string
		var synchronous int
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatal(err)
		}
		if journal != "wal" || synchronous != 2 {
			t.Fatalf("durability disabled: %s %d", journal, synchronous)
		}
		now := nowFloat()
		done := make(chan struct{})
		close(done)
		s.shellJobs["crash"] = &shellJob{ID: "crash", Status: "completed", CreatedAt: now - 1, DoneAt: now, Result: "durable result"}
		s.idempotency["crash-key"] = &idempotencyEntry{Fingerprint: "fp", CreatedAt: time.Now(), Done: done, JobID: "crash", Response: map[string]any{"job_id": "crash"}, Status: 200}
		if err := s.saveTaskStateLocked("crash"); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(s.taskDatabasePath() + "-wal")
		if err != nil || st.Size() == 0 {
			t.Fatalf("no committed WAL: %v", err)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTaskStoreCrashRecovery$")
	cmd.Env = append(os.Environ(), "GPTADMIN_TASK_CRASH_TEST_DIR="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v %s", err, output)
	}
	s := New(Config{ConfigDir: dir})
	if job := s.shellJobs["crash"]; job == nil || job.Result != "durable result" {
		t.Fatalf("committed result lost: %#v", job)
	}
	if s.idempotency["crash-key"] == nil {
		t.Fatal("committed idempotency lost")
	}
}

func TestTaskResultDoesNotAcknowledgeFailedCommit(t *testing.T) {
	s := New(Config{})
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.ConfigDir = blocked
	s.shellJobs["result"] = &shellJob{ID: "result", Server: "host", Status: "running", CreatedAt: nowFloat()}
	r := httptest.NewRequest(http.MethodPost, "/shell/queue/host/result", strings.NewReader(`{"id":"result","result":{"stdout":"keep in outbox"}}`))
	w := httptest.NewRecorder()
	s.shellQueueResult(w, r, "host")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("acknowledged uncommitted result: %d %s", w.Code, w.Body.String())
	}
}

func TestTaskDispatchRequeuesFailedCommit(t *testing.T) {
	s := New(Config{})
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.ConfigDir = blocked
	s.shellJobs["dispatch"] = &shellJob{ID: "dispatch", Server: "host", Status: "queued", CreatedAt: nowFloat()}
	s.shellQueues["host"] = []string{"dispatch"}
	r := httptest.NewRequest(http.MethodGet, "/shell/queue/host?timeout=1", nil)
	w := httptest.NewRecorder()
	s.pollShellQueue(w, r, "host")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("dispatched uncommitted job: %d %s", w.Code, w.Body.String())
	}
	if s.shellJobs["dispatch"].Status != "queued" || len(s.shellQueues["host"]) != 1 {
		t.Fatal("failed dispatch was not requeued")
	}
}

func TestTaskStoreRespectsConfiguredRetention(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	now := nowFloat()
	s.settings.Values["completed_job_retention_hours"] = 48
	s.shellJobs["keep"] = &shellJob{ID: "keep", Status: "completed", CreatedAt: now - 40*3600, DoneAt: now - 36*3600, Result: "36h old"}
	s.shellJobs["expired"] = &shellJob{ID: "expired", Status: "completed", CreatedAt: now - 80*3600, DoneAt: now - 72*3600}
	s.shellJobs["active"] = &shellJob{ID: "active", Status: "running", CreatedAt: now - 80*3600}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	state, err := s.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Shell["keep"]; !ok {
		t.Fatal("configured 48h retention was shortened")
	}
	if _, ok := state.Shell["expired"]; ok {
		t.Fatal("expired task was retained")
	}
	if _, ok := state.Shell["active"]; !ok {
		t.Fatal("active task was pruned")
	}
}

func TestTaskStoreCommitsRecoveryTransition(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	s.shellJobs["queued"] = &shellJob{ID: "queued", Status: "queued", CreatedAt: nowFloat()}
	if err := s.saveTaskStateLocked("queued"); err != nil {
		t.Fatal(err)
	}
	restored := New(s.cfg)
	state, err := restored.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Shell["queued"].Status != "failed" {
		t.Fatal("recovery transition only exists in memory")
	}
}
