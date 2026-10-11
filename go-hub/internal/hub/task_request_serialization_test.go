package hub

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// Focused local logic + real private SQLite. No child/browser operation.
// Expected <1s per case/max5s: publication and pre-commit cleanup must obey
// the same task mutation mutex as callbacks, rather than race another writer.
func TestTaskRequestFinishSerializesWithCallbacks(t *testing.T) {
	testTaskRequestWriteSerialization(t, false)
}

func TestTaskRequestCleanupSerializesWithCallbacks(t *testing.T) {
	testTaskRequestWriteSerialization(t, true)
}

func testTaskRequestWriteSerialization(t *testing.T, failure bool) {
	t.Helper()
	s := New(Config{ConfigDir: t.TempDir()})
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	ready := make(chan struct{})
	proceed := make(chan struct{})
	finished := make(chan int, 1)
	go func() {
		_, status := s.executeDurableTaskRequest("mutex-fixture", "fp", func() (map[string]any, int) {
			close(ready)
			<-proceed
			if failure {
				return taskPersistenceFailure(errors.New("known uncommitted fixture")), 503
			}
			return map[string]any{"marker": "local unit only"}, 200
		})
		finished <- status
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		close(proceed)
		t.Fatal("private request reservation did not finish")
	}
	s.mu.Lock()
	close(proceed)
	var early bool
	var status int
	select {
	case status = <-finished:
		early = true
	case <-time.After(150 * time.Millisecond):
	}
	var payload []byte
	readErr := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT response FROM task_requests WHERE key='mutex-fixture'`).Scan(&payload)
	})
	s.mu.Unlock()
	if !early {
		select {
		case status = <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("request did not finish after task mutex release")
		}
	}
	if early || readErr != nil || payload != nil {
		t.Fatalf("request writer bypassed task mutex: completed=%v readErr=%v responsePresent=%v", early, readErr, payload != nil)
	}
	want := 200
	if failure {
		want = 503
	}
	if status != want {
		t.Fatalf("final status=%d want=%d", status, want)
	}
}

func TestFleetRequestFinishSerializesWithCallbacks(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.reserveDurableTaskRequest("fleet-mutex-fixture", "fp"); err != nil {
		t.Fatal(err)
	}
	entry := &idempotencyEntry{Key: "fleet-mutex-fixture", Done: make(chan struct{})}
	finished := make(chan error, 1)
	s.mu.Lock()
	go func() { finished <- s.finishFleetIdempotency(entry, map[string]any{"marker": "local unit only"}, 200) }()
	time.Sleep(150 * time.Millisecond)
	var payload []byte
	err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT response FROM task_requests WHERE key='fleet-mutex-fixture'`).Scan(&payload)
	})
	s.mu.Unlock()
	select {
	case finishErr := <-finished:
		if finishErr != nil {
			t.Fatal(finishErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fleet publication did not finish after mutex release")
	}
	if err != nil || payload != nil {
		t.Fatalf("fleet persisted outside task mutex: readErr=%v responsePresent=%v", err, payload != nil)
	}
}
