package hub

import (
	"net/http"
	"strings"
	"testing"
)

func TestCodeModeTypedToolsRejectWrongTypeBeforeMCPDispatch(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	result, status := codeModeRun(t, host.server, r, map[string]any{
		"code": `return await tools["shell:fixture"].shell_exec({cmd: 42});`,
	})
	if status == http.StatusOK {
		t.Fatalf("invalid typed tool was accepted: %#v", result)
	}
	err := mapValue(result["error"])
	if firstString(err, "code") != "invalid_arguments" {
		t.Fatalf("wrong error code instead of JSON Schema validation: %#v", result)
	}
	validation := mapValue(mapValue(err["receipt"])["validation"])
	if firstString(validation, "path") != "/cmd" || firstString(validation, "keyword") != "type" ||
		validation["expected"] != "string" || firstString(validation, "actual") != "integer" {
		t.Fatalf("typed MCP error is not actionable: %#v", result)
	}
	if host.agent.callCount() != 0 {
		t.Fatalf("bad arguments reached remote MCP host: calls=%d", host.agent.callCount())
	}
}

func TestCodeModeTypedToolsRejectUnknownPropertyAndMissingField(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	for _, code := range []string{
		`return await tools["shell:fixture"].shell_exec({argv: ["printf", "ok"], cmd: "printf bad"});`,
		`return await tools["file:fixture"].file_editor({action:"create",path:"/tmp/demo",content:42});`,
		`return await tools.hub.settings_schema({unknown_flag: true});`,
	} {
		result, status := codeModeRun(t, host.server, r, map[string]any{"code": code})
		if status == http.StatusOK || firstString(mapValue(result["error"]), "code") != "invalid_arguments" {
			t.Fatalf("invalid JSON object was dispatched: code=%s result=%#v", code, result)
		}
	}
	if host.agent.callCount() != 0 {
		t.Fatalf("invalid arguments reached MCP executor: %d", host.agent.callCount())
	}
}

func TestCodeModeTypedToolsAcceptLiteralTextAndRetainOldCall(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	secret := "with \"double\" and 'single' $HOME `not-evaluated` Привет 🌍"
	code := `const first=await tools["shell:fixture"].shell_exec({cmd:"printf '%s' ok"});const second=await mcp.call("shell:fixture","shell_exec",{cmd:"printf '%s' legacy"});return {a:first.stdout,b:second.stdout};`
	result, status := codeModeRun(t, host.server, r, map[string]any{"code": code, "timeout_ms": 11000})
	got := mapValue(result["result"])
	if status != http.StatusOK || got["a"] != "ok" || got["b"] != "legacy" {
		t.Fatalf("compatible tools and direct call failed: status=%d body=%#v", status, result)
	}
	if strings.Contains(resultString(result), secret) {
		t.Fatal("sensitive value unexpectedly appeared in the output")
	}
	if len(host.agent.callsFor("shell_exec")) != 2 {
		t.Fatalf("expected both calls to execute: %#v", result)
	}
}

func TestCodeModeTypedToolsReadVirtualMCPInputSchemaBeforeDispatch(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	s.mu.Lock()
	s.virtualMCP["webhooks"] = true
	s.mu.Unlock()
	result, status := codeModeRun(t, s, r, map[string]any{
		"code": `return await tools["webhooks"].webhook_route_create({route:42});`,
	})
	if status == http.StatusOK || firstString(mapValue(result["error"]), "code") != "invalid_arguments" {
		t.Fatalf("external virtual MCP schema was not enforced: status=%d %#v", status, result)
	}
	if len(s.listWebhookRouteSummaries()) != 0 {
		t.Fatalf("invalid typed request created a webhook route: %#v", result)
	}
}

func TestCodeModeManualSchemaAlsoValidatesCall(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	result, status := codeModeRun(t, host.server, r, map[string]any{
		"code": `await mcp.schema("shell:fixture","shell_exec");return await mcp.call("shell:fixture","shell_exec",{cmd:true});`,
	})
	failure := mapValue(result["error"])
	validation := mapValue(mapValue(failure["receipt"])["validation"])
	if status == http.StatusOK || firstString(failure, "code") != "invalid_arguments" ||
		firstString(validation, "path") != "/cmd" || host.agent.callCount() != 0 {
		t.Fatalf("manual schema did not guard call: %d %#v", status, result)
	}
}

func TestCodeModeDirectMCPCallUsesBuiltinJSONSchema(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	result, status := codeModeRun(t, host.server, r, map[string]any{
		"code": `return await mcp.call("shell:fixture","shell_exec",{cmd:17});`,
	})
	if status == http.StatusOK || host.agent.callCount() != 0 {
		t.Fatalf("legacy call skipped Hub preflight: status=%d %#v", status, result)
	}
	failure := mapValue(result["error"])
	validation := mapValue(mapValue(failure["receipt"])["validation"])
	if firstString(failure, "code") != "invalid_arguments" || firstString(validation, "path") != "/cmd" {
		t.Fatalf("legacy typed error lost field details: %#v", result)
	}
}

func resultString(value any) string {
	return adminPreview(value)
}
