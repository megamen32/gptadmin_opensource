package hub

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeCodexCallerBanFixture(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-caller-ban.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write ban fixture: %v", err)
	}
	t.Setenv("GPTADMIN_CODEX_CALLER_BAN_FILE", path)
	resetCodexCallerBanCache()
	t.Cleanup(resetCodexCallerBanCache)
}

func codexCallerBanRequest(t *testing.T, clientID, userAgent string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	r = requestWithAuthClaims(r, map[string]any{"client_id": clientID})
	if userAgent != "" {
		r.Header.Set("User-Agent", userAgent)
	}
	return r
}

func TestCodexCallerBanRefusalIsTypedAndCarriesTheCaller(t *testing.T) {
	writeCodexCallerBanFixture(t, `{
		"version": 1,
		"enabled": true,
		"clients": ["chatgpt-connector"],
		"target_suffixes": [":AgentHerder"],
		"tools": ["new_or_resume"],
		"denied_harness": "codex"
	}`)

	r := codexCallerBanRequest(t, "chatgpt-connector", "openai-mcp/1.0.0")
	err := authorizeCodexCallerBan(r, "mcp:shell:admin-node-100:AgentHerder", "new_or_resume", map[string]any{"harness": "codex"})
	var banErr *CodexCallerBanError
	if !errors.As(err, &banErr) {
		t.Fatalf("refusal is not a CodexCallerBanError: %#v", err)
	}
	if banErr.Caller != "chatgpt-connector" || banErr.Tool != "new_or_resume" || banErr.Target != "mcp:shell:admin-node-100:AgentHerder" {
		t.Fatalf("refusal lost its evidence: %#v", banErr)
	}
}

func TestCodexCallerBanIsOffWithoutRuleFile(t *testing.T) {
	t.Setenv("GPTADMIN_CODEX_CALLER_BAN_FILE", filepath.Join(t.TempDir(), "missing.json"))
	resetCodexCallerBanCache()
	t.Cleanup(resetCodexCallerBanCache)

	r := codexCallerBanRequest(t, "chatgpt-connector", "openai-mcp/1.0.0")
	err := authorizeCodexCallerBan(r, "mcp:shell:admin-node-100:AgentHerder", "new_or_resume", map[string]any{"harness": "codex"})
	if err != nil {
		t.Fatalf("missing rule file denied a call: %v", err)
	}
}

func TestCodexCallerBanRefusesOnlyCodexStartsForTheNamedCaller(t *testing.T) {
	writeCodexCallerBanFixture(t, `{
		"version": 1,
		"enabled": true,
		"clients": ["chatgpt-connector"],
		"targets": [],
		"target_suffixes": [":AgentHerder"],
		"tools": ["new_or_resume", "create_session", "resume_agent", "deliver"],
		"denied_harness": "codex"
	}`)

	target := "mcp:shell:admin-node-100:AgentHerder"

	r := codexCallerBanRequest(t, "chatgpt-connector", "openai-mcp/1.0.0")
	if err := authorizeCodexCallerBan(r, target, "new_or_resume", map[string]any{"harness": "codex"}); err == nil {
		t.Fatal("codex start by the banned caller was allowed")
	}
	if err := authorizeCodexCallerBan(r, target, "create_session", map[string]any{"harness": "codex"}); err == nil {
		t.Fatal("codex create by the banned caller was allowed")
	}
	if err := authorizeCodexCallerBan(r, target, "resume_agent", map[string]any{"harness": "codex"}); err == nil {
		t.Fatal("codex resume by the banned caller was allowed")
	}
	if err := authorizeCodexCallerBan(r, target, "deliver", map[string]any{"harness": "codex"}); err == nil {
		t.Fatal("codex deliver by the banned caller was allowed")
	}

	if err := authorizeCodexCallerBan(r, target, "new_or_resume", map[string]any{"harness": "zcode"}); err != nil {
		t.Fatalf("a non-codex harness was refused: %v", err)
	}
	if err := authorizeCodexCallerBan(r, target, "list_agents", map[string]any{}); err != nil {
		t.Fatalf("a read tool was refused: %v", err)
	}
	if err := authorizeCodexCallerBan(r, target, "new_or_resume", map[string]any{}); err != nil {
		t.Fatalf("a call without a harness was refused: %v", err)
	}

	other := codexCallerBanRequest(t, "owner-client", "openai-mcp/1.0.0")
	if err := authorizeCodexCallerBan(other, target, "new_or_resume", map[string]any{"harness": "codex"}); err != nil {
		t.Fatalf("another caller was refused: %v", err)
	}
}

func TestCodexCallerBanMatchesUserAgentAndSkipsExcludedAgents(t *testing.T) {
	writeCodexCallerBanFixture(t, `{
		"version": 1,
		"enabled": true,
		"clients": [],
		"user_agent_prefixes": ["openai-mcp/"],
		"user_agent_excludes": ["(Codex)"],
		"target_suffixes": [":AgentHerder"],
		"tools": ["new_or_resume"],
		"denied_harness": "codex"
	}`)

	target := "mcp:shell:node-44:AgentHerder"

	chat := codexCallerBanRequest(t, "some-client", "openai-mcp/1.0.0")
	if err := authorizeCodexCallerBan(chat, target, "new_or_resume", map[string]any{"harness": "codex"}); err == nil {
		t.Fatal("the chatgpt connector was allowed to start codex")
	}

	codexConnector := codexCallerBanRequest(t, "some-client", "openai-mcp/1.0.0 (Codex)")
	if err := authorizeCodexCallerBan(codexConnector, target, "new_or_resume", map[string]any{"harness": "codex"}); err != nil {
		t.Fatalf("the excluded codex connector was refused: %v", err)
	}
}

func TestCodexCallerBanDisabledFlagKeepsEveryCall(t *testing.T) {
	writeCodexCallerBanFixture(t, `{
		"version": 1,
		"enabled": false,
		"clients": ["chatgpt-connector"],
		"target_suffixes": [":AgentHerder"],
		"tools": ["new_or_resume"],
		"denied_harness": "codex"
	}`)

	r := codexCallerBanRequest(t, "chatgpt-connector", "openai-mcp/1.0.0")
	if err := authorizeCodexCallerBan(r, "mcp:shell:admin-node-100:AgentHerder", "new_or_resume", map[string]any{"harness": "codex"}); err != nil {
		t.Fatalf("a disabled rule refused a call: %v", err)
	}
}

func TestAuthorizeToolActionAppliesTheCallerBan(t *testing.T) {
	writeCodexCallerBanFixture(t, `{
		"version": 1,
		"enabled": true,
		"clients": ["chatgpt-connector"],
		"target_suffixes": [":AgentHerder"],
		"tools": ["new_or_resume"],
		"denied_harness": "codex"
	}`)

	r := codexCallerBanRequest(t, "chatgpt-connector", "openai-mcp/1.0.0")
	err := authorizeToolAction(r, "mcp:shell:admin-node-100:AgentHerder", "new_or_resume", map[string]any{"harness": "codex"})
	if err == nil {
		t.Fatal("the tool action gate did not apply the caller ban")
	}
}
