// LHC:COVERS go-hub/internal/hub/access_task_receipts.go go-hub/internal/hub/execution_origin.go go-hub/internal/hub/server.go
package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const originTestNativeSession = "019a6e27-e55b-73d1-87d8-4e01f1f75043"

func originTestRequest() *http.Request {
	r := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{
		"client_id": "origin-client", "jti": "origin-token", "sub": "origin-subject",
		"profile_id": "forged-claim-profile", "profile_version": 999,
	})
	r = requestWithAccessProfile(r, AccessProfile{ID: "origin-profile", Version: 7, AccessMode: accessModeFull})
	r.Header.Set("X-GptAdmin-Harness", "codex")
	r.Header.Set("X-GptAdmin-Native-Session-Id", originTestNativeSession)
	r.Header.Set("Mcp-Session-Id", "opaque-mcp-origin-session")
	return r
}

func originTestCallerJSON(t *testing.T, caller *taskCaller) map[string]any {
	t.Helper()
	data, err := json.Marshal(caller)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	origin, ok := decoded["caller_origin"].(map[string]any)
	if !ok {
		t.Fatal("authenticated task caller did not record caller_origin")
	}
	return origin
}

func TestExecutionOriginCapturesValidatedCallerAndHashesMCPSession(t *testing.T) {
	r := originTestRequest()
	caller := taskCallerFromRequest(r)
	origin := originTestCallerJSON(t, caller)
	digest := sha256.Sum256([]byte(r.Header.Get("Mcp-Session-Id")))
	want := map[string]any{"source": "authenticated_mcp", "client_id": "origin-client", "subject": "origin-subject",
		"profile_id": "origin-profile", "profile_version": float64(7), "native_harness_hint": "codex",
		"native_session_hint": originTestNativeSession, "native_session_status": "client_declared",
		"mcp_session_hash": hex.EncodeToString(digest[:])}
	for key, expected := range want {
		if origin[key] != expected {
			t.Errorf("origin %s = %v, want %v", key, origin[key], expected)
		}
	}
	encoded, _ := json.Marshal(caller)
	if strings.Contains(string(encoded), r.Header.Get("Mcp-Session-Id")) {
		t.Fatal("raw MCP session ID was persisted")
	}
}

func TestExecutionOriginHintsAreValidatedAndNotAuthority(t *testing.T) {
	for _, tc := range []struct{ harness, session, wantHarness, wantSession string }{
		{"opencode", "ses_e80eb63d1ffeiPeBKMW1XhStge", "opencode", "ses_e80eb63d1ffeiPeBKMW1XhStge"},
		{"zcode", "sess_4b3ea36a-7064-4191-8e6a-a7acab1194ff", "zcode", "sess_4b3ea36a-7064-4191-8e6a-a7acab1194ff"},
		{"Bearer secret-fixture", "sk-secret-fixture", "", ""},
		{"codex", "arbitrary text", "codex", ""},
		{"unknown-agent", originTestNativeSession, "", originTestNativeSession},
		{"codex", strings.Repeat("a", 4096), "codex", ""},
		{"opencode", "ses_short", "opencode", ""},
		{"zcode", "sess_not-a-uuid", "zcode", ""},
		{"", "", "", ""},
	} {
		r := originTestRequest()
		r.Header.Set("X-GptAdmin-Harness", tc.harness)
		r.Header.Set("X-GptAdmin-Native-Session-Id", tc.session)
		caller := taskCallerFromRequest(r)
		origin := originTestCallerJSON(t, caller)
		status := "not_supplied"
		if tc.wantSession != "" {
			status = "client_declared"
		}
		if origin["native_harness_hint"] != tc.wantHarness || origin["native_session_hint"] != tc.wantSession || origin["native_session_status"] != status {
			t.Errorf("diagnostic hints were not safely validated: %v", origin)
		}
		if !sameTaskCaller(caller, taskCallerFromRequest(originTestRequest())) {
			t.Fatal("diagnostic hints changed task caller authorization identity")
		}
		encoded, _ := json.Marshal(caller)
		if strings.Contains(string(encoded), "secret-fixture") || strings.Contains(string(encoded), "arbitrary text") {
			t.Fatal("invalid hint text was persisted")
		}
	}
	unscoped := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	unscoped.Header.Set("X-GptAdmin-Harness", "codex")
	unscoped.Header.Set("X-GptAdmin-Native-Session-Id", originTestNativeSession)
	if taskCallerFromRequest(nil) != nil || taskCallerFromRequest(unscoped) != nil {
		t.Fatal("internal/operator nil caller semantics changed")
	}
}

