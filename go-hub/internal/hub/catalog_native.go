package hub

import (
	"context"
	"net/http"
)

// The public tool catalog has two explicit modes. Both are chosen by an
// administrator in the typed Hub settings registry (tool_catalog_mode).
// Nothing is inferred from a user agent, a language header or a connection
// shape: a client that loaded the catalog must be able to read which mode is
// active from the same settings API it already uses.
//
//	compat: the short catalog carries the compatibility execute facade next
//	        to the code-mode facade. Legacy clients keep one entry point that
//	        routes every capability, and the working code mode stays visible.
//	modern: the short catalog prefers the code-mode facades (exec/ro) and
//	        the FTS search facade, and hides the compatibility execute.
const (
	catalogModeCompat = "compat"
	catalogModeModern = "modern"
)

// compatShortCatalogNames is the routing set of the compat mode. It carries
// both executors, the search facade, the schema and discover primitives and
// the compatibility execute facade. The remaining capabilities stay
// reachable through the execute facade, which routes every capability:
// job and expand through execute(target="hub", tool=...), the ui facade
// through the full catalog.
var compatShortCatalogNames = []string{"discover", "search", "schema", "exec", "ro", "execute"}

// modernShortCatalogNames is the routing set of the modern mode. It is the
// code-mode first catalog: search finds a tool, exec runs it, ro runs a
// bounded read-only script.
var modernShortCatalogNames = []string{"ui", "discover", "search", "schema", "exec", "ro", "job", "expand"}

// readOnlyShortCatalogNames is the routing set of a read-only caller. It
// carries the read primitives and the read-only code facade, and never the
// write-capable execute or exec facades.
var readOnlyShortCatalogNames = []string{"ui", "discover", "search", "schema", "ro"}

type catalogModeKey struct{}

// requestWithCatalogMode attaches the active catalog mode to the request so
// the catalog builders stay free functions with a stable signature.
func requestWithCatalogMode(r *http.Request, mode string) *http.Request {
	if r == nil {
		return nil
	}
	return r.WithContext(context.WithValue(r.Context(), catalogModeKey{}, mode))
}

// catalogModeFromRequest returns the catalog mode for this request. An absent
// or unknown mode is the compat mode: the compatibility entry point must
// never disappear because of a missing context value.
func catalogModeFromRequest(r *http.Request) string {
	if r == nil {
		return catalogModeCompat
	}
	mode, _ := r.Context().Value(catalogModeKey{}).(string)
	if mode != catalogModeModern {
		return catalogModeCompat
	}
	return mode
}

// toolCatalogMode returns the explicitly configured catalog mode.
func (s *Server) toolCatalogMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hubSettingStringLocked("tool_catalog_mode")
}

// nativeShortCatalogTools builds the short routing catalog of the active
// mode. The code-mode and search facades are published separately, because
// the Apps SDK catalog keeps one execution entry point and stays inside its
// payload budget.
func nativeShortCatalogTools(r *http.Request, tools []map[string]any) []map[string]any {
	names := compatShortCatalogNames
	if catalogModeFromRequest(r) == catalogModeModern {
		names = modernShortCatalogNames
	}
	if requestAccessMode(r) == accessModeReadonly {
		// A read-only caller must not be offered a write-capable routing
		// facade: the execute and exec entries are refused for it anyway, and
		// advertising a tool the caller may not use is catalog hygiene. The
		// read-only code facade ro is the only executor it may load.
		names = readOnlyShortCatalogNames
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
			"name": "ro", "title": "Read only code", "description": "Run bounded JavaScript with a hard read only execution ceiling. Shell commands, writes and persistent indexing are refused.",
			"inputSchema": readonlyExecInputSchema(), "outputSchema": codeModeOutputSchema(),
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}, "securitySchemes": readSecurity, "_meta": readMeta,
		}
	}
	return map[string]any{
		"name": "exec", "title": "Think in Code", "description": "Run short JavaScript in the isolated sandbox. Use search then await mcp.call(target, tool, args); intermediate data stays inside the VM and only your return value is sent.",
		"inputSchema": codeModeInputSchema(), "outputSchema": codeModeOutputSchema(),
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": true}, "securitySchemes": execSecurity, "_meta": execMeta,
	}
}

// withoutExecuteFacade hides the direct execute facade from a read-only
// caller. Every business call through it is denied by the read-only ceiling,
// and the catalog must not advertise a write path to a read-only client: the
// read-only code facade ro is the only execution surface it may use.
func withoutExecuteFacade(r *http.Request, tools []map[string]any) []map[string]any {
	if r == nil || requestAccessMode(r) != accessModeReadonly {
		return tools
	}
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
