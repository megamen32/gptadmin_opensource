package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminMCPToolNativeList(t *testing.T) {
	s := New(Config{CtlToken: "fixture", ConfigDir: t.TempDir()})
	defer s.Close()
	s.mu.Lock()
	s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Kind: "virtual_shell", Status: "online", LastSeen: nowFloat()}
	s.mu.Unlock()
	payload := []byte(`{"target":"shell:fixture","method":"tools/list"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/tool", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer fixture")
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d %s", recorder.Code, recorder.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(sliceValue(mapValue(decoded["result"])["tools"])) == 0 {
		t.Fatalf("no native tools: %#v", decoded)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("tools/list queued a legacy job")
	}
}
