package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminMCPToolCreatesNativeTaskAndReadsReceipt(t *testing.T) {
	s := New(Config{CtlToken: "fixture", ConfigDir: t.TempDir()})
	defer s.Close()
	s.mu.Lock()
	s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Kind: "virtual_shell", Status: "online", LastSeen: nowFloat()}
	s.mu.Unlock()
	body := []byte(`{"target":"shell:fixture","method":"tools/call","name":"shell_exec","arguments":{"cmd":"printf test-native","timeout":1}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/tool", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer fixture")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("native call http=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	task := mapValue(payload["result"])
	id := firstString(task, "taskId")
	if task["resultType"] != "task" || id == "" {
		t.Fatalf("native task missing: %#v", payload)
	}
	receiptReq := httptest.NewRequest(http.MethodGet, "/admin/api/tasks/"+id, nil)
	receiptReq.Header.Set("Authorization", "Bearer fixture")
	receipt := httptest.NewRecorder()
	s.Handler().ServeHTTP(receipt, receiptReq)
	if receipt.Code != http.StatusOK {
		t.Fatalf("receipt http=%d: %s", receipt.Code, receipt.Body.String())
	}
	var stored map[string]any
	if err := json.Unmarshal(receipt.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if firstString(stored, "taskId") != id || stored["resultType"] != "complete" {
		t.Fatalf("receipt identity mismatch: %#v", stored)
	}
}
