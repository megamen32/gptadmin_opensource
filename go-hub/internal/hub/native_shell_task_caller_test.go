package hub

import (
	"net/http"
	"testing"
	"time"
)

// Exercise the native tools/call -> tasks/get seam with authenticated client
// identities. No executor is attached: only the isolated durable queue is used.
func TestNativeShellTaskReceiptKeepsSubmittingClient(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{ConfigDir: dir, DefaultTimeout: time.Second})
	defer s.Close()
	profile := AccessProfile{
		ID: "native-shell-receipt", Version: 1,
		AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted,
		AllowedTargets: []string{"shell:test"}, AllowedTools: []string{"shell_exec"},
	}
	if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	agent := Agent{AgentID: "shell:test", Name: "test", Kind: "shell", Status: "online"}
	request := func(client, token string) *http.Request {
		r := taskReceiptTestRequest(http.MethodPost, "/server/shell-test/mcp", client, token, profile)
		return requestWithAuthClaims(r, map[string]any{
			"client_id": client, "jti": token, "sub": client, "scope": "gptadmin.read gptadmin.exec",
		})
	}
	owner, foreign := request("owner", "owner-token"), request("foreign", "foreign-token")
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion": "2026-07-28",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{
			"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}},
		},
	}
	created, rpcErr, noContent := s.agentMCPJSONRPC(owner, agent, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "shell_exec", "arguments": map[string]any{"cmd": "printf isolated-receipt"}, "_meta": meta},
	})
	if rpcErr != nil || noContent || firstString(mapValue(created), "resultType") != "task" {
		t.Fatalf("native task not admitted: result=%v error=%v", created, rpcErr)
	}
	id := firstString(mapValue(created), "taskId")
	if id == "" {
		t.Fatal("missing task identity")
	}
	get := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tasks/get",
		"params": map[string]any{"taskId": id, "_meta": meta}}
	check := func(server *Server, status string) {
		t.Helper()
		if result, err, _ := server.agentMCPJSONRPC(foreign, agent, get); err == nil || result != nil || firstString(mapValue(err), "message") != "unknown taskId" {
			t.Fatalf("foreign client must remain denied: result=%v error=%v", result, err)
		}
		result, err, _ := server.agentMCPJSONRPC(owner, agent, get)
		if err != nil || firstString(mapValue(result), "taskId") != id || firstString(mapValue(result), "status") != status {
			t.Fatalf("submitting client cannot read its %s native task: result=%v error=%v", status, result, err)
		}
	}
	check(s, "working")
	s.mu.Lock()
	if len(s.shellJobs) != 1 || len(s.shellQueues["test"]) != 1 {
		s.mu.Unlock()
		t.Fatal("receipt reads must not submit another task")
	}
	// Supply one local completion fixture, without running any shell job.
	job := s.shellJobs[id]
	job.Status, job.DoneAt = "completed", nowFloat()
	job.Result = map[string]any{"stdout": "isolated-receipt", "returncode": 0}
	if err := s.saveTaskStateLocked(id); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	check(s, "completed")
	reopened := New(Config{ConfigDir: dir})
	defer reopened.Close()
	check(reopened, "completed")
}
