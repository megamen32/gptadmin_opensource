package hub

import (
	"database/sql"
	"strings"
	"testing"
)

func TestPendingRecoveryUsesQueuedStatusIndex(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		rows, err := tx.Query("EXPLAIN QUERY PLAN SELECT kind,key,payload,revision FROM task_records "+pendingShellRecoveryIndex+" WHERE "+pendingShellRecoveryWhere, "host", "owner")
		if err != nil {
			return err
		}
		defer rows.Close()
		found := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			if strings.Contains(detail, "USING INDEX task_records_status") && strings.Contains(detail, "<expr>=?") {
				found = true
			}
		}
		if !found {
			t.Error("recovery scans historical payloads instead of seeking queued status")
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}
