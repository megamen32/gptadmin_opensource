package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"sort"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/audit"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/mcpclient"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// supervisorActionReq is the JSON shape accepted on /capabilities/mcp/:
//   - "action" + "ref" both in body, OR
//   - "ref" in the URL path (the trailing path segment) and "action" in body/query.
type supervisorActionReq struct {
	Action string `json:"action"`
	Ref    string `json:"ref"`
}

// supervisorHandler implements the deprecated POST /capabilities/mcp/ API.
// It intentionally delegates to childMCP so this compatibility path cannot
// create a second detached process outside the MCP protocol session owner.
func (s *Server) supervisorHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	var req supervisorActionReq
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}

	// Fall back to the URL path: the trailing segment after /capabilities/mcp/
	// is treated as the agent ref.
	if req.Ref == "" {
		req.Ref = strings.TrimPrefix(r.URL.Path, "/capabilities/mcp/")
		req.Ref = strings.Trim(req.Ref, "/")
	}
	// And the query string, mirroring the Python shellmcp behavior.
	if req.Action == "" {
		req.Action = r.URL.Query().Get("action")
	}
	if req.Ref == "" {
		req.Ref = r.URL.Query().Get("ref")
	}

	if req.Action == "" || req.Ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing action or ref"})
		return
	}

	mgr := s.supervisor
	if mgr == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "supervisor not initialized"})
		return
	}

	s.auditLog.Event(audit.ServiceAction, map[string]any{
		"service": "supervisor",
		"action":  req.Action,
		"ref":     req.Ref,
	})

	agent, err := mgr.Agent(req.Ref)
	if err != nil {
		if errors.Is(err, supervisor.ErrUnknownRef) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error(), "ref": req.Ref})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "ref": req.Ref, "action": req.Action})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	status := s.childMCP.Status(req.Ref)
	switch req.Action {
	case "start":
		err = s.childMCP.Start(ctx, agent)
	case "stop":
		err = s.childMCP.Close(req.Ref)
	case "restart":
		err = s.childMCP.Restart(ctx, agent)
	case "status":
		status = s.childMCP.Status(req.Ref)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action: " + req.Action})
		return
	}
	if err != nil {
		// Distinguish "unknown ref" so clients can recover.
		if errors.Is(err, supervisor.ErrUnknownRef) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error(), "ref": req.Ref})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "ref": req.Ref, "action": req.Action})
		return
	}

	resp := map[string]any{
		"ok":     true,
		"action": req.Action,
		"ref":    req.Ref,
	}
	if req.Action == "status" {
		resp["running"] = status.Running
		resp["pid"] = status.PID
		resp["started_at"] = status.StartedAt
		resp["exited_at"] = status.ExitedAt
		resp["exit_code"] = status.ExitCode
	}
	writeJSON(w, http.StatusOK, resp)
}

// mcpAgentsForCapabilities summarizes the supervisor's agent registry in
// the shape clients expect on /capabilities.
func (s *Server) mcpAgentsForCapabilities() []map[string]any {
	if s.supervisor == nil {
		return []map[string]any{}
	}
	agents := s.supervisor.Agents()
	health := map[string]map[string]any{}
	if value := s.mcpHealth.Load(); value != nil {
		health, _ = value.(map[string]map[string]any)
	}
	out := make([]map[string]any, 0, len(agents))
	runnerUser := ""
	if runner, err := user.Current(); err == nil {
		runnerUser = runner.Username
	}
	for _, a := range agents {
		safeAgent := redactMCPAgent(a)
		configuredUser := ""
		if a.User != "" {
			if owner, err := user.Lookup(a.User); err == nil {
				configuredUser = owner.Username
			}
		}
		descriptor := map[string]any{
			"ref":                       a.Ref,
			"name":                      a.Name,
			"transport":                 a.Transport,
			"command":                   safeAgent.Command,
			"args":                      safeAgent.Args,
			"url":                       safeAgent.URL,
			"enabled":                   a.Enabled,
			"connection_mode":           a.ConnectionMode,
			"owner_profile_id":          a.OwnerProfileID,
			"connection_scopes_version": 1,
			"connection_instance":       s.mcpConnectionInstance(a.Ref),
			"managed_stdio_owner":       configuredUser != "" && configuredUser == runnerUser,
			"managed_stdio_platform":    mcpclient.ScopedProcessGroupsSupported(),
		}
		// Metadata generation survives process restarts but changes with the
		// actual definition, including credentials. Never publish the definition.
		descriptor["catalog_config_digest"] = mcpCatalogConfigDigest(a)
		if current, ok := health[a.Ref]; ok {
			descriptor["health"] = current
			if protocol, ok := current["protocol"].(map[string]any); ok {
				if digest, ok := protocol["catalog_digest"].(string); ok && digest != "" {
					descriptor["catalog_digest"] = digest
				}
			}
		} else {
			descriptor["health"] = unknownMCPHealth()
		}
		out = append(out, descriptor)
	}
	return out
}

func (s *Server) mcpConnectionInstance(ref string) string {
	if s.connectionInstance == "" || s.childMCP == nil {
		return ""
	}
	return s.mcpConnectionInstanceAt(ref, s.childMCP.Generation(ref))
}

func (s *Server) mcpConnectionInstanceAt(ref string, generation uint64) string {
	if s.connectionInstance == "" {
		return ""
	}
	value := fmt.Sprintf("%s\x00%s\x00%d", s.connectionInstance, ref, generation)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func mcpCatalogDigest(tools []map[string]any) string {
	rows := make([]string, 0, len(tools))
	for _, tool := range tools {
		b, _ := json.Marshal(tool)
		rows = append(rows, string(b))
	}
	sort.Strings(rows)
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mcpCatalogConfigDigest fences cached metadata by effective credential inputs.
// Hash only: neither resolved headers nor inherited environment leave this node.
// Include the full stdio environment because legacy unscoped clients inherit it;
// the narrower scoped-client allowlist alone cannot fence those credentials.
func mcpCatalogConfigDigest(agent supervisor.Agent) string {
	effective := agent
	effective.Headers = make(map[string]string, len(agent.Headers))
	effective.Env = make(map[string]string, len(agent.Env))
	for key, value := range agent.Headers {
		effective.Headers[key] = os.ExpandEnv(value)
	}
	for key, value := range agent.Env {
		effective.Env[key] = os.ExpandEnv(value)
	}
	var environment []string
	if agent.Transport == "stdio" {
		environment = os.Environ()
		sort.Strings(environment)
	}
	encoded, _ := json.Marshal(struct {
		Definition  supervisor.Agent
		Effective   supervisor.Agent
		Environment []string
	}{agent, effective, environment})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
