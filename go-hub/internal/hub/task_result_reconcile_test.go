package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func terminalResultFixture(t *testing.T) (quorum.TerminalOriginEvidence, map[string]json.RawMessage, persistedShellTask) {
	t.Helper()
	now := nowFloat()
	task := persistedShellTask{ID: "synthetic-executed-orphan", Server: "fixture88", ReceiptTarget: "shell:fixture88", OwnerID: "original-owner", Caller: &taskCaller{ClientID: "original-client", TokenID: "original-token", Subject: "original-client"}, ToolName: "mcp_call", TraceID: "original-trace", TraceParent: "original-parent", CreatedAt: now - 4, StartedAt: now - 3, Status: "running"}
	payload, _ := json.Marshal(task)
	row := quorumTaskRecordRow{Kind: "shell", Key: task.ID, Updated: task.StartedAt, Payload: payload, Revision: 2}
	record, _ := json.Marshal(row)
	result := map[string]json.RawMessage{"id": json.RawMessage(`"synthetic-executed-orphan"`), "trace_id": json.RawMessage(`"original-trace"`), "traceparent": json.RawMessage(`"original-parent"`), "result": json.RawMessage(`{"structuredContent":{"marker":"retained-result"},"resultType":"complete"}`)}
	return quorum.TerminalOriginEvidence{JobID: task.ID, JobRevision: 2, Record: record}, result, task
}

func TestTerminalResultPreservesAdmissionAndUsesReconciliationTime(t *testing.T) {
	origin, result, original := terminalResultFixture(t)
	at := nowFloat()
	row, immutable, _, err := terminalRecordForResult(origin, "shell:fixture88", result, at)
	if err != nil {
		t.Fatal(err)
	}
	var task persistedShellTask
	if err = json.Unmarshal(row.Payload, &task); err != nil {
		t.Fatal(err)
	}
	if task.Status != "completed" || task.DoneAt != at || row.Done != at || row.Revision != 3 || task.CreatedAt != original.CreatedAt || task.StartedAt != original.StartedAt || task.OwnerID != original.OwnerID || task.Caller.TokenID != original.Caller.TokenID || len(task.SealedPending) != 0 {
		t.Fatal("admitted identity, time provenance or terminal-only guarantee changed")
	}
	if got, err := terminalImmutable(row.Payload); err != nil || got != immutable {
		t.Fatal("immutable original metadata changed")
	}
}

func TestTerminalResultRejectsMismatchedOrExecutableOrigin(t *testing.T) {
	for _, name := range []string{"wrong-server", "wrong-trace", "queued", "sealed-pending", "request-key", "parent-task", "missing-owner", "spilled-result", "lossy-number"} {
		t.Run(name, func(t *testing.T) {
			origin, result, task := terminalResultFixture(t)
			var row quorumTaskRecordRow
			_ = json.Unmarshal(origin.Record, &row)
			switch name {
			case "wrong-server":
				task.Server = "foreign"
			case "wrong-trace":
				result["trace_id"] = json.RawMessage(`"foreign"`)
			case "queued":
				task.Status = "queued"
			case "sealed-pending":
				task.SealedPending = json.RawMessage(`{"cipher":"must not be imported"}`)
			case "request-key":
				task.RequestKey = "requires-existing-binding"
			case "parent-task":
				task.ParentTaskID = "group-parent"
			case "missing-owner":
				task.OwnerID = ""
			case "spilled-result":
				result["result"] = json.RawMessage(`{"stdout_path":"/tmp/unpreserved-output"}`)
			case "lossy-number":
				result["result"] = json.RawMessage(`{"n":9007199254740993}`)
			}
			row.Payload, _ = json.Marshal(task)
			origin.Record, _ = json.Marshal(row)
			if _, _, _, err := terminalRecordForResult(origin, "shell:fixture88", result, nowFloat()); err == nil {
				t.Fatal("unsafe reconstruction accepted")
			}
		})
	}
}

