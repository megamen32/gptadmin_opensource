package hub

import (
	"encoding/json"
	"html"
	"net/http"
)

func (s *Server) connectionPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	s.recordActivationTelemetry("connection_page_viewed")
	origin := s.origin(r)
	clientConfigs := map[string]any{}
	for _, clientID := range []string{"codex", "claude", "chatgpt", "custom"} {
		config := map[string]any{
			"transport":            "streamable_http",
			"url":                  origin + "/mcp",
			"authentication":       "oauth2_pkce",
			"authorization_server": origin + "/.well-known/oauth-authorization-server",
			"resource":             origin,
			"first_action":         map[string]any{"method": "tools/call", "tool": "demo", "arguments": map[string]any{}},
		}
		if clientID == "chatgpt" {
			config["actions_openapi"] = origin + "/actions/openapi.yaml"
		}
		clientConfigs[clientID] = config
	}
	payload := map[string]any{
		"hub_url":                    origin,
		"mcp_endpoint":               origin + "/mcp",
		"oauth_authorization_server": origin + "/.well-known/oauth-authorization-server",
		"oauth_protected_resource":   origin + "/.well-known/oauth-protected-resource",
		"actions_openapi":            origin + "/actions/openapi.yaml",
		"connection_principle":       "Use this Hub URL; the client-specific protocol details are derived here.",
		"client_configs":             clientConfigs,
		"clients": []map[string]any{
			{"id": "codex", "label": "Codex", "method": "OAuth Authorization Code + PKCE", "endpoint": origin + "/mcp"},
			{"id": "claude", "label": "Claude-compatible", "method": "OAuth Authorization Code + PKCE", "endpoint": origin + "/mcp"},
			{"id": "chatgpt", "label": "ChatGPT / Custom GPT", "method": "OAuth Authorization Code + PKCE", "endpoint": origin + "/mcp"},
			{"id": "custom", "label": "Custom MCP client", "method": "OAuth Authorization Code + PKCE", "endpoint": origin + "/mcp"},
		},
	}
	if r.URL.Path == "/connect.json" || r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, payload)
		return
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to render connection page"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	page := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect to GPTAdmin</title><style>body{font-family:system-ui,sans-serif;max-width:760px;margin:48px auto;padding:0 20px;color:#16232a;background:#f6f4ee}main{background:#fffdf8;border:1px solid #d9d4c8;border-radius:18px;padding:28px;box-shadow:0 15px 40px rgba(40,35,25,.08)}h1{margin-top:0}code{padding:3px 6px;border-radius:6px;background:#eeeae1}li{margin:12px 0}.muted{color:#64736e}.button{display:inline-block;margin-top:12px;padding:10px 14px;border-radius:8px;background:#2d5b52;color:white;text-decoration:none}</style></head><body><main><h1>Connect your MCP client</h1><p class="muted">Use one canonical Hub URL. Client-specific OAuth and protocol details are derived from it.</p><p><code>` + html.EscapeString(origin) + `</code></p><ul><li data-client="codex">Codex — OAuth Authorization Code + PKCE</li><li data-client="claude">Claude-compatible clients — OAuth Authorization Code + PKCE</li><li data-client="chatgpt">ChatGPT / Custom GPT — OAuth Authorization Code + PKCE</li></ul><a class="button" href="` + html.EscapeString(origin+"/.well-known/oauth-authorization-server") + `">View OAuth metadata</a><a class="button" href="` + html.EscapeString(origin+"/actions/openapi.yaml") + `">View Action schema</a><script type="application/json" id="connection-data">` + html.EscapeString(string(encoded)) + `</script></main></body></html>`
	_, _ = w.Write([]byte(page))
}
