package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestOfflineShellMCPScopesExecutorAndReceipt(t *testing.T) {
	const target = "shell:review"
	const fingerprint = "review-fp"
	s := &Server{
		quorumNode: new(quorum.Node),
		localExecutors: map[string]map[string]string{
			target: {"server_id": "review-id", "fingerprint": fingerprint, "public_key": "review-pub"},
		},
		localExecutorTokens: map[string]string{target: "fixture-local-token"},
	}
	authority := offlineShellAuthority{
		index: 1,
		member: nodeMember{
			Name: target, State: nodeStateReady, LocalID: "review-id",
			LocalFingerprint: fingerprint, Capabilities: []string{"executor"},
		},
		registry: map[string]Agent{target: {Status: "online", Meta: map[string]any{
			"approved": true, "server_id": "review-id", "fingerprint": fingerprint,
		}}},
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = r.WithContext(context.WithValue(r.Context(), offlineShellAuthorityKey{}, authority))
	profile := AccessProfile{AllowedTargets: []string{"hub", target}, AllowedTools: []string{"exec", "shell_exec"}, ApprovalMode: approvalModeUnrestricted}
	r = requestWithAccessProfile(r, profile)
	if !s.offlineShellTargetAllowed(r, target) || s.offlineShellTargetAllowed(r, "shell:other") {
		t.Fatal("offline permission escaped exact local executor")
	}
	if !s.offlineShellJobAllowedLocked(r, &shellJob{Server: "review", ToolName: "shell_exec"}) ||
		s.offlineShellJobAllowedLocked(r, &shellJob{Server: "review", ToolName: "file_transfer"}) ||
		s.offlineShellJobAllowedLocked(r, &shellJob{Server: "review", ToolName: "shell_exec",
			LocalExecutor: &localShellExecutorIdentity{ID: "replacement", Fingerprint: fingerprint}}) {
		t.Fatal("offline receipt escaped shell tool or physical owner")
	}
	for _, tc := range []struct {
		method, tool string
		allow        bool
	}{
		{"tools/call", "exec", true},
		{"tasks/get", "", true},
		{"tasks/cancel", "", false},
		{"tasks/update", "", false},
		{"tools/call", "approve_pending_server", false},
		{"tools/call", "fleet_exec", false},
		{"tools/call", "job", false}, // legacy unscoped handle
	} {
		body := map[string]any{"method": tc.method, "params": map[string]any{"name": tc.tool}}
		if s.offlineShellRPCAllowed(r, body) != tc.allow {
			t.Errorf("wrong offline permission for %s %s", tc.method, tc.tool)
		}
	}
	authority.member.State = nodeStateDisabled
	disabled := r.WithContext(context.WithValue(r.Context(), offlineShellAuthorityKey{}, authority))
	if s.offlineShellTargetAllowed(disabled, target) {
		t.Fatal("disabled membership authorized offline dispatch")
	}
	profile.AllowedTargets = []string{"*"}
	if s.offlineShellTargetAllowed(requestWithAccessProfile(r, profile), target) {
		t.Fatal("wildcard alone authorized offline dispatch")
	}
}
