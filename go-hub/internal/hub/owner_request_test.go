package hub

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

// Exercise the local SQLite admission path and the actual Raft FSM.
// The process-level outage matrix separately exercises the transport and executor.
func TestOwnerLocalAdmissionReservesOnlyClientKeys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "hub"))
	t.Cleanup(func() { s.Close() })
	cfg := reviewQuorumConfig(t, root)
	cfg.Mode = quorum.OwnerLocal
	cfg.ExternalSnapshotMaxBytes = quorum.ControlSnapshotLimit
	s.quorumMode = cfg.Mode
	s.quorumSnapshotDataDir = cfg.DataDir
	n := reviewOpenQuorum(t, s, cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if _, err := n.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	term := n.CurrentTerm()
	s.quorumAuthorityTerm.Store(term)
	s.quorumAuthorityActiveWriter.Store(true)
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	run := func(req *http.Request, key string) (string, int) {
		result, status := s.executeMCPTool(req, "shell:standalone", "shell_exec",
			map[string]any{"argv": []any{"echo", "owner-local"}}, true, time.Second, key)
		return firstString(result, "job_id"), status
	}
	anonymous, status := run(r, "")
	if status != http.StatusOK || anonymous == "" {
		t.Fatalf("unkeyed admission: id=%q status=%d", anonymous, status)
	}
	first, status := run(r, "my-retry")
	if status != http.StatusOK || first == "" || first == anonymous {
		t.Fatalf("keyed admission: id=%q status=%d", first, status)
	}
	again, status := run(r, "my-retry")
	if status != http.StatusOK || again != first {
		t.Fatalf("retry: id=%q status=%d want %q", again, status, first)
	}
	if stale, status := run(r.WithContext(context.WithValue(r.Context(), ownerAdmissionTermKey{}, term+1)), ""); status != http.StatusServiceUnavailable || stale != "" {
		t.Fatalf("stale admission: id=%q status=%d", stale, status)
	}
	if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		var keys, jobs int
		err := tx.QueryRow("SELECT (SELECT count(*) FROM task_requests), (SELECT count(*) FROM task_records WHERE kind='shell')").Scan(&keys, &jobs)
		if err == nil && (keys != 1 || jobs != 2) {
			t.Errorf("stored %d reservations for %d tasks, want 1 for 2", keys, jobs)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	digest := sha256Hex([]byte(sha256Hex(nil) + ":my-retry"))
	if op, ok := n.LocalOperation(digest); !ok || op.JobID != first {
		t.Fatalf("cluster reservation does not point to original job")
	}
}
