package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Drive the regular owner receipt handler and the real SQLite store twice:
// a restart between the first commit and its acknowledgement must not replace it.
func TestOwnerReceiptPreservesFirstResultAfterRestart(t *testing.T) {
	root := t.TempDir()
	newOwner := func() *Server {
		s := New(Config{ConfigDir: root, ShellToken: "test-receipt-credential"})
		s.nodeIdentity = &nodeIdentity{NodeID: "node-a"}
		return s
	}
	const id = "ot1.bm9kZS1h.job"
	owner := newOwner()
	owner.mu.Lock()
	runtimeID, err := owner.ensureTaskOwnerLocked()
	if err == nil {
		owner.shellJobs[id] = &shellJob{ID: id, OwnerID: runtimeID, ReceiptTarget: "shell:phone", Server: "phone", Status: "running", ToolName: "shell_exec", CreatedAt: nowFloat(), StartedAt: nowFloat()}
		err = owner.saveTaskStateLocked(id)
	}
	owner.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	post := func(s *Server, name, hint, result, token string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]any{"id": id, "result": map[string]any{"stdout": result}})
		r := httptest.NewRequest("POST", "/queue/"+name+"/result?job_id="+hint, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		var ack map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
			t.Fatal(err)
		}
		return rec.Code, ack
	}
	for _, test := range []struct {
		target, hint, token string
		status              int
	}{
		{"other", id, "test-receipt-credential", 403},
		{"phone", id, "unrelated", 401},
		{"phone", "other", "test-receipt-credential", 400},
	} {
		if got, ack := post(owner, test.target, test.hint, "discard", test.token); got != test.status || ack["ok"] == true {
			t.Fatalf("unexpected acceptance HTTP %d", got)
		}
	}
	for i, content := range []string{strings.Repeat("owner-only", 128<<10), "late duplicate"} {
		status, ack := post(owner, "phone", id, content, "test-receipt-credential")
		if status != 200 || ack["ok"] != true || ack["id"] != id || ack["owner_id"] != "node-a" {
			t.Fatalf("missing durable owner ACK %d %#v", status, ack)
		}
		if i == 0 {
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			owner = newOwner()
		} else if ack["already_recorded"] != true {
			t.Fatal("lost ACK caused a second terminal transition")
		}
	}
	defer owner.Close()
	state, err := owner.readTaskRecords(id)
	if err != nil || mapValue(state.Shell[id].Result)["stdout"] != strings.Repeat("owner-only", 128<<10) {
		t.Fatal("stored result changed")
	}
	foreign := New(Config{ConfigDir: filepath.Join(t.TempDir(), "foreign"), ShellToken: "test-receipt-credential"})
	defer foreign.Close()
	foreign.nodeIdentity = &nodeIdentity{NodeID: "node-b"}
	if status, ack := post(foreign, "phone", id, "private", "test-receipt-credential"); status != 503 || ack["ok"] == true {
		t.Fatalf("foreign ACK HTTP %d", status)
	}
}

func TestResultRoutePrefixPreservesLegacyBody(t *testing.T) {
	const id = "ot1.bm9kZS1h.job"
	raw := []byte("{\"id\":\"" + id + "\",\"result\":{\"n\":9007199254740993,\"stdout\":\"" + strings.Repeat("x", 3<<20) + "\"}}")
	req := httptest.NewRequest(http.MethodPost, "/queue/phone/result", bytes.NewReader(raw))
	if got := receiptRoutingID(req); got != id {
		t.Fatal("lost legacy-client handle")
	}
	var sink bytes.Buffer
	if _, err := sink.ReadFrom(req.Body); err != nil || !bytes.Equal(sink.Bytes(), raw) {
		t.Fatal("peek changed legacy result bytes")
	}
}
