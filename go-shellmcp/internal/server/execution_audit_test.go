// LHC:COVERS go-shellmcp/internal/server/server.go go-shellmcp/internal/server/execution_audit.go go-shellmcp/internal/audit/audit.go go-shellmcp/internal/shell/run.go
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecutionAuditQueueAndMCPIgnoreSpoofedOrigin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "audit.jsonl")
	s := New(Config{AuditLog: path, IdentityDir: root, SpillDir: filepath.Join(root, "spill"), LogLimit: 8192, DisableSelfUpdate: true})
	defer s.Close()
	var q hub.QueueJob
	if err := json.Unmarshal([]byte(`{"id":"trusted-job","trace_id":"trusted-trace","cmd":"printf queue-safe","arguments":{"run_as_user":"root","execution_origin":{"client_id":"SPOOFED_CLIENT"}},"execution_origin":{"source":"mcp","client_id":"trusted-client","subject":"trusted-subject","profile_id":"profile-one","profile_version":3,"native_harness_hint":"codex","native_session_hint":"declared-session","native_session_status":"client_declared"}}`), &q); err != nil {
		t.Fatal(err)
	}
	req := shellRequestFromQueueJob(q, s.cfg.SpillDir)
	s.applyDefaults(&req)
	if !s.startCallbackJob(q.ID, req, true, q.ExecutionOrigin) {
		t.Fatal("queue rejected")
	}
	s.callbackWG.Wait()
	ctx := withExecutionTask(context.Background(), q.ID, q.TraceID, q.ExecutionOrigin)
	_, err := s.callMCPTool(ctx, "shell_exec", map[string]any{"cmd": "printf mcp-safe", "run_as_user": "root", "execution_origin": map[string]any{"client_id": "SPOOFED_CLIENT"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SPOOFED_CLIENT") {
		t.Fatal("tool args entered trusted origin")
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			t.Fatal("invalid audit")
		}
		if event["type"] == "execution_intent" {
			count++
			if event["job_id"] != q.ID || event["trace_id"] != q.TraceID || event["client_id"] != "trusted-client" || event["native_session_status"] != "client_declared" {
				t.Errorf("trusted context lost: %v", event)
			}
		}
	}
	if count != 2 {
		t.Fatalf("intents=%d want queue and MCP", count)
	}
}

func TestExecutionAuditStartedFailureKeepsRealOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "audit.jsonl")
	marker := filepath.Join(root, "ran")
	s := New(Config{IdentityDir: root, AuditLog: path, SpillDir: filepath.Join(root, "spill"), LogLimit: 8192, DisableSelfUpdate: true})
	defer s.Close()
	s.auditLog.SetAfterWrite(func() { _ = s.auditLog.Close() })
	result := s.runShell(context.Background(), shell.Request{Cmd: "printf safe > '" + marker + "'", RunAsUser: "root", SpillDir: s.cfg.SpillDir})
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("started command outcome was lost", err)
	}
	if result.ReturnCode != 0 || !strings.Contains(result.Error, "process started") {
		t.Fatalf("must retain actual outcome and report started audit failure: %+v", result)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "execution_intent") {
		t.Fatal("durable intent was lost", err)
	}
}

func TestExecutionAuditManagedDefault(t *testing.T) {
	root := t.TempDir()
	s := New(Config{IdentityDir: root, DisableSelfUpdate: true})
	defer s.Close()
	if s.cfg.AuditLog != filepath.Join(root, "execution-audit.jsonl") {
		t.Fatal("managed default missing")
	}
	if info, err := os.Stat(s.cfg.AuditLog); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private managed audit unavailable", err)
	}
}

