package hub

import (
	"fmt"
	"strings"
	"testing"
)

// Fast units, expected <1s/maximum10s: use real private SQLite to detect a
// history scan and prove unchanged expiry/control semantics, not live latency.
func TestTaskControlsPruneUsesCoveringDoneIndex(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: t.TempDir()}}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		rows, err := tx.tx.Query("EXPLAIN QUERY PLAN "+pruneDoneRecordsSQL, 100)
		if err != nil {
			return err
		}
		defer rows.Close()
		covering := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			if strings.Contains(detail, "COVERING INDEX task_records_done") {
				covering = true
			}
			if strings.Contains(detail, "task_records USING PRIMARY KEY") {
				return fmt.Errorf("history payload scan: %s", detail)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !covering {
			return fmt.Errorf("retention query lost covering done index")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskControlsPrunePreservesActiveRecentAndBoundaryRecords(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: t.TempDir()}}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		for _, row := range []struct {
			kind, key string
			done      float64
		}{
			{"shell", "expired", 99}, {"shell", "boundary", 100}, {"shell", "recent", 101}, {"shell", "active", 0}, {"relay", "relay-expired", 99},
		} {
			if _, err := tx.tx.Exec(`INSERT INTO task_records(kind,key,updated,done,payload,revision) VALUES(?,?,?,?,?,0)`, row.kind, row.key, 100, row.done, []byte(`{}`)); err != nil {
				return err
			}
			if _, err := tx.tx.Exec(`INSERT INTO task_controls(task_id,server) VALUES(?,?)`, row.key, "test"); err != nil {
				return err
			}
		}
		if err := tx.prune(100, 0); err != nil {
			return err
		}
		for _, row := range []struct {
			key             string
			record, control int
		}{
			{"expired", 0, 0}, {"boundary", 1, 1}, {"recent", 1, 1}, {"active", 1, 1}, {"relay-expired", 0, 1},
		} {
			var records, controls int
			if err := tx.tx.QueryRow(`SELECT count(*) FROM task_records WHERE key=?`, row.key).Scan(&records); err != nil {
				return err
			}
			if err := tx.tx.QueryRow(`SELECT count(*) FROM task_controls WHERE task_id=?`, row.key).Scan(&controls); err != nil {
				return err
			}
			if records != row.record || controls != row.control {
				return fmt.Errorf("retention semantics changed for %s: %d/%d", row.key, records, controls)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
