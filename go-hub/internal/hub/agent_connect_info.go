package hub

import (
	"net/http"
	"strings"
)

// GET /admin/api/agents/connect-info returns the operator-facing connection
// code for onboarding a new shell MCP agent: the canonical hub origin, the
// shared agent bearer (SHELL_TOKEN) and a ready-to-paste env block. It sits
// behind the same admin authority as every other admin API (CTL bearer or
// admin session cookie) and is always served no-store: the payload carries a
// live credential. A hub without a shared agent token (per-executor tokens
// only) reports the gap instead of inventing a credential.
func (s *Server) adminAgentConnectInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "GET required"})
		return
	}
	if s.cfg.ShellToken == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"detail":
			"shared agent token is not configured on this hub (SHELL_TOKEN is empty); per-executor tokens cannot be issued here"})
		return
	}
	origin := s.origin(r)
	envBlock := strings.Join([]string{
		"HUB_URL=" + origin,
		"SHELL_TOKEN=" + s.cfg.ShellToken,
		"SHELL_NAME=new-agent",
		"SHELLMCP_TRANSPORT=polling",
	}, "\n")
	writeJSON(w, http.StatusOK, map[string]any{
		"hub_url":         origin,
		"mcp_endpoint":    origin + "/mcp",
		"shell_token":     s.cfg.ShellToken,
		"transport":       "polling",
		"install_command": "curl -s https://raw.githubusercontent.com/megamen32/gptadmin_opensource/main/deploy/install.sh | bash",
		"env_block":       envBlock,
	})
}
