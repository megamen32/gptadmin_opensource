package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These regressions come from an independent audit of the 1030 release.
//
//  1. A real read-only client was shown "ro" in its catalog but could not run
//     it: the read-only branch of the coarse gate did not know the read-only
//     executor, and it refused a mixed file tool before the action was read.
//  2. Cancelling a task that has a shell record and a relay receipt with the
//     same id finalized only the shell record. The receipt stayed "running"
//     forever, the node counted it as unfinished work and the drain could
//     never become ready.
//  3. The direct execute facade is no longer public. The task read facade has
//     to stay reachable, because the public catalog publishes it.

// auditPayloadString renders a tool payload for a readable assertion.
func auditPayloadString(payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func readOnlyAuditRequest() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	return requestWithAuthClaims(r, map[string]any{
		"sub": "independent-readonly-audit", "client_id": "independent-readonly-audit",
		"scope": "gptadmin.read", "access_mode": accessModeReadonly,
	})
}

func TestReadOnlyIdentityCanRunReadOnlyExec(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	r = requestWithAuthClaims(r, map[string]any{
		"sub": "independent-readonly-audit", "client_id": "independent-readonly-audit",
		"scope": "gptadmin.read", "access_mode": accessModeReadonly,
	})
	out, status := s.callCodeModeToolMode(r, map[string]any{"code": "return 7;"}, true)
	if status != http.StatusOK || out["status"] != "completed" {
		t.Fatalf("read-only identity cannot run even pure ro JavaScript: http=%d response=%#v", status, out)
	}
}

func TestReadOnlyIdentityKeepsWritePathsClosed(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	r := readOnlyAuditRequest()
	if err := authorizeToolAction(r, "shell:admin-node-100", "shell_exec", map[string]any{"cmd": "id"}); err == nil {
		t.Fatal("read-only identity may run a shell command")
	}
	if err := authorizeToolAction(r, "file:admin-node-100", "file_editor", map[string]any{"action": "create", "path": "/tmp/x"}); err == nil {
		t.Fatal("read-only identity may write a file")
	}
	if err := authorizeToolAction(r, "hub", codeModeTool, map[string]any{"code": "return 1;"}); err == nil {
		t.Fatal("read-only identity may use the write-capable exec container")
	}
}

func TestReadOnlyIdentityMayReadThroughMixedTools(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	r := readOnlyAuditRequest()
	if err := authorizeToolAction(r, "file:admin-node-100", "file_editor", map[string]any{"action": "view", "path": "/tmp/x"}); err != nil {
		t.Fatalf("read-only identity cannot read a file: %v", err)
	}
	if err := authorizeToolAction(r, "file:admin-node-100", "file_checkpoint", map[string]any{"action": "list"}); err != nil {
		t.Fatalf("read-only identity cannot list checkpoints: %v", err)
	}
}

func TestReadOnlyCatalogPublishesSearchAndRo(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	r := readOnlyAuditRequest()
	names := map[string]bool{}
	for _, tool := range compactFacadeTools(r, false) {
		names[firstString(tool, "name")] = true
	}
	if !names["search"] {
		t.Fatalf("read-only catalog does not publish search: %v", names)
	}
	if !names["ro"] {
		t.Fatalf("read-only catalog does not publish ro: %v", names)
	}
	if names["exec"] || names["execute"] {
		t.Fatalf("read-only catalog publishes a write-capable facade: %v", names)
	}
}

func TestPublicCatalogHidesTheDirectExecuteFacade(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	for _, tools := range [][]map[string]any{compactFacadeTools(r, false), compactFacadeTools(r, true)} {
		for _, tool := range tools {
			if firstString(tool, "name") == "execute" {
				t.Fatalf("the public catalog publishes the direct execute facade: %v", tools)
			}
		}
	}
	names := map[string]bool{}
	for _, tool := range compactFacadeTools(r, false) {
		names[firstString(tool, "name")] = true
	}
	for _, required := range []string{"search", "exec", "ro", "job", "expand"} {
		if !names[required] {
			t.Fatalf("the public catalog does not publish %q: %v", required, names)
		}
	}
}

