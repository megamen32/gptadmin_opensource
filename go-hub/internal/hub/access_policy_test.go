package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestAccessModeTreatsWriteScopeAsFull(t *testing.T) {
	for _, scope := range []string{"gptadmin.exec", "gptadmin.write"} {
		r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
		r = requestWithAuthClaims(r, map[string]any{"scope": "gptadmin.read " + scope})
		if got := requestAccessMode(r); got != accessModeFull {
			t.Fatalf("scope %q: access mode = %q, want %q", scope, got, accessModeFull)
		}
	}
}

func TestRequestAccessModeDoesNotTreatReadOnlyScopeAsFull(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	r = requestWithAuthClaims(r, map[string]any{"scope": "gptadmin.read"})
	if got := requestAccessMode(r); got != accessModeReadonly {
		t.Fatalf("read-only scope: access mode = %q, want %q", got, accessModeReadonly)
	}
}

func TestGranularHubScopesDoNotGrantShellExec(t *testing.T) {
	tests := []struct{ scope, tool string }{
		{"gptadmin.settings.write", "settings_set"},
		{"gptadmin.registry.manage", "agent_policy_set"},
		{"gptadmin.update", "handover_start"},
	}
	for _, tt := range tests {
		r, _ := http.NewRequest(http.MethodPost, "https://hub.example/mcp", nil)
		r = requestWithAuthClaims(r, map[string]any{"scope": tt.scope})
		if err := authorizeToolCall(r, "hub", tt.tool); err != nil {
			t.Fatalf("scope %s tool %s denied: %v", tt.scope, tt.tool, err)
		}
		if err := authorizeToolCall(r, "shell:admin-server-100", "shell_exec"); err == nil {
			t.Fatalf("scope %s unexpectedly grants shell_exec", tt.scope)
		}
	}
}

func TestGranularReadScopes(t *testing.T) {
	for _, tc := range []struct{ scope, tool string }{{"gptadmin.settings.read", "settings_get"}, {"gptadmin.registry.read", "stale_cleanup_preview"}, {"gptadmin.update", "handover_status"}} {
		r, _ := http.NewRequest(http.MethodPost, "https://hub.example/mcp", nil)
		r = requestWithAuthClaims(r, map[string]any{"scope": tc.scope})
		if err := authorizeToolCall(r, "hub", tc.tool); err != nil {
			t.Fatalf("scope %s denied %s: %v", tc.scope, tc.tool, err)
		}
	}
}