func TestExecutionOriginPersistsAndQueueIgnoresForgedToolMetadata(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir})
	r := originTestRequest()
	forged := map[string]any{"source": "authenticated_mcp", "client_id": "forged-client", "native_session_hint": "forged-session"}
	args := map[string]any{"cmd": "printf origin-fixture", "execution_origin": forged, "_meta": map[string]any{"execution_origin": forged}}
	receipt := s.callShellToolWithFilesystemACLForRequest(r, "shell:origin-worker", "shell_exec", args, true, time.Second, "origin-trace", "", nil, nil)
	id := firstString(receipt, "job_id")
	if id == "" {
		_ = s.Close()
		t.Fatalf("real shell submission did not persist a job: %v", receipt)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored := New(Config{ConfigDir: dir})
	defer restored.Close()
	restored.mu.Lock()
	err := restored.refreshTaskRecordsLocked(id)
	job := restored.shellJobs[id]
	restored.mu.Unlock()
	if err != nil || job == nil || job.ID != id {
		t.Fatalf("original task was not restored: %v", err)
	}
	origin := originTestCallerJSON(t, job.Caller)
	if origin["native_session_hint"] != originTestNativeSession || !sameTaskCaller(job.Caller, taskCallerFromRequest(r)) {
		t.Fatal("restored task lost diagnostic origin or changed authorization")
	}
	w := httptest.NewRecorder()
	restored.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/origin-worker?timeout=1", nil), "origin-worker")
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	actual, ok := payload["execution_origin"].(map[string]any)
	if !ok || actual["client_id"] != "origin-client" || actual["native_session_hint"] != originTestNativeSession {
		t.Fatalf("queue did not overwrite authoritative origin from stored caller: %v", actual)
	}
	if payload["id"] != id || payload["trace_id"] != "origin-trace" || mapValue(mapValue(payload["arguments"])["execution_origin"])["client_id"] != "forged-client" {
		t.Fatal("queue changed task/trace identity or tool arguments")
	}
}

func TestExecutionOriginLegacyCallerRestoreKeepsRightsAndReportsNotRecorded(t *testing.T) {
	var caller taskCaller
	if err := json.Unmarshal([]byte(`{"client_id":"origin-client","token_id":"origin-token","subject":"origin-subject","profile_id":"origin-profile","profile_version":7}`), &caller); err != nil {
		t.Fatal(err)
	}
	if !sameTaskCaller(&caller, taskCallerFromRequest(originTestRequest())) {
		t.Fatal("legacy caller lost its existing authorization identity")
	}
	for _, owner := range []*taskCaller{&caller, nil} {
		s := New(Config{})
		job := &shellJob{ID: "origin-legacy", Server: "origin-worker", ToolName: "shell_exec", Cmd: "true", CreatedAt: nowFloat(), Status: "queued", Caller: owner}
		s.shellJobs[job.ID] = job
		s.shellQueues[job.Server] = []string{job.ID}
		w := httptest.NewRecorder()
		s.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/origin-worker?timeout=1", nil), job.Server)
		_ = s.Close()
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		origin, ok := payload["execution_origin"].(map[string]any)
		if !ok || origin["source"] != "unknown_legacy_or_operator" || origin["native_session_status"] != "not_recorded" || origin["native_session_hint"] != "" {
			t.Fatalf("old/missing metadata must render explicitly unknown: %v", origin)
		}
	}
}