func TestDirectExecuteFacadeIsRefused(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	out := s.appsSDKCallMCP(r, "execute", map[string]any{
		"target": "hub", "tool": "settings_schema", "args": map[string]any{},
	})
	payload := mapValue(out)
	if firstString(payload, "status") != "failed" {
		t.Fatalf("the direct execute facade is not refused: %#v", out)
	}
	if firstString(mapValue(payload["error"]), "code") != "execute_facade_retired" {
		t.Fatalf("the refusal does not explain the migration: %#v", out)
	}
}

func TestTaskReadFacadeStaysReachable(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	const id = "task-read-fixture"
	s.mu.Lock()
	s.shellJobs[id] = &shellJob{ID: id, Server: "audit-only", Status: "completed", ToolName: "shell_exec", Result: map[string]any{"stdout": "ok"}}
	s.mu.Unlock()
	out := s.appsSDKCallRawForRequest(r, "job", map[string]any{"id": id})
	payload := mapValue(out)
	if firstString(payload, "status") == "failed" {
		t.Fatalf("the task read facade does not work: %#v", out)
	}
	if !strings.Contains(firstString(payload, "job_id"), id) && !strings.Contains(auditPayloadString(payload), id) {
		t.Fatalf("the task read facade did not return the requested job: %#v", out)
	}
}

func TestCancelSynchronizesSharedTaskRecords(t *testing.T) {
	for _, shellStatus := range []string{"running", "failed"} {
		t.Run(shellStatus, func(t *testing.T) {
			s, _ := codeModeFixture(t)
			defer s.Close()
			const id = "shared-dual-record"
			s.mu.Lock()
			s.shellJobs[id] = &shellJob{ID: id, Server: "audit-only", Status: shellStatus, ToolName: "mcp_tools"}
			s.relayJobs[id] = &relayJob{ID: id, AgentID: "mcp:shell:audit-only:fixture", Status: "running", Method: childSchemaMethod}
			s.cancelTaskLocked(id)
			shell, relay := s.shellJobs[id].Status, s.relayJobs[id].Status
			s.mu.Unlock()
			remaining := s.unfinishedExecutionCount()
			if !taskTerminal(relay) || remaining != 0 {
				t.Fatalf("same-id relay stays nonterminal after cancel: shell=%s relay=%s unfinished_executions=%d", shell, relay, remaining)
			}
		})
	}
}

func TestSharedTaskRecordsFinishTogether(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	const id = "shared-finish-record"
	s.mu.Lock()
	s.shellJobs[id] = &shellJob{ID: id, Server: "audit-only", Status: "running", ToolName: "mcp_tools"}
	s.relayJobs[id] = &relayJob{ID: id, AgentID: "mcp:shell:audit-only:fixture", Status: "running", Method: childSchemaMethod}
	s.finishTaskRecordsLocked(id, "completed", nil)
	shell, relay := s.shellJobs[id].Status, s.relayJobs[id].Status
	s.mu.Unlock()
	if !taskTerminal(shell) || !taskTerminal(relay) {
		t.Fatalf("a finished dispatch job leaves its receipt nonterminal: shell=%s relay=%s", shell, relay)
	}
	if remaining := s.unfinishedExecutionCount(); remaining != 0 {
		t.Fatalf("unfinished executions after a finished task: %d", remaining)
	}
}

func TestOrphanedReceiptIsFailedInsteadOfWaitingForever(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	const id = "orphan-receipt"
	s.mu.Lock()
	s.relayJobs[id] = &relayJob{ID: id, AgentID: "mcp:shell:audit-only:fixture", Status: "running", Method: childSchemaMethod, CreatedAt: nowFloat() - 7200}
	repaired := s.reconcileSharedTaskRecordsLocked()
	receipt := s.relayJobs[id].Status
	s.mu.Unlock()
	if len(repaired) == 0 || !taskTerminal(receipt) {
		t.Fatalf("an orphaned receipt is not failed: repaired=%v status=%s", repaired, receipt)
	}
}
