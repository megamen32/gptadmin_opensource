package hub

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// Focused integration: real private WAL DB, no runtime/network substitutes.
// The old BEGIN IMMEDIATE reader cannot finish until the writer is released.
func TestTaskReadSnapshotWhileWriterHeld(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: t.TempDir()}}
	initial := persistedShellTask{ID: "read-snapshot", Status: "queued", CreatedAt: nowFloat()}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		return tx.save(persistedTaskState{Shell: map[string]persistedShellTask{initial.ID: initial}})
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err := openTaskDatabase(s.taskDatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	// Prepare the old connection before holding the writer; shorten only this
	// private regression's wait, never the production driver's timeout.
	if _, err := legacy.Exec(`PRAGMA busy_timeout=100`); err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	release := make(chan struct{})
	finished := make(chan error, 1)
	type readResult struct {
		state persistedTaskState
		err   error
	}
	var result chan readResult
	readFinished := false
	go func() {
		finished <- s.withTaskDatabase(func(tx *taskTransaction) error {
			updated := initial
			updated.Status = "completed"
			updated.Revision = 1
			err := tx.save(persistedTaskState{Shell: map[string]persistedShellTask{initial.ID: updated}})
			ready <- err
			<-release
			return err
		})
	}()
	defer func() {
		close(release)
		if err := <-finished; err != nil {
			t.Error(err)
		}
		if result != nil && !readFinished {
			select {
			case <-result:
			case <-time.After(12 * time.Second):
				t.Error("regression reader failed to terminate after writer release")
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer failed to start")
	}
	if tx, err := legacy.Begin(); err == nil {
		tx.Rollback()
		t.Fatal("legacy immediate reader unexpectedly acquired the writer")
	} else {
		var busy *sqlite.Error
		if !errors.As(err, &busy) || busy.Code() != 5 {
			t.Fatalf("legacy read failed for a different reason: %v", err)
		}
	}
	result = make(chan readResult, 1)
	go func() {
		state, err := s.readTaskRecords(initial.ID)
		result <- readResult{state, err}
	}()
	select {
	case got := <-result:
		readFinished = true
		if got.err != nil {
			t.Fatal(got.err)
		}
		if task := got.state.Shell[initial.ID]; task.Status != "queued" || task.Revision != 1 {
			t.Fatalf("reader saw uncommitted task: status=%s revision=%d", task.Status, task.Revision)
		}
	case <-time.After(time.Second):
		t.Error("task read waited for the SQLite writer")
		// The deferred writer release lets the old implementation terminate,
		// rather than stranding the regression reader for its busy timeout.
	}
}

func TestTaskReadSeesCommittedRevision(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: t.TempDir()}}
	task := persistedShellTask{ID: "revision", Status: "queued", CreatedAt: nowFloat()}
	write := func() error {
		return s.withTaskDatabase(func(tx *taskTransaction) error {
			return tx.save(persistedTaskState{Shell: map[string]persistedShellTask{task.ID: task}})
		})
	}
	if err := write(); err != nil {
		t.Fatal(err)
	}
	first, err := s.readTaskRecords(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = first.Shell[task.ID]
	task.Status = "completed"
	if err := write(); err != nil {
		t.Fatal(err)
	}
	second, err := s.readTaskRecords(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Shell[task.ID]; got.Status != "completed" || got.Revision != task.Revision+1 {
		t.Fatalf("committed revision was not visible: status=%s revision=%d", got.Status, got.Revision)
	}
}

func TestTaskReadColdStoreAndQueryOnly(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: filepath.Join(t.TempDir(), "cold")}}
	state, err := s.readTaskRecords()
	if err != nil || len(state.Shell) != 0 {
		t.Fatalf("cold read: %v", err)
	}
	err = s.withTaskReadDatabase(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM task_store_meta`)
		return err
	})
	if err == nil {
		t.Fatal("read-only task connection permitted a write")
	}
	if _, err := s.readTaskRecords(); err != nil {
		t.Fatalf("failed write damaged schema: %v", err)
	}
}

func TestTaskReadUnsupportedVersionFailsClosed(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: t.TempDir()}}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`UPDATE task_store_meta SET version=999 WHERE id=1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readTaskRecords(); err == nil {
		t.Fatal("unsupported version silently accepted")
	}
}
