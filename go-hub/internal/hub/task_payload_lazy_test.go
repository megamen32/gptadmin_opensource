package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Focused integration, real SQLite. Expected 3s, emergency maximum 30s.
// Detects eager history payloads, failed lazy reads and overwriting cold results.
func TestTaskPayloadHistoryLazy(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir})
	s.mu.Lock()
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("history-%d", i)
		s.shellJobs[id] = &shellJob{ID: id, Server: "host", Status: "completed", DoneAt: nowFloat() - 90, Result: strings.Repeat(id, 4096)}
	}
	s.relayJobs["relay-history"] = &relayJob{ID: "relay-history", AgentID: "agent", Status: "completed", DoneAt: nowFloat() - 90, Result: map[string]any{"text": strings.Repeat("relay", 4096)}}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	s.Close()
	s = New(Config{ConfigDir: dir})
	defer s.Close()
	if len(s.shellJobs) != 0 {
		t.Fatalf("startup retained historical tasks: %d", len(s.shellJobs))
	}
	if s.relayJobs["relay-history"] != nil {
		t.Fatal("startup retained relay payload")
	}
	got, rpcErr := s.mcpTaskGet("history-12", "shell:host")
	if rpcErr != nil || !strings.Contains(fmt.Sprint(got), strings.Repeat("history-12", 16)) {
		t.Fatalf("lazy receipt unavailable: %v %v", got, rpcErr)
	}
	s.mu.Lock()
	s.shellJobs["history-12"].CachedAt = nowFloat() - 61
	s.pruneExpiredTaskMemoryLocked()
	if s.shellJobs["history-12"] != nil {
		t.Fatal("read history outlived one-minute cache")
	}
	legacy, legacyErr := s.shellTaskPageLocked("host", 0, 3)
	if legacyErr != nil || legacy["count"] != 64 || len(legacy["tasks"].([]map[string]any)) != 3 {
		t.Fatalf("legacy lazy list: %v %v", legacy, legacyErr)
	}
	page := s.adminJobsDataLocked(0, 5)
	if page["count"] != 65 || len(page["recent"].([]map[string]any)) != 5 || len(s.shellJobs) != 0 {
		t.Fatalf("lazy inventory lost history or cached its payloads: %v", page["count"])
	}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	state, err := s.readTaskRecords("history-13", "relay-history")
	s.mu.Unlock()
	if err != nil || state.Shell["history-13"].Result != strings.Repeat("history-13", 4096) || state.Relay["relay-history"].Result["text"] != strings.Repeat("relay", 4096) {
		t.Fatal("saving metadata overwrote durable historical output", err)
	}
}

// Focused integration, expected 1s / maximum 10s. History paging must use
// metadata indexes rather than decoding/scanning every historical payload.
func TestTaskHistoryPageUsesSQLiteIndex(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		rows, err := tx.Query(`EXPLAIN QUERY PLAN SELECT key FROM task_records INDEXED BY task_records_inventory WHERE kind IN ('shell','relay') ORDER BY json_extract(payload,'$.created_at') DESC,key DESC LIMIT 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("history page lacks indexed lazy query: %v", err)
	}
}

// Focused integration, expected 1s / maximum 10s. Actual history counts must
// follow committed status changes/deletes and survive transaction rollback.
func TestTaskHistoryCountsFollowDurableTransactions(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shellJobs["counted"] = &shellJob{ID: "counted", Status: "completed", DoneAt: nowFloat(), CreatedAt: nowFloat()}
	if err := s.saveTaskStateLocked("counted"); err != nil {
		t.Fatal(err)
	}
	check := func(want int, status string) {
		t.Helper()
		n, counts, err := s.taskHistoryCountsLocked()
		if err != nil || n != want || (want > 0 && counts[status] != want) {
			t.Fatalf("history count: %d %v %v", n, counts, err)
		}
	}
	check(1, "completed")
	abort := errors.New("rollback")
	change := func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`UPDATE task_records SET payload=json_set(payload,'$.status','failed') WHERE key='counted'`)
		return err
	}
	err := s.withTaskDatabase(func(tx *taskTransaction) error {
		if err := change(tx); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	check(1, "completed")
	if err := s.withTaskDatabase(change); err != nil {
		t.Fatal(err)
	}
	check(1, "failed")
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		_, err := tx.tx.Exec(`DELETE FROM task_records WHERE key='counted'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(0, "")
}
