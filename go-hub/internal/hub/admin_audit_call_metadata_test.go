package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAuditCallStoresShapeNotSensitiveInput(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	s.auditToolDecision(request, "shell:node-100", "shell_exec",
		map[string]any{"cmd": "private-command-or-secret", "cwd": "/srv"}, "allow", "", nil, http.StatusOK)
	s.mu.Lock()
	event := s.audit[len(s.audit)-1]
	s.mu.Unlock()
	keys, ok := event.Fields["argument_keys"].([]string)
	if !ok || !reflect.DeepEqual(keys, []string{"cmd", "cwd"}) {
		t.Fatalf("unexpected argument fields: %v", event.Fields["argument_keys"])
	}
	if event.Fields["method"] != "POST" || event.Fields["endpoint"] != "/mcp" {
		t.Fatalf("missing safe call method and endpoint: %+v", event.Fields)
	}
	raw, err := json.Marshal(event)
	if err != nil || strings.Contains(string(raw), "private-command-or-secret") || strings.Contains(string(raw), "/srv") {
		t.Fatalf("raw values leaked into audit event: %s", raw)
	}
}
