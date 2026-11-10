// LHC:COVERS go-shellmcp/internal/server/execution_audit_child.go go-shellmcp/internal/server/execution_audit.go go-shellmcp/internal/server/mcp.go
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Status(ref) reports the original unscoped process. A validated scoped call
// targets a different partition, whose PID is not exposed by that observer.
func TestExecutionAuditChildScopedInvocationDoesNotAttributeUnscopedPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "audit.jsonl")
	marker := filepath.Join(root, "scoped-pid")
	script := filepath.Join(root, "child.sh")
	body := "#!/bin/sh\nwhile IFS= read -r line; do\ncase \"$line\" in\n" +
		"*'\"method\":\"initialize\"'*) printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\",\"capabilities\":{}}}' ;;\n" +
		"*'\"method\":\"tools/call\"'*) printf '%s' \"$$\" > \"$SCOPED_PID_MARKER\"; printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[]}}' ;;\n" +
		"esac\ndone\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	s := New(Config{IdentityDir: filepath.Join(root, "identity"), AuditLog: path, MCPConfig: filepath.Join(root, "mcp.json"), DisableSelfUpdate: true, ExecTimeout: 2})
	defer s.Close()
	if _, err := s.mcpManage(map[string]any{"action": "upsert", "config": map[string]any{
		"ref": "child", "transport": "stdio", "command": script, "enabled": true,
		"env": map[string]any{"SCOPED_PID_MARKER": marker},
	}}); err != nil {
		t.Fatal(err)
	}
	agent, err := s.supervisor.Agent("child")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.childMCP.Start(ctx, agent); err != nil {
		t.Fatal(err)
	}
	unscoped := s.childMCP.Status(agent.Ref)
	if !unscoped.Running || unscoped.PID <= 0 {
		t.Fatal("unscoped process A is missing")
	}
	scope := strings.Repeat("a", 64)
	ctx = withExecutionTask(ctx, "scoped-job", "scoped-trace", map[string]any{"client_id": "scoped-client"})
	_, callErr := s.callMCPTool(ctx, "mcp_call", map[string]any{
		"ref": agent.Ref, "name": "echo", "arguments": map[string]any{},
		"_connection_scope": scope, "_connection_instance": s.mcpConnectionInstance(agent.Ref),
		"_connection_expires_at": time.Now().Add(10 * time.Second).UnixMilli(),
	})
	// The current managed-stdio lifecycle gate may reject the downstream
	// execution. Its pre-forward intent still must not attribute process A.
	if callErr != nil && !strings.Contains(callErr.Error(), "scoped stdio ownership-safe lifecycle unavailable") {
		t.Fatal(callErr)
	}
	if callErr == nil {
		pidText, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		scopedPID, err := strconv.Atoi(string(pidText))
		if err != nil || scopedPID <= 0 || scopedPID == unscoped.PID {
			t.Fatalf("expected distinct real partition B: A=%d B=%q error=%v", unscoped.PID, pidText, err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var intent map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) == nil && event["type"] == "child_invocation_intent" {
			intent = event
		}
	}
	if intent == nil {
		t.Fatal("scoped child intent missing")
	}
	if intent["stdio_pid"] != nil || intent["stdio_pid_status"] != "unknown" || intent["stdio_process"] != nil {
		t.Fatalf("validated scoped invocation falsely attributed unscoped A=%d: pid=%v status=%v process=%v", unscoped.PID, intent["stdio_pid"], intent["stdio_pid_status"], intent["stdio_process"])
	}
	if intent["job_id"] != "scoped-job" || intent["trace_id"] != "scoped-trace" || intent["client_id"] != "scoped-client" {
		t.Fatal("scoped caller context lost")
	}
	if strings.Contains(string(raw), scope) {
		t.Fatal("raw scoped capability leaked into audit")
	}
}