func TestExecutionAuditEntireLegacyLogHasNoSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "audit.jsonl")
	s := New(Config{Token: "test-token", AuditLog: path, IdentityDir: root, SpillDir: filepath.Join(root, "spill"), LogLimit: 8192, DisableSelfUpdate: true})
	defer s.Close()
	for _, endpoint := range []string{"/exec", "/exec/live", "/exec/stream"} {
		for _, spec := range []map[string]any{
			{"cmd": "AUDIT_SECRET=INLINE_UNIQUE_SECRET printf safe", "stdin": "STDIN_UNIQUE_SECRET", "env": map[string]string{"AUDIT_ENV": "ENV_UNIQUE_SECRET"}, "run_as_user": "root"},
			{"argv": []string{"/usr/bin/printf", "ARGV_UNIQUE_SECRET"}, "run_as_user": "root"},
		} {
			body, _ := json.Marshal(spec)
			r := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Errorf("%s failed: %d %s", endpoint, w.Code, w.Body.String())
			}
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"INLINE_UNIQUE_SECRET", "ARGV_UNIQUE_SECRET", "STDIN_UNIQUE_SECRET", "ENV_UNIQUE_SECRET"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("entire audit log leaked %s", secret)
		}
	}
}

func TestExecutionAuditIntentBeforeRealCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "stream"}[stream], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "audit.jsonl")
			marker := filepath.Join(root, "marker")
			t.Setenv("CODEX_THREAD_ID", "DO_NOT_INFER_NATIVE_SESSION")
			s := New(Config{IdentityDir: root, AuditLog: path, SpillDir: filepath.Join(root, "spill"), LogLimit: 8192, DisableSelfUpdate: true})
			defer s.Close()
			req := shell.Request{Cmd: "grep -q execution_intent '" + path + "' && sleep 0.05 && printf PRIVATE_COMMAND_SENTINEL > '" + marker + "'", RunAsUser: "root", TraceID: "audit-trace", Stdin: "PRIVATE_STDIN_SENTINEL", Env: map[string]string{"PRIVATE_ENV": "PRIVATE_ENV_SENTINEL"}}
			s.applyDefaults(&req)
			var result shell.Result
			if stream {
				result = s.runShellStream(context.Background(), req, func(shell.Event) {})
			} else {
				result = s.runShell(context.Background(), req)
			}
			if result.ReturnCode != 0 || result.Error != "" {
				t.Errorf("real command failed: %+v", result)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("real marker missing: %v", err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"PRIVATE_COMMAND_SENTINEL", "PRIVATE_STDIN_SENTINEL", "PRIVATE_ENV_SENTINEL", "DO_NOT_INFER_NATIVE_SESSION"} {
				if strings.Contains(string(raw), secret) {
					t.Errorf("audit leaked %s", secret)
				}
			}
			var intent, started map[string]any
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line == "" {
					continue
				}
				var event map[string]any
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				switch event["type"] {
				case "execution_intent":
					intent = event
				case "execution_started":
					started = event
				}
			}
			if intent == nil || started == nil {
				t.Fatalf("missing durable intent/start: %s", raw)
			}
			if intent["executor_pid"] != float64(os.Getpid()) || intent["executor_ppid"] != float64(os.Getppid()) {
				t.Errorf("executor identity missing: %v", intent)
			}
			if intent["native_session_status"] != "not_supplied" || intent["trace_id"] != "audit-trace" {
				t.Errorf("caller context missing: %v", intent)
			}
			if hash, ok := intent["command_sha256"].(string); !ok || len(hash) != 64 {
				t.Errorf("command digest missing: %v", intent)
			}
			if started["child_pid"] == nil || started["execution_id"] != intent["execution_id"] {
				t.Errorf("child correlation missing: %v", started)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Errorf("audit permissions=%o", info.Mode().Perm())
			}
		})
	}
}

func TestExecutionAuditUnavailablePreventsRealCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	s := New(Config{IdentityDir: root, AuditLog: filepath.Join(root, "audit.jsonl"), SpillDir: filepath.Join(root, "spill"), LogLimit: 8192, DisableSelfUpdate: true})
	defer s.Close()
	if err := s.auditLog.Close(); err != nil {
		t.Fatal(err)
	}
	result := s.runShell(context.Background(), shell.Request{Cmd: "printf BAD > '" + marker + "'", RunAsUser: "root", SpillDir: filepath.Join(root, "spill")})
	if result.ReturnCode != -1 || result.Error == "" {
		t.Errorf("unwritable managed audit must reject execution: %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("command ran without durable intent: %v", err)
	}
}
