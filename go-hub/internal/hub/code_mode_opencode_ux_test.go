package hub

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCodeModeOpenCodeSearchObjectWithItemsAndPaths(t *testing.T) {
	s, request := codeModeFixture(t)
	defer s.Close()
	code := "const items = search({query:'exec',limit:5});return JSON.stringify(items.items.map(function(i){return {path:i.path,desc:i.description.slice(0,140)};}),null,1);"
	response, status := codeModeRun(t, s, request, map[string]any{"code": code})
	if status != http.StatusOK {
		t.Fatalf("search object syntax did not execute: status=%d response=%#v", status, response)
	}
	var items []struct {
		Path string `json:"path"`
		Desc string `json:"desc"`
	}
	if err := json.Unmarshal([]byte(firstString(response, "result")), &items); err != nil {
		t.Fatalf("the example's returned JSON is invalid: %v, result=%#v", err, response)
	}
	if len(items) == 0 || len(items) > 5 {
		t.Fatalf("search({query,limit}) ignored limit or returned no results: %#v", items)
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Path, "tools[") || !strings.Contains(item.Path, "][") || len(item.Desc) > 140 {
			t.Fatalf("search returned an invalid callable path or description: %#v", item)
		}
	}
	calls := outputRows(response["tool_calls"])
	if len(calls) != 1 || firstString(mapValue(calls[0]), "name") != "search" || firstString(mapValue(calls[0]), "path") != "search" {
		t.Fatalf("search must appear in the independent Tool calls list: %#v", calls)
	}
	if _, exposed := mapValue(calls[0])["args"]; exposed {
		t.Fatal("search arguments must not be echoed in the call metadata")
	}
}

func TestCodeModeSearchObjectAndStringPreserveLegacyResults(t *testing.T) {
	s, request := codeModeFixture(t)
	defer s.Close()
	code := "const a=search({query:'exec',limit:3});const b=search('exec',{limit:2});return {a:a.items.length,ar:a.results.length,b:b.results.length,bi:b.items.length,ok:a.items[0].path.startsWith('tools['),lean:!Object.keys(a).includes('items')};"
	response, status := codeModeRun(t, s, request, map[string]any{"code": code})
	data := mapValue(response["result"])
	if status != http.StatusOK || intFromAny(data["a"]) == 0 || intFromAny(data["a"]) > 3 ||
		intFromAny(data["a"]) != intFromAny(data["ar"]) || intFromAny(data["b"]) != intFromAny(data["bi"]) ||
		intFromAny(data["b"]) > 2 || data["ok"] != true || data["lean"] != true {
		t.Fatalf("search compatibility failed: status=%d result=%#v", status, response)
	}
}

func TestCodeModeOpenCodeToolsExactTargetAndHubAlias(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	request := codeModeHostRequest(t, host)
	code := "const a=await tools['shell:fixture'].shell_exec({cmd:'printf exact'});const b=await tools.hub.settings_schema({});return {a:a.stdout,b:typeof b};"
	response, status := codeModeRun(t, host.server, request, map[string]any{"code": code, "timeout_ms": 13000})
	result := mapValue(response["result"])
	if status != http.StatusOK || result["a"] != "exact" || result["b"] != "object" {
		t.Fatalf("OpenCode tools shorthand failed: %d %#v", status, response)
	}
	operations := outputRows(response["tool_calls"])
	if len(operations) != 2 {
		t.Fatalf("missing structured tool call list: %#v", response)
	}
	for _, op := range operations {
		row := mapValue(op)
		if row["status"] != "completed" || !((row["target"] == "shell:fixture" && row["name"] == "shell_exec") || (row["target"] == "hub" && row["name"] == "settings_schema")) {
			t.Fatalf("tool_calls metadata incorrect: %#v", operations)
		}
		if _, leaked := row["args"]; leaked {
			t.Fatal("tool call list leaked arguments")
		}
	}
}

func TestCodeModeAmbiguousAliasDoesNotCallRandomServer(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	request := codeModeHostRequest(t, host)
	host.server.mu.Lock()
	for _, id := range []string{
		"mcp:shell:node-a:AgentHerder", "mcp:shell:node-b:AgentHerder",
	} {
		host.server.agents[id] = &Agent{
			AgentID: id, Name: "AgentHerder", Kind: "mcp", Status: "online",
			Meta: map[string]any{"approved": true},
		}
	}
	host.server.mu.Unlock()
	code := "try{await tools['agent-herder'].resume_after_status({});return 'unsafe'}catch(e){return e.message}"
	response, status := codeModeRun(t, host.server, request, map[string]any{"code": code})
	text := firstString(response, "result")
	if status != http.StatusOK || !strings.Contains(text, "Ambiguous MCP alias") ||
		!strings.Contains(text, "node-a") || !strings.Contains(text, "node-b") ||
		host.agent.callCount() != 0 {
		t.Fatalf("ambiguous alias was not blocked: %d %#v", status, response)
	}
}

func TestCodeModeToolsShortcutPreservesReadonlyPolicy(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	request := codeModeHostRequest(t, host)
	response, status := host.server.callCodeModeToolMode(request, map[string]any{
		"code":       "return await tools['shell:fixture'].shell_exec({cmd:'echo forbidden'});",
		"timeout_ms": 5000,
	}, true)
	if status == http.StatusOK || firstString(mapValue(response["error"]), "code") != "forbidden" ||
		host.agent.callCount() != 0 {
		t.Fatalf("tools proxy bypassed read-only ceiling: status=%d response=%#v", status, response)
	}
}
