package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestQuorumLocalDispatchRequestPreservesCodeModeBody(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"exec","arguments":{"code":"return 1;"}}}`))
	if !s.quorumLocalDispatchRequest(r) {
		t.Fatal("code mode request was not admitted to the inner target gate")
	}
	if r.Body == nil {
		t.Fatal("request body was consumed")
	}
	var envelope map[string]any
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil || firstString(envelope, "method") != "tools/call" {
		t.Fatalf("request body was not restored: %v %#v", err, envelope)
	}
}

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
	stale := r.WithContext(context.WithValue(r.Context(), ownerAdmissionTermKey{}, term+1))
	for _, target := range []string{"shell:standalone", "shell:homeassistant"} {
		result, status := s.executeMCPTool(stale, target, "shell_exec",
			map[string]any{"argv": []any{"echo", "owner-local"}}, true, time.Second, "")
		if status != http.StatusServiceUnavailable || firstString(result, "job_id") != "" || result["executed"] != false {
			t.Fatalf("stale admission to %s: HTTP %d %#v", target, status, result)
		}
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

	t.Run("delivery after Raft stops", func(t *testing.T) {
		const target = "shell:review"
		const token = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
		admission := context.WithValue(r.Context(), ownerAdmissionTermKey{}, term)
		admission = context.WithValue(admission, localShellAuthStateKey{}, s.authState)
		submit := func(key string) string {
			t.Helper()
			result, status := s.executeMCPTool(r.WithContext(admission), target, "shell_exec",
				map[string]any{"argv": []any{"echo", "same input"}}, true, time.Second, key)
			if id := firstString(result, "job_id"); status == http.StatusOK && id != "" {
				return id
			}
			t.Fatalf("addressed submission failed: HTTP %d %#v", status, result)
			return ""
		}
		a, b := submit(""), submit("")
		keyed := submit("local-retry")
		if a == b || a == keyed || b == keyed || submit("local-retry") != keyed {
			t.Fatal("independent identical calls or explicit retry lost their identity")
		}
		defer func() {
			// Keep the stopped node for route and owner checks, then avoid
			// closing it twice in the fixture cleanup.
			s.mu.Lock()
			s.quorumNode = nil
			s.mu.Unlock()
		}()
		if err := n.Close(); err != nil {
			t.Fatal(err)
		}
		if n.VerifyLeader(ctx) == nil {
			t.Fatal("Raft still authorizes dispatch")
		}
		localAdmission := context.WithValue(admission, localShellAdmissionKey{}, target)
		localResult, localStatus := s.executeMCPTool(r.WithContext(localAdmission), target, "shell_exec",
			map[string]any{"argv": []any{"echo", "after-raft"}}, true, time.Second, "")
		if localStatus != http.StatusOK || firstString(localResult, "job_id") == "" {
			t.Fatalf("addressed local submission after Raft loss: HTTP %d %#v", localStatus, localResult)
		}
		localJobID := firstString(localResult, "job_id")
		poll := func(credential, fingerprint string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/queue/review?timeout=0&server_id=n0&public_key=review-public&fingerprint="+fingerprint, nil)
			req.Header.Set("Authorization", "Bearer "+credential)
			reply := httptest.NewRecorder()
			s.Handler().ServeHTTP(reply, req)
			return reply
		}
		for _, reply := range []*httptest.ResponseRecorder{poll("wrong-token", "review-fingerprint"), poll(token, "wrong-pin")} {
			if reply.Code != http.StatusServiceUnavailable {
				t.Fatalf("unbound poll received HTTP %d", reply.Code)
			}
		}
		for _, id := range []string{a, b, keyed, localJobID} {
			reply := poll(token, "review-fingerprint")
			var envelope map[string]any
			if reply.Code != http.StatusOK || json.Unmarshal(reply.Body.Bytes(), &envelope) != nil || firstString(envelope, "id") != id {
				t.Fatalf("wrong delivery for %s: HTTP %d %s", id, reply.Code, reply.Body)
			}
		}
		if reply := poll(token, "review-fingerprint"); reply.Code != http.StatusServiceUnavailable {
			t.Fatalf("poll replayed a dispatched ID: HTTP %d", reply.Code)
		}
		post := func(result string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/queue/review/result?job_id="+keyed,
				strings.NewReader(`{"id":"`+keyed+`","result":{"stdout":"`+result+`"}}`))
			req.Header.Set("Authorization", "Bearer "+token)
			reply := httptest.NewRecorder()
			s.Handler().ServeHTTP(reply, req)
			return reply
		}
		for _, result := range []string{"first", "duplicate"} {
			if reply := post(result); reply.Code != http.StatusOK {
				t.Fatalf("receipt: HTTP %d %s", reply.Code, reply.Body)
			}
		}
		state, err := s.readTaskRecords(keyed)
		if err != nil || firstString(mapValue(state.Shell[keyed].Result), "stdout") != "first" {
			t.Fatalf("original result was replaced or lost: %v", err)
		}
	})

}
