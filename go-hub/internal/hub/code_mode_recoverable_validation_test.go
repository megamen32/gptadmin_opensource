package hub

import (
	"net/http"
	"testing"
)

func TestCodeModeRecoversInvalidArgumentsWithoutDispatch(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	body, status := codeModeRun(t, host.server, r, map[string]any{"code": `
 let problem;
 try {await tools["shell:fixture"].shell_exec({cmd:42});}
 catch(e){ problem={code:e.code,path:e.validation?.path,expected:e.validation?.expected};}
 const ok=await tools["shell:fixture"].shell_exec({cmd:"printf 'fixed'"});
 return {problem, stdout:ok.stdout};
 `})
	if status != http.StatusOK {
		t.Fatalf("pre-dispatch validation must be repairable: %d %#v", status, body)
	}
	got := mapValue(body["result"])
	problem := mapValue(got["problem"])
	if firstString(problem, "code") != "invalid_arguments" || firstString(problem, "path") != "/cmd" || problem["expected"] != "string" || got["stdout"] != "fixed" {
		t.Fatalf("missing structured validation or real tool result: %#v", body)
	}
	if len(host.agent.callsFor("shell_exec")) != 1 {
		t.Fatalf("invalid call was dispatched or corrected call lost: %#v", body)
	}
	if intFromAny(body["calls"]) != 1 {
		t.Fatalf("typed preflight consumed a tool-call slot: %#v", body)
	}
}

func TestCodeModeDirectCallCanRepairPreflightWithoutStickyFailure(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	body, status := codeModeRun(t, host.server, r, map[string]any{"code": `
 let problem;
 try {await mcp.call("shell:fixture","shell_exec",{cmd:99});}
 catch(e){problem={code:e.code,path:e.validation?.path};}
 const ok=await mcp.call("shell:fixture","shell_exec",{cmd:"printf fixed"});
 return {problem,stdout:ok.stdout};
 `})
	if status != http.StatusOK || firstString(mapValue(body["result"]), "stdout") != "fixed" ||
		firstString(mapValue(mapValue(body["result"])["problem"]), "path") != "/cmd" {
		t.Fatalf("direct MCP validation could not be repaired in same run: %d %#v", status, body)
	}
	if host.agent.callCount() != 1 {
		t.Fatalf("validation had side effects: %#v", body)
	}
}

func TestCodeModeNonterminalAfterValidDispatchStillSticky(t *testing.T) {
	// A schema preflight does not relax existing auth/approval/network fences.
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	body, status := codeModeRun(t, host.server, r, map[string]any{"code": `
 try {await tools["shell:fixture"].shell_exec({cmd:10});}catch(e){}
 try {await tools["shell:fixture"].shell_exec({cmd:"printf allowed"});}catch(e){}
 return "attempted";
 `})
	if status != http.StatusOK || firstString(body, "status") != "completed" || host.agent.callCount() != 1 {
		t.Fatalf("preflight recovery did not preserve normal execution: %d %#v", status, body)
	}
}
