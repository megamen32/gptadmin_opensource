package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessReadonlyProfileCannotInheritFullToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = requestWithAuthClaims(r, map[string]any{"scope": "gptadmin.read gptadmin.exec", "access_mode": "full"})
	r = requestWithAccessProfile(r, AccessProfile{ID: "reader", AccessMode: "readonly", ApprovalMode: "read_only", AllowedTargets: []string{"shell:host"}, AllowedTools: []string{"shell_exec"}})
	if err := authorizeToolCall(r, "shell:host", "shell_exec"); err == nil {
		t.Fatal("read-only profile inherited execution from full token")
	}
}