func TestTerminalResultLiveCallbackAndAlteredErrorReadback(t *testing.T) {
	s := authContinuityNode(t, "writer", "")
	token := strings.Repeat("f", 32)
	if err := s.RegisterLocalExecutor("fixture88", map[string]string{"server_id": "fixture88-id", "public_key": "fixture88-public", "fingerprint": "fixture88-fingerprint"}, token); err != nil {
		t.Fatal(err)
	}
	origin, result, _ := terminalResultFixture(t)
	body, _ := json.Marshal(result)
	callback := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/queue/fixture88/result", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.shellQueueResult(rec, req, "fixture88")
		return rec
	}
	if rec := callback(); rec.Code != 404 {
		t.Fatalf("missing origin callback=%d", rec.Code)
	}
	at := nowFloat()
	row, immutable, resultHash, err := terminalRecordForResult(origin, "shell:fixture88", result, at)
	if err != nil {
		t.Fatal(err)
	}
	if err = handoffImportLegacy(s.cfg, []quorumTaskRecordRow{row}, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, exists := s.shellJobs[row.Key]; exists {
		t.Fatal("import unexpectedly filled runtime cache")
	}
	if rec := callback(); rec.Code != 200 {
		t.Fatalf("normal callback=%d %s", rec.Code, rec.Body.String())
	}
	owner := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"client_id": "original-client", "jti": "original-token", "sub": "original-client"})
	foreign := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"client_id": "foreign", "jti": "foreign", "sub": "foreign"})
	if _, rpcErr := s.mcpTaskGetForRequest(owner, row.Key, ""); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, rpcErr := s.mcpTaskGetForRequest(foreign, row.Key, ""); rpcErr == nil {
		t.Fatal("original ACL lost")
	}
	directory := t.TempDir()
	outbox := filepath.Join(t.TempDir(), "outbox")
	if err = os.Mkdir(outbox, 0700); err != nil {
		t.Fatal(err)
	}
	outbox = filepath.Join(outbox, row.Key+".json")
	if err = os.WriteFile(outbox, []byte("retained fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := terminalResultPlan{Schema: "gptadmin.terminal-result-plan/v1", ConfigDir: s.cfg.ConfigDir, Target: "shell:fixture88", OutboxFile: outbox, Record: row, ImmutableSHA256: immutable, ResultSHA256: resultHash, ReconciledAt: at, ApplicationChainClaim: "not verified or modified"}
	path := filepath.Join(directory, "plan.json")
	if err = handoffWritePrivate(path, plan); err != nil {
		t.Fatal(err)
	}
	digest, err := handoffFileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	readback, err := InspectTerminalResult(s.cfg, directory, digest)
	if err != nil || readback["record_matches_plan"] != true {
		t.Fatalf("normal callback timestamp/revision rejected: %v %+v", err, readback)
	}
	if err = s.withTaskDatabase(func(tx *taskTransaction) error {
		_, e := tx.tx.Exec("UPDATE task_records SET payload=json_set(payload,'$.error','changed error'),revision=revision+1 WHERE kind='shell' AND key=?", row.Key)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	readback, err = InspectTerminalResult(s.cfg, directory, digest)
	if err != nil {
		t.Fatal(err)
	}
	if readback["record_matches_plan"] != false {
		t.Fatal("altered error accepted despite same result/status/ACL")
	}
	if _, err = os.Stat(outbox); err != nil {
		t.Fatal("inspection modified the retained outbox")
	}
}

func TestTerminalResultAbsentGuardPreservesControlsAndTombstones(t *testing.T) {
	s := authContinuityNode(t, "writer", "")
	id := "exact-missing-receipt"
	if err := s.withTaskDatabase(func(tx *taskTransaction) error { return terminalAbsent(tx.tx, id) }); err != nil {
		t.Fatal(err)
	}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		_, e := tx.tx.Exec("INSERT INTO task_controls(task_id,server,next_attempt) VALUES(?,?,0)", id, "fixture88")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error { return terminalAbsent(tx.tx, id) }); err == nil {
		t.Fatal("existing control was ignored")
	}
	other := "retained-revision-only"
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		if _, e := tx.tx.Exec("CREATE TABLE task_quorum_revisions(namespace TEXT,row_key TEXT,revision INTEGER)"); e != nil {
			return e
		}
		_, e := tx.tx.Exec("INSERT INTO task_quorum_revisions VALUES('task_records/shell',?,2)", other)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error { return terminalAbsent(tx.tx, other) }); err == nil {
		t.Fatal("retained revision/tombstone was ignored")
	}
}