// The child is a protocol sentinel: it observes forwarding and the persisted
// intent, without interpreting tool inputs or claiming real PTY acceptance.
func TestExecutionAuditChildForwardingRequiresDurableIntent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess fixture")
	}
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "intent_before_forward", true: "audit_unavailable"}[closed], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "audit.jsonl")
			marker := filepath.Join(root, "forwarded")
			script := filepath.Join(root, "pty-mcp")
			body := "#!/bin/sh\nwhile IFS= read -r line; do\ncase \"$line\" in\n" +
				"*'\"method\":\"initialize\"'*) printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\",\"capabilities\":{}}}' ;;\n" +
				"*'\"method\":\"tools/call\"'*) if grep -q child_invocation_intent \"$AUDIT_PATH\"; then printf persisted > \"$FORWARD_MARKER\"; else printf missing > \"$FORWARD_MARKER\"; fi; printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[]}}' ;;\n" +
				"esac\ndone\n"
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			s := New(Config{IdentityDir: filepath.Join(root, "identity"), AuditLog: path, MCPConfig: filepath.Join(root, "mcp.json"), DisableSelfUpdate: true, ExecTimeout: 2})
			defer s.Close()
			if _, err := s.mcpManage(map[string]any{"action": "upsert", "config": map[string]any{"ref": "PtyMcp", "name": "pty-mcp", "transport": "stdio", "command": script, "enabled": true, "env": map[string]any{"AUDIT_PATH": path, "FORWARD_MARKER": marker}}}); err != nil {
				t.Fatal(err)
			}
			if closed {
				if err := s.auditLog.Close(); err != nil {
					t.Fatal(err)
				}
			}
			ctx := withExecutionTask(context.Background(), "forward-job", "forward-trace", map[string]any{"source": "mcp", "client_id": "forward-client"})
			_, callErr := s.callMCPTool(ctx, "mcp_call", map[string]any{"ref": "PtyMcp", "name": "terminal_spawn", "arguments": map[string]any{"shell": "/bin/sh", "args": []string{"-c", "printf FORWARD_SECRET_PAYLOAD"}, "execution_origin": map[string]any{"client_id": "FORGED_CLIENT"}}})
			if closed {
				if callErr == nil || !strings.Contains(callErr.Error(), "durable child invocation audit unavailable") {
					t.Errorf("audit failure was not enforced before forwarding: %v", callErr)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Error("unwritable audit reached child")
				}
				return
			}
			if callErr != nil {
				t.Fatal(callErr)
			}
			observed, err := os.ReadFile(marker)
			if err != nil || string(observed) != "persisted" {
				t.Fatalf("child received request without prior durable intent: marker=%q err=%v", observed, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "FORWARD_SECRET_PAYLOAD") || strings.Contains(string(raw), "FORGED_CLIENT") {
				t.Fatal("forwarding audit leaked payload")
			}
			var intent map[string]any
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var event map[string]any
				if json.Unmarshal([]byte(line), &event) == nil && event["type"] == "child_invocation_intent" {
					intent = event
				}
			}
			if intent["job_id"] != "forward-job" || intent["trace_id"] != "forward-trace" || intent["client_id"] != "forward-client" {
				t.Errorf("original context lost before forwarding: %v", intent)
			}
		})
	}
}

