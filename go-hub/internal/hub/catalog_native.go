package hub

import (
	"net/http"
)

// The public tool catalog of GPTAdmin is deliberately short:
//
//	search: find the capability that is needed.
//	ro:     read and analyse without changing anything.
//	exec:   run JavaScript, including several MCP calls and the processing of
//	        their results.
//
// The direct execute(target, tool, args) facade is not public. It invites one
// tool call per request instead of one script that batches the calls, and its
// name costs more tokens per call than the short names above. The internal
// dispatcher stays: ro, exec, the relay and the agent routes use it.
//
// The task readers (job, expand) stay published, because a client has to be
// able to read the result of a task it started.

// publicCatalogNames is the routing set of the public catalog.
var publicCatalogNames = []string{"discover", "search", "schema", "exec", "ro", "job", "expand"}

// readOnlyCatalogNames is the routing set of a read-only caller. It carries the
// read primitives, the task readers and the read-only code facade, and never
// the write-capable exec facade.
var readOnlyCatalogNames = []string{"ui", "discover", "search", "schema", "ro", "job", "expand"}

// nativeShortCatalogTools builds the short routing catalog. The code-mode and
// search facades are published separately, because the Apps SDK catalog keeps
// one execution entry point and stays inside its payload budget.
func nativeShortCatalogTools(r *http.Request, tools []map[string]any) []map[string]any {
	names := publicCatalogNames
	if requestAccessMode(r) == accessModeReadonly {
		// A read-only caller must not be offered a write-capable routing
		// facade: the exec entry is refused for it anyway, and advertising a
		// tool the caller may not use is catalog hygiene. The read-only code
		// facade ro is the only executor it may load.
		names = readOnlyCatalogNames
	}
	byName := make(map[string]map[string]any, len(tools))
	for _, tool := range tools {
		byName[firstString(tool, "name")] = tool
	}
	extra := map[string]map[string]any{
		"search": searchCatalogTool(),
		"exec":   codeModeCatalogTool("exec"),
		"ro":     codeModeCatalogTool("ro"),
	}
	selected := make([]map[string]any, 0, len(names))
	for _, name := range names {
		if tool, ok := byName[name]; ok {
			selected = append(selected, tool)
			continue
		}
		if tool, ok := extra[name]; ok {
			selected = append(selected, tool)
		}
	}
	return selected
}

// searchCatalogTool publishes the FTS search facade for the short catalog.
func searchCatalogTool() map[string]any {
	tool := searchToolDefinition()
	tool["outputSchema"] = map[string]any{"type": "object", "additionalProperties": true}
	tool["_meta"] = map[string]any{"securitySchemes": []map[string]any{{"type": "oauth2", "scopes": []string{"gptadmin.read"}}}, "openai/toolInvocation/invoked": "Loaded."}
	tool["securitySchemes"] = []map[string]any{{"type": "oauth2", "scopes": []string{"gptadmin.read"}}}
	return tool
}

// codeModeCatalogTool publishes one code-mode facade.
func codeModeCatalogTool(name string) map[string]any {
	readSecurity := []map[string]any{{"type": "oauth2", "scopes": []string{"gptadmin.read"}}}
	execSecurity := []map[string]any{{"type": "oauth2", "scopes": []string{"gptadmin.read", "gptadmin.exec"}}}
	readMeta := map[string]any{"securitySchemes": readSecurity, "openai/toolInvocation/invoked": "Loaded."}
	execMeta := map[string]any{"securitySchemes": execSecurity, "openai/toolInvocation/invoked": "Done."}
	if name == "ro" {
		return map[string]any{
			"name": "ro", "title": "Read only code", "description": "Read-only JavaScript sandbox. Write an async code body that calls await mcp.call(target, tool, args) for reads such as list_directory, search, status or pending. Shell commands, writes and indexing are refused. Filter inside the code and return only what you need; output is size-limited.",
			"inputSchema": readonlyExecInputSchema(), "outputSchema": codeModeOutputSchema(),
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}, "securitySchemes": readSecurity, "_meta": readMeta,
		}
	}
	return map[string]any{
		"name": "exec", "title": "Compose MCP calls", "description": "JavaScript sandbox for any tool call, including writes and shell: mcp.call(target, tool, args) returns a Promise. Start independent calls together with await Promise.all([...]) so Hub dispatches them concurrently; use sequential await only when a later call depends on an earlier result. Explicitly return the final value. Catching a failed nested call does not turn the run into success: Hub preserves the original failure receipt. For example, call('shell:<host>', 'shell_exec', {cmd}) can run inside Promise.all. Do loops, filtering and joins inside the code and return only the final value; that costs far fewer tokens than many separate calls. Find tools with search and exact arguments with schema. No filesystem, environment, network or timer access. When a project path is known, use local rg for file or content search; use GrepMesh search or find_paths for unknown locations or cross-host search, then read_text for the exact result.",
		"inputSchema": codeModeInputSchema(), "outputSchema": codeModeOutputSchema(),
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": true}, "securitySchemes": execSecurity, "_meta": execMeta,
	}
}

// withoutExecuteFacade hides the direct execute facade from every catalog.
// The facade is not public: it invites one tool call per request instead of
// one script that batches the calls. The internal dispatcher stays, so ro,
// exec, the relay and the agent routes keep working.
func withoutExecuteFacade(r *http.Request, tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if firstString(tool, "name") == "execute" {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// withReadOnlyExecTool advertises the read-only code facade to a read-only
// caller. The facade is not part of the Apps SDK catalog, but a read-only
// client has no other entry point for a bounded read script: exec is refused
// for it and the direct execute facade cannot lower the read-only ceiling.
func withReadOnlyExecTool(r *http.Request, tools []map[string]any) []map[string]any {
	if r == nil || requestAccessMode(r) != accessModeReadonly {
		return tools
	}
	if !profileAllowsTool(r, "ro") {
		return tools
	}
	for _, tool := range tools {
		if firstString(tool, "name") == "ro" {
			return tools
		}
	}
	out := make([]map[string]any, 0, len(tools)+1)
	out = append(out, tools...)
	return append(out, codeModeCatalogTool("ro"))
}

// withSearchFacadeTool publishes the FTS search facade to a caller whose
// catalog does not carry it yet. A read-only identity needs the search facade
// to find the tools it is allowed to read.
func withSearchFacadeTool(r *http.Request, tools []map[string]any) []map[string]any {
	for _, tool := range tools {
		if firstString(tool, "name") == "search" {
			return tools
		}
	}
	out := make([]map[string]any, 0, len(tools)+1)
	out = append(out, tools...)
	return append(out, searchCatalogTool())
}