func TestExecutionAuditChildReportsExistingRealStdioPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "audit.jsonl")
	script := filepath.Join(root, "mcp-fixture.sh")
	// A real managed stdio subprocess proves PID observation only. It is not
	// PtyMcp and this fixture never creates a terminal or executes tool inputs.
	body := `#!/bin/sh
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}' ;;
 esac
done
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	s := New(Config{IdentityDir: root, AuditLog: path, MCPConfig: filepath.Join(root, "mcp.json"), DisableSelfUpdate: true})
	defer s.Close()
	if _, err := s.mcpManage(map[string]any{"action": "upsert", "config": map[string]any{"ref": "stdio-fixture", "transport": "stdio", "command": script, "enabled": true}}); err != nil {
		t.Fatal(err)
	}
	agent, err := s.supervisor.Agent("stdio-fixture")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.childMCP.Start(ctx, agent); err != nil {
		t.Fatal(err)
	}
	status := s.childMCP.Status(agent.Ref)
	if !status.Running || status.PID <= 0 {
		t.Fatal("real stdio process missing")
	}
	ctx = withExecutionTask(ctx, "declared-job", "declared-trace", map[string]any{"native_harness_hint": "codex", "native_session_hint": "declared-native-session", "native_session_status": "client_declared"})
	if err := s.auditChildInvocation(ctx, agent.Ref, "terminal_spawn", map[string]any{"shell": "/bin/bash"}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var candidate map[string]any
		if json.Unmarshal([]byte(line), &candidate) != nil {
			t.Fatal("invalid audit")
		}
		if candidate["type"] == "child_invocation_intent" {
			event = candidate
		}
	}
	if event["stdio_pid"] != float64(status.PID) || event["stdio_pid_status"] != "observed_running" || event["stdio_process"] == nil {
		t.Fatalf("real stdio identity lost: %v", event)
	}
	if event["terminal_pid"] != nil {
		t.Fatal("stdio PID was invented as a terminal PID")
	}
	if event["native_session_hint"] != "declared-native-session" || event["native_session_status"] != "client_declared" {
		t.Fatal("declared native hint lost")
	}
}

// This is helper-level coverage, not proof that mcpChildCall is wired or that
// a real PtyMcp terminal was created. The forwarding file is owned separately.
type childInvocationAuditor interface {
	auditChildInvocation(context.Context, string, string, map[string]any, string) error
}

func TestExecutionAuditChildInvocationIntent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "audit.jsonl")
	s := New(Config{IdentityDir: root, AuditLog: path, DisableSelfUpdate: true})
	defer s.Close()
	auditor, ok := any(s).(childInvocationAuditor)
	if !ok {
		t.Fatal("child invocation durable intent helper is absent")
	}
	t.Setenv("CODEX_THREAD_ID", "DAEMON_NATIVE_ID_MUST_NOT_LEAK")
	inputs := map[string]any{"shell": "/bin/bash", "args": []string{"-lc", "INLINE_SECRET=value printf COMMAND_SECRET"}, "session_id": "RAW_TERMINAL_CAPABILITY", "env": map[string]any{"SECRET": "ENV_SECRET"}, "execution_origin": map[string]any{"client_id": "SPOOFED_CLIENT"}}
	origin := map[string]any{"source": "mcp", "client_id": "trusted-client", "subject": "trusted-subject", "profile_id": "profile", "profile_version": 2}
	ctx := withExecutionTask(context.Background(), "original-job", "original-trace", origin)
	if err := auditor.auditChildInvocation(ctx, "PtyMcp", "terminal_spawn", inputs, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"COMMAND_SECRET", "INLINE_SECRET", "ENV_SECRET", "RAW_TERMINAL_CAPABILITY", "SPOOFED_CLIENT", "DAEMON_NATIVE_ID_MUST_NOT_LEAK"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("child audit leaked %s", secret)
		}
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "child_invocation_intent" || event["child_ref"] != "PtyMcp" || event["child_tool"] != "terminal_spawn" || event["phase"] != "before_forward" {
		t.Errorf("not an original invocation intent: %v", event)
	}
	if event["job_id"] != "original-job" || event["trace_id"] != "original-trace" || event["client_id"] != "trusted-client" {
		t.Errorf("trusted context lost: %v", event)
	}
	if event["native_session_status"] != "not_supplied" || event["native_session_hint"] != "unknown" {
		t.Errorf("native identity fabricated: %v", event)
	}
	encoded, _ := json.Marshal(inputs)
	digest := sha256.Sum256(encoded)
	if event["inputs_sha256"] != hex.EncodeToString(digest[:]) {
		t.Error("input digest mismatch")
	}
	if event["executor_pid"] != float64(os.Getpid()) || event["executor_ppid"] != float64(os.Getppid()) || event["executor_process"] == nil {
		t.Error("executor process metadata missing")
	}
	if event["stdio_pid"] != nil || event["terminal_pid"] != nil || event["stdio_pid_status"] != "unknown" {
		t.Error("missing process identity was fabricated")
	}
	if _, found := event["result"]; found {
		t.Fatal("intent claims a response")
	}
}

func TestExecutionAuditChildUnavailableFailsClosed(t *testing.T) {
	root := t.TempDir()
	s := New(Config{IdentityDir: root, DisableSelfUpdate: true})
	defer s.Close()
	auditor, ok := any(s).(childInvocationAuditor)
	if !ok {
		t.Fatal("child invocation durable intent helper is absent")
	}
	if err := s.auditLog.Close(); err != nil {
		t.Fatal(err)
	}
	if err := auditor.auditChildInvocation(context.Background(), "PtyMcp", "terminal_spawn", map[string]any{"shell": "/bin/bash"}, ""); err == nil {
		t.Fatal("unwritable audit allowed forwarding")
	}
}
