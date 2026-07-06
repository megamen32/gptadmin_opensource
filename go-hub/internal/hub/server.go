package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var BuildVersion = "go-dev"
var GitCommit = "worktree"

type Config struct {
	Addr                     string
	ConfigDir                string
	PublicDir                string
	ArtifactDir              string
	CtlToken                 string
	RelayAgentToken          string
	ShellToken               string
	DefaultTimeout           time.Duration
	PollMaxTimeout           time.Duration
	OutputDir                string
	PublicOrigin             string
	MCPResource              string
	AdminPassword            string
	OAuthClientSecret        string
	OAuthPermissiveRedirects bool
	OAuthPermissiveResources bool
	AuthLogSecrets           bool
	BridgeKey                string
	RegistryStateFile        string
	FailoverConfigFile       string
	FailoverStateFile        string
}

func FromEnv() Config {
	port := env("GPTADMIN_HUB_PORT", env("HUB_PORT", env("PORT", "9001")))
	host := env("GPTADMIN_HUB_HOST", env("HUB_HOST", ""))
	root := env("GPTADMIN_ROOT", ".")
	cfgDir := env("GPTADMIN_CONFIG_DIR", filepath.Join(root, "config"))
	defTimeout := secondsEnv("MCP_RELAY_DEFAULT_TIMEOUT", 30)
	pollTimeout := secondsEnv("MCP_RELAY_POLL_MAX_TIMEOUT", 55)
	return Config{
		Addr:                     host + ":" + port,
		ConfigDir:                cfgDir,
		PublicDir:                env("GPTADMIN_PUBLIC_DIR", filepath.Join(root, "public")),
		ArtifactDir:              env("GPTADMIN_ARTIFACT_DIR", filepath.Join(root, "build")),
		CtlToken:                 env("CTL_TOKEN", env("GPTADMIN_CTL_TOKEN", "")),
		RelayAgentToken:          env("MCP_RELAY_AGENT_TOKEN", env("GPTADMIN_MCP_RELAY_AGENT_TOKEN", "")),
		ShellToken:               env("SHELL_TOKEN", env("SHELLMCP_TOKEN", "")),
		DefaultTimeout:           time.Duration(defTimeout) * time.Second,
		PollMaxTimeout:           time.Duration(pollTimeout) * time.Second,
		OutputDir:                env("GPTADMIN_OUTPUT_DIR", filepath.Join(cfgDir, "outputs")),
		PublicOrigin:             strings.TrimRight(env("PUBLIC_ORIGIN", ""), "/"),
		MCPResource:              strings.TrimRight(env("MCP_RESOURCE", env("PUBLIC_ORIGIN", "")), "/"),
		AdminPassword:            env("ADMIN_PASSWORD", ""),
		OAuthClientSecret:        env("OAUTH_CLIENT_SECRET", env("ADMIN_PASSWORD", env("CTL_TOKEN", "gptadmin-dev-secret"))),
		OAuthPermissiveRedirects: truthyString(env("OAUTH_PERMISSIVE_REDIRECTS", "0")),
		OAuthPermissiveResources: truthyString(env("OAUTH_PERMISSIVE_RESOURCES", "0")),
		AuthLogSecrets:           truthyString(env("AUTH_LOG_SECRETS", "0")),
		BridgeKey:                env("MCP_BRIDGE_KEY", env("CTL_TOKEN", "")),
		RegistryStateFile:        env("GPTADMIN_REGISTRY_STATE_FILE", filepath.Join(cfgDir, "registry_state.json")),
		FailoverConfigFile:       env("GPTADMIN_FAILOVER_CONFIG_FILE", filepath.Join(cfgDir, "failover_config.json")),
		FailoverStateFile:        env("GPTADMIN_FAILOVER_STATE_FILE", filepath.Join(cfgDir, "failover_state.json")),
	}
}

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func secondsEnv(k string, d int) int {
	v, err := strconv.Atoi(env(k, ""))
	if err != nil || v <= 0 {
		return d
	}
	return v
}

func truthyString(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

type Agent struct {
	AgentID      string         `json:"agent_id"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Transport    string         `json:"transport"`
	Status       string         `json:"status"`
	LastSeen     float64        `json:"last_seen"`
	Capabilities []string       `json:"capabilities"`
	Meta         map[string]any `json:"meta,omitempty"`
}

type persistentRegistryState struct {
	SavedAt      float64          `json:"saved_at"`
	BuildVersion string           `json:"build_version,omitempty"`
	GitCommit    string           `json:"git_commit,omitempty"`
	Agents       map[string]Agent `json:"agents"`
}

type relayJob struct {
	ID        string         `json:"id"`
	AgentID   string         `json:"agent_id,omitempty"`
	Method    string         `json:"method"`
	Params    map[string]any `json:"params,omitempty"`
	CreatedAt float64        `json:"created_at"`
	StartedAt float64        `json:"started_at,omitempty"`
	DoneAt    float64        `json:"completed_at,omitempty"`
	Status    string         `json:"status"`
	Result    map[string]any `json:"result,omitempty"`
	Error     any            `json:"error,omitempty"`
}

type shellJob struct {
	ID        string         `json:"id"`
	Server    string         `json:"server,omitempty"`
	Cmd       string         `json:"cmd"`
	Cwd       string         `json:"cwd,omitempty"`
	Timeout   int            `json:"timeout,omitempty"`
	Env       map[string]any `json:"env,omitempty"`
	CreatedAt float64        `json:"created_at"`
	StartedAt float64        `json:"started_at,omitempty"`
	DoneAt    float64        `json:"completed_at,omitempty"`
	Status    string         `json:"status"`
	Result    any            `json:"result,omitempty"`
	Error     any            `json:"error,omitempty"`
}

type auditEvent struct {
	Time   string         `json:"time"`
	Name   string         `json:"name"`
	Fields map[string]any `json:"fields,omitempty"`
}

type oauthCode struct {
	Created     time.Time
	Challenge   string
	ClientID    string
	RedirectURI string
	Resource    string
	Scope       string
	State       string
}

type Server struct {
	cfg Config

	mu          sync.Mutex
	cond        *sync.Cond
	agents      map[string]*Agent
	relayQueues map[string][]string
	relayJobs   map[string]*relayJob
	shellQueues map[string][]string
	shellJobs   map[string]*shellJob
	oauthCodes  map[string]oauthCode
	audit       []auditEvent
	failover    FailoverConfig
}

func New(cfg Config) *Server {
	s := &Server{
		cfg:         cfg,
		agents:      map[string]*Agent{},
		relayQueues: map[string][]string{},
		relayJobs:   map[string]*relayJob{},
		shellQueues: map[string][]string{},
		shellJobs:   map[string]*shellJob{},
		oauthCodes:  map[string]oauthCode{},
		audit:       []auditEvent{},
	}
	s.cond = sync.NewCond(&s.mu)
	if err := s.loadRegistryState(); err != nil {
		log.Printf("registry state load failed path=%s err=%v", s.registryStatePath(), err)
	}
	s.failover = s.loadFailoverConfig()
	return s
}

func (s *Server) registryStatePath() string {
	if s.cfg.RegistryStateFile != "" {
		return s.cfg.RegistryStateFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "registry_state.json")
}

func (s *Server) loadRegistryState() error {
	path := s.registryStatePath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var state persistentRegistryState
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	loaded := 0
	for id, agent := range state.Agents {
		if id == "" {
			id = agent.AgentID
		}
		if id == "" || id == "hub" {
			continue
		}
		agent.AgentID = id
		if agent.Status == "" || agent.Status == "online" || agent.Status == "running" {
			agent.Status = "stale"
		}
		if agent.Meta == nil {
			agent.Meta = map[string]any{}
		}
		agent.Meta["restored_from_state"] = true
		agent.Meta["state_file"] = path
		cp := agent
		s.agents[id] = &cp
		loaded++
	}
	if loaded > 0 {
		log.Printf("registry state loaded path=%s agents=%d saved_at=%.0f", path, loaded, state.SavedAt)
	}
	return nil
}

func (s *Server) saveRegistryStateLocked() error {
	path := s.registryStatePath()
	if path == "" {
		return nil
	}
	state := persistentRegistryState{SavedAt: nowFloat(), BuildVersion: BuildVersion, GitCommit: GitCommit, Agents: map[string]Agent{}}
	for id, agent := range s.agents {
		if id == "" || agent == nil || id == "hub" {
			continue
		}
		cp := *agent
		cp.Meta = cloneMap(cp.Meta)
		delete(cp.Meta, "public_mcp_endpoint")
		delete(cp.Meta, "public_mcp_path")
		delete(cp.Meta, "public_mcp_slug")
		delete(cp.Meta, "public_mcp_auth")
		delete(cp.Meta, "exposed_by_default")
		delete(cp.Meta, "restored_from_state")
		delete(cp.Meta, "state_file")
		state.Agents[id] = cp
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Server) ListenAndServe() error {
	if err := os.MkdirAll(s.cfg.OutputDir, 0o750); err != nil {
		log.Printf("output dir unavailable: %v", err)
	}
	log.Printf("gptadmin go hub listening addr=%s config_dir=%s public_dir=%s", s.cfg.Addr, s.cfg.ConfigDir, s.cfg.PublicDir)
	srv := &http.Server{Addr: s.cfg.Addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", s.version)
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/actions/openapi.yaml", s.actionsOpenAPI)
	mux.HandleFunc("/artifacts/shellmcp.json", s.requireCtl(s.shellmcpArtifactManifest))
	mux.HandleFunc("/artifacts/shellmcp.tar.gz", s.requireCtl(s.shellmcpArtifactDownload))
	// Legacy rootd artifact aliases: old services still point ROOTD_UPDATE_MANIFEST_URL here.
	mux.HandleFunc("/artifacts/rootd.json", s.requireCtl(s.shellmcpArtifactManifest))
	mux.HandleFunc("/artifacts/rootd.tar.gz", s.requireCtl(s.shellmcpArtifactDownload))
	mux.HandleFunc("/heartbeat", s.heartbeat)
	mux.HandleFunc("/servers", s.requireCtl(s.serversList))
	mux.HandleFunc("/bulk/exec", s.requireCtl(s.bulkExec))
	mux.HandleFunc("/queue/", s.queue)
	mux.HandleFunc("/tasks/", s.requireCtl(s.tasksEndpoint))
	mux.HandleFunc("/mcp-relay/register", s.mcpRelayRegister)
	mux.HandleFunc("/mcp-relay/poll/", s.mcpRelayPoll)
	mux.HandleFunc("/mcp-relay/result/", s.mcpRelayResult)
	mux.HandleFunc("/mcp-relay/servers", s.requireCtl(s.mcpRelayServers))
	mux.HandleFunc("/mcp-relay/list_mcp_servers", s.requireCtl(s.mcpRelayServers))
	// Legacy aliases kept for old clients only. Do not expose in OpenAPI.
	mux.HandleFunc("/mcp-relay/agents", s.requireCtl(s.mcpRelayAgents))
	mux.HandleFunc("/mcp-relay/list_mcp_agents", s.requireCtl(s.mcpRelayAgents))
	mux.HandleFunc("/mcp-relay/list_mcp_tools", s.requireCtl(s.mcpRelayTools))
	mux.HandleFunc("/mcp-relay/tools", s.requireCtl(s.mcpRelayTools))
	mux.HandleFunc("/mcp-relay/call_mcp_tool", s.requireCtl(s.mcpRelayCall))
	mux.HandleFunc("/mcp-relay/call", s.requireCtl(s.mcpRelayCall))
	mux.HandleFunc("/mcp-relay/get_mcp_job/", s.requireCtl(s.mcpRelayJob))
	mux.HandleFunc("/mcp-relay/job/", s.requireCtl(s.mcpRelayJob))
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.oauthProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.oauthAuthorizationServer)
	mux.HandleFunc("/register", s.oauthRegister)
	mux.HandleFunc("/authorize", s.oauthAuthorize)
	mux.HandleFunc("/token", s.oauthToken)
	mux.HandleFunc("/mcp", s.mcpEndpoint)
	mux.HandleFunc("/server/", s.serverMCPEndpoint)
	// Legacy alias kept for old pinned MCP URLs.
	mux.HandleFunc("/agent/", s.agentMCPEndpoint)
	mux.HandleFunc("/mcp-prompt/prompt", s.mcpPrompt)
	mux.HandleFunc("/mcp-prompt/call", s.mcpPromptCall)
	mux.HandleFunc("/admin/api/mcp/manage", s.requireCtl(s.adminMCPManage))
	mux.HandleFunc("/admin/api/mcp/resources/list", s.requireCtl(s.adminMCPResourcesList))
	mux.HandleFunc("/admin/api/mcp/resources/read", s.requireCtl(s.adminMCPResourceRead))
	mux.HandleFunc("/admin/api/clients/revoke-all", s.requireCtl(s.adminClientsRevokeAll))
	mux.HandleFunc("/admin/api/clients/", s.requireCtl(s.adminClientDelete))
	mux.HandleFunc("/admin/api/overview", s.requireCtl(s.adminOverview))
	mux.HandleFunc("/admin/api/failover/state", s.requireCtl(s.adminFailoverState))
	mux.HandleFunc("/admin/api/failover/reclaim", s.requireCtl(s.adminFailoverReclaim))
	mux.HandleFunc("/admin/api/failover", s.requireCtl(s.adminFailover))
	mux.HandleFunc("/admin/api/jobs", s.requireCtl(s.adminJobs))
	mux.HandleFunc("/admin/api/audit", s.requireCtl(s.adminAudit))
	mux.HandleFunc("/admin/api/clients", s.requireCtl(s.adminClients))
	mux.HandleFunc("/admin/", s.adminStatic)
	mux.HandleFunc("/admin", s.adminIndex)
	return withCORS(mux)
}

func withCORS(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "authorization,content-type,x-ctl-token,x-mcp-relay-token")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": "gptadmin-go-hub", "build_version": BuildVersion, "git_commit": GitCommit})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) requireCtl(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.CtlToken == "" || tokenMatches(r, s.cfg.CtlToken) {
			s.authAudit("ctl_auth_ok", r, map[string]any{"auth_kind": "ctl_token"})
			next(w, r)
			return
		}
		if claims, err := s.verifyBearerJWTFromRequest(r); err == nil {
			s.authAudit("ctl_auth_ok", r, map[string]any{"auth_kind": "oauth_jwt", "jwt_claims": claims})
			next(w, r)
			return
		} else {
			s.authAudit("ctl_auth_denied", r, map[string]any{"reason": err.Error()})
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "unauthorized"})
	}
}

func (s *Server) requireRelay(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.RelayAgentToken == "" || tokenMatches(r, s.cfg.RelayAgentToken) || r.Header.Get("X-MCP-Relay-Token") == s.cfg.RelayAgentToken {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "unauthorized"})
	return false
}

func tokenMatches(r *http.Request, expected string) bool {
	if expected == "" {
		return true
	}
	candidates := []string{
		r.Header.Get("X-CTL-Token"),
		r.Header.Get("X-GPTAdmin-Token"),
		r.Header.Get("X-MCP-Relay-Token"),
		r.URL.Query().Get("token"),
	}
	if h := strings.TrimSpace(r.Header.Get("Authorization")); h != "" {
		if strings.HasPrefix(strings.ToLower(h), "bearer ") {
			candidates = append(candidates, strings.TrimSpace(h[7:]))
		} else {
			candidates = append(candidates, h)
		}
	}
	for _, got := range candidates {
		if got == expected {
			return true
		}
	}
	return false
}

func (s *Server) actionsOpenAPI(w http.ResponseWriter, r *http.Request) {
	origin := s.origin(r)
	yaml := fmt.Sprintf(`openapi: 3.1.0
info:
  title: GPTAdmin MCP Relay
  version: %s
  description: |
    Universal MCP relay for GPTAdmin.

    Use this API as a single interface for remote servers:
      1. listMcpServers — choose an online server.
      2. listMcpTools — inspect tools available on that server.
      3. callMcpTool — call exactly one tool on exactly one target.
      4. If background=true and job_id is returned, poll getMcpJob until status is completed or failed.

    Shell hosts and MCP services are exposed as GPTAdmin servers with ids like shell:<server_name>.
    The hub itself is exposed as target "hub" for registry and approval tools.
servers:
  - url: %s
security:
  - bearerAuth: []
paths:
  /mcp-relay/servers:
    get:
      operationId: listMcpServers
      summary: List MCP servers
      description: Lists real MCP servers, virtual shell servers, and the built-in hub server.
      responses:
        "200":
          description: Available MCP servers
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ListMcpServersResponse"
  /mcp-relay/tools:
    post:
      operationId: listMcpTools
      summary: List tools for one MCP server target
      description: Requests tools/list from an explicitly selected MCP server. Call listMcpServers first and pass one returned server_id as target. There is no default target; never use target="default".
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/ListMcpToolsRequest"
      responses:
        "200":
          description: Tool list response or background job reference
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/McpToolResponse"
  /mcp-relay/call:
    post:
      operationId: callMcpTool
      summary: Call one tool on one MCP server target
      description: Calls one tool on one selected target. Do not use this as bulk API; call it once per target when several servers must be used.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/CallMcpToolRequest"
      responses:
        "200":
          description: Tool call response or background job reference
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/McpToolResponse"
  /mcp-relay/job/{job_id}:
    get:
      operationId: getMcpJob
      summary: Get MCP background job status
      description: Polls a background MCP job. Set ack=true after reading a completed or failed result to remove it from hub memory.
      parameters:
        - name: job_id
          in: path
          required: true
          description: Job id returned by listMcpTools or callMcpTool.
          schema:
            type: string
        - name: ack
          in: query
          required: false
          description: Remove completed or failed job result after reading.
          schema:
            type: boolean
            default: false
      responses:
        "200":
          description: MCP job status and optional result
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/McpJobResponse"
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
  schemas:
    ListMcpServersResponse:
      type: object
      additionalProperties: false
      required: [servers]
      properties:
        servers:
          type: array
          items:
            $ref: "#/components/schemas/McpServer"
    McpServer:
      type: object
      additionalProperties: true
      required: [server_id, name, kind, status]
      properties:
        server_id:
          type: string
          description: Target id to use in listMcpTools and callMcpTool.
        name:
          type: string
        kind:
          type: string
          enum: [real_mcp, virtual_shell, virtual_hub, hub]
        transport:
          type: [string, "null"]
        status:
          type: string
          enum: [online, offline, stale]
        last_seen:
          type: [number, "null"]
        capabilities:
          type: array
          items:
            type: string
        meta:
          type: object
          additionalProperties: true
    ListMcpToolsRequest:
      type: object
      additionalProperties: false
      required: [target]
      properties:
        target:
          type: string
          description: Explicit server id from listMcpServers. There is no default target. Never use "default".
        timeout:
          type: [integer, "null"]
          minimum: 1
          maximum: 35
          default: 30
        background:
          type: boolean
          default: false
    CallMcpToolRequest:
      type: object
      additionalProperties: false
      required: [target, tool_name]
      properties:
        target:
          type: string
          description: Explicit server id from listMcpServers. There is no default target. Never use "default".
        tool_name:
          type: string
          description: Tool name returned by listMcpTools.
        arguments:
          type: object
          additionalProperties: true
          default: {}
        timeout:
          type: [integer, "null"]
          minimum: 1
          maximum: 35
          default: 30
        background:
          type: boolean
          default: false
    McpToolResponse:
      type: object
      additionalProperties: true
      required: [server_id, status]
      properties:
        server_id:
          type: string
        status:
          type: string
          enum: [completed, running, failed, running_or_unknown]
        response:
          type: [object, "null"]
          additionalProperties: true
        background:
          type: boolean
        job_id:
          type: [string, "null"]
        message:
          type: [string, "null"]
        error:
          oneOf:
            - $ref: "#/components/schemas/McpError"
            - type: string
            - type: "null"
    McpJobResponse:
      type: object
      additionalProperties: true
      required: [job_id, status]
      properties:
        job_id:
          type: string
        status:
          type: string
          enum: [queued, running, completed, failed, orphaned, running_or_unknown]
        server_id:
          type: [string, "null"]
        response:
          type: [object, "null"]
          additionalProperties: true
        error:
          oneOf:
            - $ref: "#/components/schemas/McpError"
            - type: string
            - type: "null"
        acked:
          type: boolean
          default: false
    McpError:
      type: object
      additionalProperties: true
      properties:
        message:
          type: [string, "null"]
        code:
          type: [string, integer, "null"]
`, BuildVersion, origin)
	b := []byte(yaml)
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
func (s *Server) shellmcpArtifactPath() string {
	return filepath.Join(s.cfg.ArtifactDir, "gptadmin-shellmcp.tar.gz")
}

func (s *Server) shellmcpArtifactManifest(w http.ResponseWriter, r *http.Request) {
	artifact := s.shellmcpArtifactPath()
	st, err := os.Stat(artifact)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "shellmcp artifact not found: " + artifact})
		return
	}
	sha, err := sha256File(artifact)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"component": "shellmcp", "build_version": BuildVersion, "git_commit": GitCommit, "sha256": sha, "size": st.Size(), "url": s.origin(r) + "/artifacts/shellmcp.tar.gz"})
}

func (s *Server) shellmcpArtifactDownload(w http.ResponseWriter, r *http.Request) {
	artifact := s.shellmcpArtifactPath()
	if _, err := os.Stat(artifact); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "shellmcp artifact not found: " + artifact})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="gptadmin-shellmcp.tar.gz"`)
	http.ServeFile(w, r, artifact)
}

func (s *Server) serversList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	s.mu.Lock()
	servers := []map[string]any{}
	for _, a := range s.agents {
		if strings.HasPrefix(a.AgentID, "shell:") {
			servers = append(servers, map[string]any{"name": strings.TrimPrefix(a.AgentID, "shell:"), "server_id": a.AgentID, "status": a.Status, "last_seen": a.LastSeen, "mode": a.Transport, "meta": a.Meta})
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers, "count": len(servers)})
}

func (s *Server) bulkExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	cmd := firstString(req, "cmd", "command")
	if cmd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing cmd"})
		return
	}
	targets := []string{}
	if arr, ok := req["servers"].([]any); ok {
		for _, item := range arr {
			if v, ok := item.(string); ok && v != "" {
				targets = append(targets, v)
			}
		}
	}
	if len(targets) == 0 {
		s.mu.Lock()
		for _, a := range s.agents {
			if strings.HasPrefix(a.AgentID, "shell:") && a.Status == "online" {
				targets = append(targets, strings.TrimPrefix(a.AgentID, "shell:"))
			}
		}
		s.mu.Unlock()
	}
	results := map[string]any{}
	for _, srv := range targets {
		results[srv] = s.callShellTool("shell:"+strings.TrimPrefix(srv, "shell:"), "shell_exec", map[string]any{"cmd": cmd, "cwd": firstString(req, "cwd"), "timeout": req["timeout"]}, true, s.cfg.DefaultTimeout)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results})
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var beat map[string]any
	if err := readJSON(r, &beat); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	name := firstString(beat, "name", "server_name", "host", "hostname")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing heartbeat name"})
		return
	}
	agentID := "shell:" + name
	now := nowFloat()
	meta := cloneMap(beat)
	delete(meta, "name")
	delete(meta, "server_name")
	mode := firstString(beat, "mode")
	transport := mode
	if transport == "" {
		transport = "webhook"
	}
	s.mu.Lock()
	s.agents[agentID] = &Agent{AgentID: agentID, Name: "Shell: " + name, Kind: "virtual_shell", Transport: transport, Status: "online", LastSeen: now, Capabilities: []string{"shell", "system", "tasks", "logs"}, Meta: meta}
	s.addAuditLocked("heartbeat", map[string]any{"agent_id": agentID, "transport": transport})
	if err := s.saveRegistryStateLocked(); err != nil {
		log.Printf("registry state save failed: %v", err)
	}
	if err := s.saveFailoverStateBundleLocked(); err != nil {
		log.Printf("failover state save failed: %v", err)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent_id": agentID, "status": "registered"})
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	trim := strings.TrimPrefix(r.URL.Path, "/queue/")
	parts := strings.Split(strings.Trim(trim, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "missing queue name"})
		return
	}
	name, _ := url.PathUnescape(parts[0])
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.pollShellQueue(w, r, name)
		return
	}
	if len(parts) == 2 && parts[1] == "result" && r.Method == http.MethodPost {
		s.shellQueueResult(w, r, name)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "not found"})
}

func (s *Server) pollShellQueue(w http.ResponseWriter, r *http.Request, name string) {
	timeout := queryDuration(r, "timeout", s.cfg.PollMaxTimeout)
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if q := s.shellQueues[name]; len(q) > 0 {
			id := q[0]
			s.shellQueues[name] = q[1:]
			job := s.shellJobs[id]
			if job == nil {
				continue
			}
			job.Status = "running"
			job.StartedAt = nowFloat()
			writeJSON(w, http.StatusOK, job)
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		waitCond(s.cond, minDuration(remaining, time.Second))
	}
}

func (s *Server) shellQueueResult(w http.ResponseWriter, r *http.Request, name string) {
	var res struct {
		ID     string `json:"id"`
		Result any    `json:"result"`
		Error  any    `json:"error"`
	}
	if err := readJSON(r, &res); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	s.mu.Lock()
	job := s.shellJobs[res.ID]
	if job == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown job"})
		return
	}
	job.DoneAt = nowFloat()
	job.Status = "completed"
	job.Result = res.Result
	if res.Error != nil {
		job.Status = "failed"
		job.Error = res.Error
	}
	s.addAuditLocked("shell_result", map[string]any{"server": name, "job_id": res.ID, "status": job.Status})
	s.cond.Broadcast()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) mcpRelayRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if !s.requireRelay(w, r) {
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	agentID := firstString(req, "agent_id", "id")
	if agentID == "" {
		agentID = firstString(req, "name")
	}
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing agent_id"})
		return
	}
	name := firstString(req, "name")
	if name == "" {
		name = agentID
	}
	kind := firstString(req, "kind")
	if kind == "" {
		kind = "real_mcp"
	}
	transport := firstString(req, "transport")
	if transport == "" {
		transport = "stdio"
	}
	caps := stringSlice(req["capabilities"])
	meta := mapValue(req["meta"])
	s.mu.Lock()
	s.agents[agentID] = &Agent{AgentID: agentID, Name: name, Kind: kind, Transport: transport, Status: "online", LastSeen: nowFloat(), Capabilities: caps, Meta: meta}
	s.addAuditLocked("mcp_register", map[string]any{"agent_id": agentID, "kind": kind, "transport": transport})
	if err := s.saveRegistryStateLocked(); err != nil {
		log.Printf("registry state save failed: %v", err)
	}
	if err := s.saveFailoverStateBundleLocked(); err != nil {
		log.Printf("failover state save failed: %v", err)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent_id": agentID, "status": "registered"})
}

func (s *Server) mcpRelayPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if !s.requireRelay(w, r) {
		return
	}
	agentID, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/mcp-relay/poll/"))
	agentID = strings.Trim(agentID, "/")
	if agentID == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "missing agent"})
		return
	}
	timeout := queryDuration(r, "timeout", s.cfg.PollMaxTimeout)
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if a := s.agents[agentID]; a != nil {
			a.Status = "online"
			a.LastSeen = nowFloat()
		}
		if q := s.relayQueues[agentID]; len(q) > 0 {
			id := q[0]
			s.relayQueues[agentID] = q[1:]
			job := s.relayJobs[id]
			if job == nil {
				continue
			}
			job.Status = "running"
			job.StartedAt = nowFloat()
			writeJSON(w, http.StatusOK, map[string]any{"id": job.ID, "method": job.Method, "params": job.Params})
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		waitCond(s.cond, minDuration(remaining, time.Second))
	}
}

func (s *Server) mcpRelayResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if !s.requireRelay(w, r) {
		return
	}
	agentID, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/mcp-relay/result/"))
	agentID = strings.Trim(agentID, "/")
	var res struct {
		ID     string         `json:"id"`
		OK     *bool          `json:"ok"`
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err := readJSON(r, &res); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	s.mu.Lock()
	job := s.relayJobs[res.ID]
	if job == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown job"})
		return
	}
	job.DoneAt = nowFloat()
	job.Result = res.Result
	if res.OK != nil && !*res.OK {
		job.Status = "failed"
		job.Error = res.Error
	} else {
		job.Status = "completed"
	}
	s.addAuditLocked("mcp_result", map[string]any{"server_id": agentID, "job_id": res.ID, "status": job.Status})
	s.cond.Broadcast()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) mcpRelayServers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	servers := s.publicServersLocked(r)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

// mcpRelayAgents is a deprecated compatibility alias for old clients.
func (s *Server) mcpRelayAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	agents := s.publicAgentsLocked(r)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) publicServersLocked(r *http.Request) []map[string]any {
	agents := s.publicAgentsLocked(r)
	servers := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		servers = append(servers, agentAsServer(a))
	}
	return servers
}

func agentAsServer(a Agent) map[string]any {
	return map[string]any{
		"server_id":    a.AgentID,
		"name":         a.Name,
		"kind":         a.Kind,
		"transport":    a.Transport,
		"status":       a.Status,
		"last_seen":    a.LastSeen,
		"capabilities": a.Capabilities,
		"meta":         a.Meta,
	}
}

func (s *Server) publicAgentsLocked(r *http.Request) []Agent {
	agents := make([]Agent, 0, len(s.agents)+1)
	hub := s.hubAgentLocked()
	agents = append(agents, s.withExposeMetaLocked(hub, r))
	for _, a := range s.agents {
		cp := *a
		agents = append(agents, s.withExposeMetaLocked(cp, r))
	}
	return agents
}

func (s *Server) withExposeMetaLocked(a Agent, r *http.Request) Agent {
	slug := agentSlug(a.AgentID)
	if slug == "" {
		slug = agentSlug(a.Name)
	}
	if a.Meta == nil {
		a.Meta = map[string]any{}
	} else {
		cp := make(map[string]any, len(a.Meta)+6)
		for k, v := range a.Meta {
			cp[k] = v
		}
		a.Meta = cp
	}
	path := "/server/" + slug + "/mcp"
	a.Meta["exposed_by_default"] = true
	a.Meta["public_mcp_slug"] = slug
	a.Meta["public_mcp_path"] = path
	if r != nil {
		a.Meta["public_mcp_endpoint"] = s.origin(r) + path
	}
	a.Meta["public_mcp_auth"] = map[string]any{"bearer": true, "oauth": true}
	return a
}

func (s *Server) hubAgentLocked() Agent {
	return Agent{AgentID: "hub", Name: "GPTAdmin Hub", Kind: "hub", Transport: "internal", Status: "online", LastSeen: nowFloat(), Capabilities: []string{"registry", "pending_servers", "mcp_relay"}, Meta: map[string]any{"server_count": len(s.agents)}}
}

func (s *Server) mcpRelayTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	_ = readJSON(r, &req)
	target := firstString(req, "target", "server_id", "agent_id")
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing target"})
		return
	}
	if target == "hub" {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": hubTools()}})
		return
	}
	if strings.HasPrefix(target, "shell:") {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": shellTools()}})
		return
	}
	jobID := s.enqueueRelay(target, "tools/list", nil)
	if truthy(req["background"]) {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": target, "status": "running", "background": true, "job_id": jobID})
		return
	}
	resp := s.waitRelay(jobID, timeoutFromReq(req, s.cfg.DefaultTimeout))
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) mcpRelayCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	target := firstString(req, "target", "server_id", "agent_id")
	toolName := firstString(req, "tool_name", "name")
	args := mapValue(req["arguments"])
	if len(args) == 0 {
		args = mapValue(req["args"])
	}
	if target == "" || toolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing target or tool_name"})
		return
	}
	if target == "hub" {
		resp, status := s.callHubTool(toolName, args)
		writeJSON(w, status, map[string]any{"server_id": target, "status": "completed", "response": resp})
		return
	}
	if strings.HasPrefix(target, "shell:") {
		resp := s.callShellTool(target, toolName, args, truthy(req["background"]), timeoutFromReq(req, s.cfg.DefaultTimeout))
		writeJSON(w, http.StatusOK, resp)
		return
	}
	params := map[string]any{"name": toolName, "arguments": args}
	jobID := s.enqueueRelay(target, "tools/call", params)
	if truthy(req["background"]) {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": target, "status": "running", "background": true, "job_id": jobID})
		return
	}
	resp := s.waitRelay(jobID, timeoutFromReq(req, s.cfg.DefaultTimeout))
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) mcpRelayJob(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimPrefix(r.URL.Path, "/mcp-relay/get_mcp_job/")
	jobID = strings.TrimPrefix(jobID, "/mcp-relay/job/")
	jobID, _ = url.PathUnescape(strings.Trim(jobID, "/"))
	if jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing job_id"})
		return
	}
	ack := r.URL.Query().Get("ack") == "true" || r.URL.Query().Get("ack") == "1"
	s.mu.Lock()
	if j := s.relayJobs[jobID]; j != nil {
		resp := relayJobResponse(j)
		if ack && (j.Status == "completed" || j.Status == "failed") {
			delete(s.relayJobs, jobID)
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if j := s.shellJobs[jobID]; j != nil {
		resp := shellJobResponse(j)
		if ack && (j.Status == "completed" || j.Status == "failed") {
			delete(s.shellJobs, jobID)
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown job"})
}

func (s *Server) enqueueRelay(agentID, method string, params map[string]any) string {
	id := newID()
	s.mu.Lock()
	s.relayJobs[id] = &relayJob{ID: id, AgentID: agentID, Method: method, Params: params, CreatedAt: nowFloat(), Status: "queued"}
	s.relayQueues[agentID] = append(s.relayQueues[agentID], id)
	s.addAuditLocked("mcp_enqueue", map[string]any{"server_id": agentID, "job_id": id, "method": method})
	s.cond.Broadcast()
	s.mu.Unlock()
	return id
}

func (s *Server) waitRelay(jobID string, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		job := s.relayJobs[jobID]
		if job == nil {
			return map[string]any{"status": "failed", "error": "unknown job", "job_id": jobID}
		}
		if job.Status == "completed" || job.Status == "failed" {
			return relayJobResponse(job)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return map[string]any{"server_id": job.AgentID, "status": "running", "background": true, "job_id": jobID, "message": "MCP relay job is still running"}
		}
		waitCond(s.cond, minDuration(remaining, 500*time.Millisecond))
	}
}

func relayJobResponse(job *relayJob) map[string]any {
	if job.Status == "failed" {
		return map[string]any{"server_id": job.AgentID, "status": "failed", "job_id": job.ID, "error": job.Error}
	}
	return map[string]any{"server_id": job.AgentID, "status": job.Status, "job_id": job.ID, "response": spillFriendly(job.Result)}
}

func shellJobResponse(job *shellJob) map[string]any {
	out := map[string]any{"server_id": "shell:" + job.Server, "status": job.Status, "job_id": job.ID, "task_id": job.ID}
	if job.Result != nil {
		out["response"] = map[string]any{"content": []map[string]any{{"type": "text", "text": "shell_exec completed on " + job.Server}}, "structuredContent": map[string]any{"server": job.Server, "result": job.Result}}
	}
	if job.Error != nil {
		out["error"] = job.Error
	}
	return out
}

func (s *Server) callHubTool(name string, args map[string]any) (map[string]any, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addAuditLocked("hub_tool", map[string]any{"tool": name})
	switch name {
	case "listMcpServers", "list_mcp_servers":
		servers := s.publicServersLocked(nil)
		return map[string]any{"servers": servers}, http.StatusOK
	case "listMcpAgents", "list_mcp_agents":
		agents := s.publicAgentsLocked(nil)
		return map[string]any{"agents": agents}, http.StatusOK
	case "list_pending_servers":
		return map[string]any{"pending": []any{}, "count": 0}, http.StatusOK
	case "hub_status", "status":
		return map[string]any{"ok": true, "servers": len(s.agents), "relay_jobs": len(s.relayJobs), "shell_jobs": len(s.shellJobs)}, http.StatusOK
	default:
		return map[string]any{"error": "unsupported hub tool", "tool": name, "arguments": args}, http.StatusBadRequest
	}
}

func (s *Server) callShellTool(target, toolName string, args map[string]any, background bool, timeout time.Duration) map[string]any {
	server := strings.TrimPrefix(target, "shell:")
	if toolName != "shell_exec" {
		return map[string]any{"server_id": target, "status": "failed", "error": "unsupported shell tool: " + toolName}
	}
	cmd := firstString(args, "cmd", "command")
	if cmd == "" {
		return map[string]any{"server_id": target, "status": "failed", "error": "missing cmd"}
	}
	job := &shellJob{ID: newID(), Server: server, Cmd: cmd, Cwd: firstString(args, "cwd"), Timeout: intFromAny(args["timeout"]), Env: mapValue(args["env"]), CreatedAt: nowFloat(), Status: "queued"}
	s.mu.Lock()
	s.shellJobs[job.ID] = job
	s.shellQueues[server] = append(s.shellQueues[server], job.ID)
	s.addAuditLocked("shell_enqueue", map[string]any{"server": server, "job_id": job.ID})
	s.cond.Broadcast()
	s.mu.Unlock()
	if background {
		return map[string]any{"server_id": target, "status": "running", "background": true, "job_id": job.ID, "task_id": job.ID, "message": "shell job queued"}
	}
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		j := s.shellJobs[job.ID]
		if j.Status == "completed" || j.Status == "failed" {
			return shellJobResponse(j)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return map[string]any{"server_id": target, "status": "running", "background": true, "job_id": job.ID, "task_id": job.ID, "message": "shell job is still running"}
		}
		waitCond(s.cond, minDuration(remaining, 500*time.Millisecond))
	}
}

func hubTools() []map[string]any {
	return []map[string]any{
		{"name": "list_mcp_servers", "description": "List registered GPTAdmin servers, including the internal hub", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "list_pending_servers", "description": "List pending shell server approvals", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "hub_status", "description": "Return Go hub runtime status", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	}
}

func shellTools() []map[string]any {
	return []map[string]any{{"name": "shell_exec", "description": "Execute a shell command through a polling shellmcp agent", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}, "cwd": map[string]any{"type": []string{"string", "null"}}, "timeout": map[string]any{"type": []string{"integer", "null"}}}, "required": []string{"cmd"}}}}
}

func (s *Server) tasksEndpoint(w http.ResponseWriter, r *http.Request) {
	trim := strings.TrimPrefix(r.URL.Path, "/tasks/")
	parts := strings.Split(strings.Trim(trim, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "missing server"})
		return
	}
	srv, _ := url.PathUnescape(parts[0])
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.mu.Lock()
		items := []map[string]any{}
		for _, j := range s.shellJobs {
			if j.Server == srv {
				items = append(items, map[string]any{"task_id": j.ID, "job_id": j.ID, "server": j.Server, "cmd": j.Cmd, "status": j.Status, "result": j.Result, "error": j.Error, "created_at": j.CreatedAt, "started_at": j.StartedAt, "completed_at": j.DoneAt})
			}
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"tasks": items, "count": len(items)})
		return
	}
	if len(parts) >= 2 {
		tid, _ := url.PathUnescape(parts[1])
		if len(parts) == 2 && r.Method == http.MethodGet {
			s.mu.Lock()
			j := s.shellJobs[tid]
			if j == nil || j.Server != srv {
				s.mu.Unlock()
				writeJSON(w, http.StatusNotFound, map[string]any{"detail": "task not found"})
				return
			}
			resp := shellJobResponse(j)
			if r.URL.Query().Get("ack") == "1" || r.URL.Query().Get("ack") == "true" {
				delete(s.shellJobs, tid)
			}
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, resp)
			return
		}
		if len(parts) == 3 && parts[2] == "ack" && r.Method == http.MethodPost {
			s.mu.Lock()
			_, existed := s.shellJobs[tid]
			delete(s.shellJobs, tid)
			s.mu.Unlock()
			status := "not_found"
			if existed {
				status = "acknowledged"
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status, "server": srv, "task_id": tid})
			return
		}
		if len(parts) == 3 && parts[2] == "edit" && r.Method == http.MethodPost {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "unsupported_in_go_hub_yet", "server": srv, "task_id": tid})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "not found"})
}

func (s *Server) adminMCPManage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	_ = readJSON(r, &req)
	action := firstString(req, "action")
	if action == "" {
		action = "list"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": firstString(req, "target"), "action": action, "response": map[string]any{"note": "go hub MCP manage is read-only/placeholder; use shell:mcp_tools for mutation until full parity", "servers": len(s.agents)}})
}

func (s *Server) adminClientsRevokeAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked_count": 0, "oauth_secret_rotated": false, "note": "go hub keeps OAuth codes/tokens in memory"})
}

func (s *Server) adminClientDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": false})
}

func (s *Server) adminMCPResourcesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	target := firstString(req, "target", "server_id", "agent_id")
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing target"})
		return
	}
	jobID := s.enqueueRelay(target, "resources/list", map[string]any{})
	if truthy(req["background"]) {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": target, "status": "running", "background": true, "job_id": jobID})
		return
	}
	writeJSON(w, http.StatusOK, s.waitRelay(jobID, timeoutFromReq(req, s.cfg.DefaultTimeout)))
}

func (s *Server) adminMCPResourceRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	target := firstString(req, "target", "server_id", "agent_id")
	uri := firstString(req, "uri")
	if target == "" || uri == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing target or uri"})
		return
	}
	jobID := s.enqueueRelay(target, "resources/read", map[string]any{"uri": uri})
	if truthy(req["background"]) {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": target, "status": "running", "background": true, "job_id": jobID})
		return
	}
	writeJSON(w, http.StatusOK, s.waitRelay(jobID, timeoutFromReq(req, s.cfg.DefaultTimeout)))
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	servers := s.publicServersLocked(r)
	jobs := s.adminJobsDataLocked()
	audit := append([]auditEvent(nil), s.audit...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "build": map[string]any{"name": "gptadmin-go-hub", "build_version": BuildVersion, "git_commit": GitCommit}, "now": time.Now().Unix(), "now_fmt": time.Now().Format("2006-01-02 15:04:05 MST"), "servers": servers, "server_counts": serverStatusCounts(servers), "clients": []any{}, "client_count": 0, "clients_with_multiple_ips": []any{}, "jobs": jobs, "audit": audit, "state_files": map[string]any{"mode": "go-persistent", "registry_state": s.registryStatePath(), "failover_config": s.failoverConfigPath(), "failover_state": s.failoverStatePath()}, "failover_config": s.failover})
}

func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	jobs := s.adminJobsDataLocked()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	items := append([]auditEvent(nil), s.audit...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"events": items, "audit_log": "go-in-memory"})
}

func (s *Server) adminClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"clients": []any{}, "client_count": 0})
}

func (s *Server) adminJobsDataLocked() map[string]any {
	items := make([]map[string]any, 0, len(s.relayJobs)+len(s.shellJobs))
	for _, j := range s.relayJobs {
		items = append(items, map[string]any{"job_id": j.ID, "server_id": j.AgentID, "kind": "mcp_relay", "method": j.Method, "status": j.Status, "created_at": j.CreatedAt, "started_at": j.StartedAt, "completed_at": j.DoneAt})
	}
	for _, j := range s.shellJobs {
		items = append(items, map[string]any{"job_id": j.ID, "task_id": j.ID, "server": j.Server, "server_id": "shell:" + j.Server, "kind": "shell", "command": j.Cmd, "status": j.Status, "created_at": j.CreatedAt, "started_at": j.StartedAt, "completed_at": j.DoneAt})
	}
	queued := []map[string]any{}
	background := []map[string]any{}
	counts := map[string]int{}
	for _, item := range items {
		st, _ := item["status"].(string)
		counts[st]++
		if st == "queued" || st == "queued_offline" {
			queued = append(queued, item)
		}
		if st == "running" || st == "dispatching" {
			background = append(background, item)
		}
	}
	return map[string]any{"count": len(items), "status_counts": counts, "queued": queued, "background": background, "recent": items}
}

func serverStatusCounts(servers []map[string]any) map[string]int {
	counts := map[string]int{"online": 0, "offline": 0, "stale": 0, "pending": 0}
	for _, srv := range servers {
		if st, _ := srv["status"].(string); st != "" {
			counts[st]++
		}
	}
	return counts
}

func (s *Server) adminIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/admin/index.html", http.StatusFound)
}

func (s *Server) adminStatic(w http.ResponseWriter, r *http.Request) {
	root := filepath.Join(s.cfg.PublicDir, "admin")
	fs := http.StripPrefix("/admin/", http.FileServer(http.Dir(root)))
	fs.ServeHTTP(w, r)
}

func (s *Server) addAuditLocked(name string, fields map[string]any) {
	s.audit = append(s.audit, auditEvent{Time: time.Now().Format(time.RFC3339), Name: name, Fields: fields})
	if len(s.audit) > 500 {
		s.audit = s.audit[len(s.audit)-500:]
	}
}

func readJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(http.MaxBytesReader(nilWriter{}, r.Body, 64<<20))
	if err != nil {
		return err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return errors.New("empty JSON body")
	}
	return json.Unmarshal(body, dst)
}

type nilWriter struct{}

func (nilWriter) Header() http.Header         { return http.Header{} }
func (nilWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nilWriter) WriteHeader(statusCode int)  {}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		b = []byte(`{"error":"json encode failed"}`)
	}
	b = append(b, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func mcpToolResult(payload any) map[string]any {
	b, err := json.Marshal(payload)
	if err != nil {
		b = []byte(`{"error":"json encode failed"}`)
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(b)}},
		"structuredContent": payload,
	}
}
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case string:
				if strings.TrimSpace(x) != "" {
					return strings.TrimSpace(x)
				}
			case fmt.Stringer:
				return x.String()
			}
		}
	}
	return ""
}

func mapValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func stringSlice(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func queryDuration(r *http.Request, name string, def time.Duration) time.Duration {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v <= 0 {
		return def
	}
	return time.Duration(v) * time.Second
}

func timeoutFromReq(req map[string]any, def time.Duration) time.Duration {
	if v := intFromAny(req["timeout"]); v > 0 {
		return time.Duration(v) * time.Second
	}
	return def
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		x = strings.ToLower(strings.TrimSpace(x))
		return x == "1" || x == "true" || x == "yes" || x == "on"
	case float64:
		return x != 0
	default:
		return false
	}
}

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func waitCond(c *sync.Cond, d time.Duration) {
	t := time.AfterFunc(d, func() {
		c.L.Lock()
		c.Broadcast()
		c.L.Unlock()
	})
	c.Wait()
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func spillFriendly(v any) any {
	// Keep Go hub responses compatible with the Python hub contract.  Actual
	// filesystem spilling can be added here without changing external JSON.
	return v
}

func (s *Server) origin(r *http.Request) string {
	if s.cfg.PublicOrigin != "" {
		return s.cfg.PublicOrigin
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xf != "" {
		host = xf
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return scheme + "://" + strings.TrimRight(host, "/")
}

func (s *Server) resource(r *http.Request) string {
	if s.cfg.MCPResource != "" {
		return s.cfg.MCPResource
	}
	return s.origin(r)
}

func (s *Server) oauthProtectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":               s.resource(r),
		"authorization_servers":  []string{s.origin(r)},
		"scopes_supported":       []string{"gptadmin.read", "gptadmin.exec"},
		"resource_documentation": s.origin(r) + "/",
	})
}

func (s *Server) oauthAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	origin := s.origin(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                origin,
		"authorization_endpoint":                origin + "/authorize",
		"token_endpoint":                        origin + "/token",
		"registration_endpoint":                 origin + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{"gptadmin.read", "gptadmin.exec"},
	})
}

func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	_ = readJSON(r, &req)
	clientID := "gptadmin-" + newID()
	clientSecret := newID()
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_secret":              clientSecret,
		"client_id_issued_at":        time.Now().Unix(),
		"client_secret_expires_at":   0,
		"redirect_uris":              req["redirect_uris"],
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"code_challenge_methods":     []string{"S256"},
		"scope":                      "gptadmin.read gptadmin.exec",
	})
}

func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.oauthAuthorizeGet(w, r)
	case http.MethodPost:
		s.oauthAuthorizePost(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (s *Server) oauthAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	resource := strings.TrimRight(q.Get("resource"), "/")
	if resource == "" {
		resource = s.resource(r)
	}
	if !s.allowedRedirect(redirectURI) || !s.allowedResource(resource, r) {
		s.authAudit("oauth_authorize_denied", r, map[string]any{"reason": "invalid redirect_uri or resource", "redirect_uri": redirectURI, "resource": resource, "form": s.formForAudit(r)})
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": "invalid redirect_uri or resource"})
		return
	}
	hidden := ""
	for _, k := range []string{"client_id", "redirect_uri", "state", "scope", "code_challenge", "code_challenge_method", "resource"} {
		v := q.Get(k)
		if k == "resource" && v == "" {
			v = resource
		}
		hidden += `<input type="hidden" name="` + html.EscapeString(k) + `" value="` + html.EscapeString(v) + `">` + "\n"
	}
	scope := q.Get("scope")
	if scope == "" {
		scope = "gptadmin.read gptadmin.exec"
	}
	page := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize GPTAdmin MCP</title><style>body{font-family:system-ui,sans-serif;background:#070a12;color:#e5eefc;display:grid;place-items:center;min-height:100vh;margin:0}.card{max-width:560px;padding:28px;border:1px solid #1e293b;border-radius:24px;background:#0f1623}input,button{width:100%;box-sizing:border-box;padding:14px;border-radius:14px;margin-top:10px}input{background:#111827;color:#fff;border:1px solid #334155}button{border:0;background:linear-gradient(135deg,#7c3aed,#06b6d4);color:#fff;font-weight:800}.muted{color:#94a3b8;word-break:break-all}</style></head><body><main class="card"><h1>Authorize GPTAdmin MCP</h1><p class="muted">Client: ` + html.EscapeString(q.Get("client_id")) + `</p><p class="muted">Resource: ` + html.EscapeString(resource) + `</p><p>Scopes: ` + html.EscapeString(scope) + `</p><form method="POST" action="/authorize">` + hidden + `<label>Admin password</label><input type="password" name="password" autofocus required autocomplete="current-password"><button type="submit">Authorize</button></form></main></body></html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

func (s *Server) oauthAuthorizePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	if !s.adminPasswordOK(r.Form.Get("password")) {
		s.authAudit("oauth_authorize_denied", r, map[string]any{"reason": "invalid password", "form": s.formForAudit(r)})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "access_denied", "error_description": "invalid password"})
		return
	}
	redirectURI := r.Form.Get("redirect_uri")
	resource := strings.TrimRight(r.Form.Get("resource"), "/")
	if resource == "" {
		resource = s.resource(r)
	}
	if !s.allowedRedirect(redirectURI) || !s.allowedResource(resource, r) {
		s.authAudit("oauth_authorize_denied", r, map[string]any{"reason": "invalid redirect_uri or resource", "redirect_uri": redirectURI, "resource": resource, "form": s.formForAudit(r)})
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": "invalid redirect_uri or resource"})
		return
	}
	code := newID()
	scope := r.Form.Get("scope")
	if scope == "" {
		scope = "gptadmin.read gptadmin.exec"
	}
	s.mu.Lock()
	s.oauthCodes[code] = oauthCode{Created: time.Now(), Challenge: r.Form.Get("code_challenge"), ClientID: r.Form.Get("client_id"), RedirectURI: redirectURI, Resource: resource, Scope: scope, State: r.Form.Get("state")}
	s.addAuditLocked("oauth_code_issued", map[string]any{"client_id": r.Form.Get("client_id"), "resource": resource})
	s.mu.Unlock()
	s.authAudit("oauth_authorize_ok", r, map[string]any{"client_id": r.Form.Get("client_id"), "redirect_uri": redirectURI, "resource": resource, "scope": scope, "code": s.secretForAudit(code), "form": s.formForAudit(r)})
	loc := redirectURI
	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}
	loc += sep + url.Values{"code": {code}, "state": {r.Form.Get("state")}}.Encode()
	http.Redirect(w, r, loc, http.StatusFound)
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	code := r.Form.Get("code")
	s.mu.Lock()
	data, ok := s.oauthCodes[code]
	delete(s.oauthCodes, code)
	s.mu.Unlock()
	resource := strings.TrimRight(r.Form.Get("resource"), "/")
	if resource == "" {
		resource = data.Resource
	}
	if !ok || time.Since(data.Created) > 5*time.Minute || !s.allowedResource(resource, r) || strings.TrimRight(data.Resource, "/") != resource {
		s.authAudit("oauth_token_denied", r, map[string]any{"reason": "code not found, expired, or resource mismatch", "resource": resource, "stored_resource": data.Resource, "code_found": ok, "form": s.formForAudit(r)})
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "code not found, expired, or resource mismatch"})
		return
	}
	if data.Challenge != "" && !pkceOK(r.Form.Get("code_verifier"), data.Challenge) {
		s.authAudit("oauth_token_denied", r, map[string]any{"reason": "PKCE verification failed", "client_id": data.ClientID, "resource": resource, "form": s.formForAudit(r)})
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "PKCE verification failed"})
		return
	}
	token, err := s.signJWT(map[string]any{"sub": "admin", "scope": data.Scope, "client_id": data.ClientID, "iss": s.origin(r), "aud": resource, "resource": resource, "exp": time.Now().Add(12 * time.Hour).Unix(), "iat": time.Now().Unix()})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.addAuditLocked("oauth_token_issued", map[string]any{"client_id": data.ClientID, "scope": data.Scope, "resource": resource})
	s.mu.Unlock()
	s.authAudit("oauth_token_ok", r, map[string]any{"client_id": data.ClientID, "scope": data.Scope, "resource": resource, "access_token": s.secretForAudit(token), "jwt_claims": decodeJWTClaimsUnverified(token), "form": s.formForAudit(r)})
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 43200})
}

func agentSlug(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range v {
		if r >= 'A' && r <= 'Z' {
			r = r + ('a' - 'A')
		}
		isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlnum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func compactSlug(v string) string {
	return strings.ReplaceAll(agentSlug(v), "-", "")
}

func (s *Server) resolveExposedAgent(slug string) (Agent, bool) {
	slug, _ = url.PathUnescape(strings.Trim(slug, "/"))
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return Agent{}, false
	}
	want := agentSlug(slug)
	wantCompact := compactSlug(slug)
	s.mu.Lock()
	defer s.mu.Unlock()
	hub := s.hubAgentLocked()
	for _, a := range append([]Agent{hub}, s.agentCopiesLocked()...) {
		aliases := []string{a.AgentID, a.Name, agentSlug(a.AgentID), agentSlug(a.Name), compactSlug(a.AgentID), compactSlug(a.Name)}
		for _, alias := range aliases {
			if strings.EqualFold(slug, alias) || want == agentSlug(alias) || wantCompact == compactSlug(alias) {
				return a, true
			}
		}
	}
	return Agent{}, false
}

func (s *Server) agentCopiesLocked() []Agent {
	agents := make([]Agent, 0, len(s.agents))
	for _, a := range s.agents {
		if a == nil {
			continue
		}
		cp := *a
		agents = append(agents, cp)
	}
	return agents
}

func parseAgentPath(p string) (slug, tail string, ok bool) {
	rest := strings.TrimPrefix(strings.TrimPrefix(p, "/server/"), "/agent/")
	if rest == p || rest == "" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", "", false
	}
	tail = ""
	if len(parts) > 1 {
		tail = strings.Join(parts[1:], "/")
	}
	return parts[0], tail, true
}

func (s *Server) serverMCPEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	slug, tail, ok := parseAgentPath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "missing agent slug"})
		return
	}
	agent, ok := s.resolveExposedAgent(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown exposed MCP agent", "slug": slug})
		return
	}
	if tail != "" && tail != "mcp" && tail != "card" && tail != "health" {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown agent endpoint", "endpoint": tail})
		return
	}
	if !s.mcpAuth(w, r) {
		return
	}
	if tail == "card" {
		writeJSON(w, http.StatusOK, s.agentCard(r, agent))
		return
	}
	if tail == "health" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": agent.Status == "online" || agent.AgentID == "hub", "server_id": agent.AgentID, "status": agent.Status})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.agentCard(r, agent))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": err.Error()}})
		return
	}
	result, rpcErr, noContent := s.agentMCPJSONRPC(r, agent, body)
	if noContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": body["id"]}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	writeJSON(w, http.StatusOK, resp)
}

// agentMCPEndpoint is a deprecated compatibility alias for old pinned MCP URLs.
func (s *Server) agentMCPEndpoint(w http.ResponseWriter, r *http.Request) {
	s.serverMCPEndpoint(w, r)
}

func (s *Server) agentCard(r *http.Request, agent Agent) map[string]any {
	slug := agentSlug(agent.AgentID)
	path := "/server/" + slug + "/mcp"
	return map[string]any{
		"ok":                  true,
		"server_id":           agent.AgentID,
		"name":                agent.Name,
		"kind":                agent.Kind,
		"transport":           agent.Transport,
		"status":              agent.Status,
		"slug":                slug,
		"mcp_path":            path,
		"mcp_endpoint":        s.origin(r) + path,
		"auth":                map[string]any{"bearer": true, "oauth": true},
		"tools_endpoint":      path,
		"drop_in_replacement": true,
	}
}

func (s *Server) agentMCPJSONRPC(r *http.Request, agent Agent, body map[string]any) (any, any, bool) {
	method := firstString(body, "method")
	params := mapValue(body["params"])
	switch method {
	case "initialize":
		return map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}, "resources": map[string]any{}, "prompts": map[string]any{}}, "serverInfo": map[string]any{"name": "gptadmin-server-" + agentSlug(agent.AgentID), "version": BuildVersion}}, nil, false
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil, true
	case "tools/list":
		result, err := s.agentToolsList(agent)
		return result, err, false
	case "tools/call":
		name := firstString(params, "name")
		args := mapValue(params["arguments"])
		if name == "" {
			return nil, map[string]any{"code": -32602, "message": "tool name is required"}, false
		}
		result, err := s.agentToolCall(agent, name, args)
		return result, err, false
	case "resources/list":
		result, err := s.agentResourcesList(r, agent)
		return result, err, false
	case "resources/read":
		uri := firstString(params, "uri")
		if uri == "" {
			return nil, map[string]any{"code": -32602, "message": "resource uri is required"}, false
		}
		result, err := s.agentResourceRead(r, agent, uri)
		return result, err, false
	case "prompts/list":
		result, err := s.agentPromptsList(agent)
		return result, err, false
	case "prompts/get":
		result, err := s.agentPromptGet(agent, params)
		return result, err, false
	default:
		return nil, map[string]any{"code": -32601, "message": "method not found"}, false
	}
}

func (s *Server) agentToolsList(agent Agent) (any, any) {
	if agent.AgentID == "hub" {
		return map[string]any{"tools": appsSDKTools()}, nil
	}
	if strings.HasPrefix(agent.AgentID, "shell:") {
		return map[string]any{"tools": shellTools()}, nil
	}
	jobID := s.enqueueRelay(agent.AgentID, "tools/list", map[string]any{})
	return unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
}

func (s *Server) agentToolCall(agent Agent, name string, args map[string]any) (any, any) {
	if agent.AgentID == "hub" {
		return mcpToolResult(s.appsSDKCall(name, args)), nil
	}
	if strings.HasPrefix(agent.AgentID, "shell:") {
		return unwrapMCPUpstream(s.callShellTool(agent.AgentID, name, args, false, s.cfg.DefaultTimeout))
	}
	jobID := s.enqueueRelay(agent.AgentID, "tools/call", map[string]any{"name": name, "arguments": args})
	return unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
}

func (s *Server) agentResourcesList(r *http.Request, agent Agent) (any, any) {
	if agent.AgentID == "hub" {
		return appsSDKResourcesList(), nil
	}
	if strings.HasPrefix(agent.AgentID, "shell:") || !hasCapability(agent, "resources/list") {
		return map[string]any{"resources": []map[string]any{{"uri": "gptadmin://server/" + agentSlug(agent.AgentID), "name": agent.Name + " card", "mimeType": "application/json"}}}, nil
	}
	jobID := s.enqueueRelay(agent.AgentID, "resources/list", map[string]any{})
	return unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
}

func (s *Server) agentResourceRead(r *http.Request, agent Agent, uri string) (any, any) {
	if agent.AgentID == "hub" {
		return s.appsSDKResourceRead(r, uri), nil
	}
	if strings.HasPrefix(agent.AgentID, "shell:") || strings.HasPrefix(uri, "gptadmin://server/") || strings.HasPrefix(uri, "gptadmin://agent/") || !hasCapability(agent, "resources/read") {
		b, _ := json.Marshal(s.agentCard(r, agent))
		return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(b)}}}, nil
	}
	jobID := s.enqueueRelay(agent.AgentID, "resources/read", map[string]any{"uri": uri})
	return unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
}

func (s *Server) agentPromptsList(agent Agent) (any, any) {
	if agent.AgentID == "hub" || strings.HasPrefix(agent.AgentID, "shell:") || !hasCapability(agent, "prompts/list") {
		return map[string]any{"prompts": []any{}}, nil
	}
	jobID := s.enqueueRelay(agent.AgentID, "prompts/list", map[string]any{})
	return unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
}

func (s *Server) agentPromptGet(agent Agent, params map[string]any) (any, any) {
	if agent.AgentID == "hub" || strings.HasPrefix(agent.AgentID, "shell:") || !hasCapability(agent, "prompts/get") {
		return nil, map[string]any{"code": -32601, "message": "prompts/get is not supported by this agent"}
	}
	jobID := s.enqueueRelay(agent.AgentID, "prompts/get", params)
	return unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
}

func hasCapability(agent Agent, cap string) bool {
	for _, item := range agent.Capabilities {
		if item == cap {
			return true
		}
	}
	return false
}

func unwrapMCPUpstream(resp map[string]any) (any, any) {
	status := firstString(resp, "status")
	if status == "failed" {
		return nil, map[string]any{"code": -32000, "message": "upstream MCP call failed", "data": resp}
	}
	if status == "running" || truthy(resp["background"]) {
		return nil, map[string]any{"code": -32001, "message": "upstream MCP call is still running", "data": resp}
	}
	if v, ok := resp["response"]; ok {
		return v, nil
	}
	return resp, nil
}

func (s *Server) mcpEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.mcpAuth(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": "GPTAdmin MCP", "tools": appsSDKTools()})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": err.Error()}})
		return
	}
	id := body["id"]
	method := firstString(body, "method")
	params := mapValue(body["params"])
	var result any
	var rpcErr any
	switch method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}, "resources": map[string]any{}}, "serverInfo": map[string]any{"name": "gptadmin-go-hub", "version": BuildVersion}}
	case "notifications/initialized":
		w.WriteHeader(http.StatusNoContent)
		return
	case "tools/list":
		result = map[string]any{"tools": appsSDKTools()}
	case "tools/call":
		name := firstString(params, "name")
		args := mapValue(params["arguments"])
		if name == "" {
			rpcErr = map[string]any{"code": -32602, "message": "tool name is required"}
		} else {
			result = mcpToolResult(s.appsSDKCall(name, args))
		}
	case "resources/list":
		result = appsSDKResourcesList()
	case "resources/read":
		uri := firstString(params, "uri")
		if uri == "" {
			rpcErr = map[string]any{"code": -32602, "message": "resource uri is required"}
		} else {
			result = s.appsSDKResourceRead(r, uri)
		}
	default:
		rpcErr = map[string]any{"code": -32601, "message": "method not found"}
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) mcpPrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.cfg.BridgeKey != "" && r.URL.Query().Get("key") != s.cfg.BridgeKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	target := r.URL.Query().Get("target")
	if target == "" || target == "all" {
		s.mu.Lock()
		servers := s.publicServersLocked(nil)
		s.mu.Unlock()
		var b strings.Builder
		b.WriteString("You have GPTAdmin MCP tools. Use JSON target/tool/args.\nAvailable servers:\n")
		for _, srv := range servers {
			b.WriteString("  " + fmt.Sprint(srv["server_id"]) + " (" + fmt.Sprint(srv["kind"]) + ")\n")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Tools for " + target + " are available through /mcp-relay/list_mcp_tools or /mcp JSON-RPC tools/list.\n"))
}

func (s *Server) mcpPromptCall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.cfg.BridgeKey != "" && r.URL.Query().Get("key") != s.cfg.BridgeKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	tool := firstString(req, "tool", "tool_name", "name")
	args := mapValue(req["args"])
	if len(args) == 0 {
		args = mapValue(req["arguments"])
	}
	if tool == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tool is required"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "result": s.appsSDKCall(tool, args)})
}

func (s *Server) appsSDKCall(name string, args map[string]any) any {
	switch name {
	case "list_mcp_servers", "listMcpServers":
		s.mu.Lock()
		servers := s.publicServersLocked(nil)
		s.mu.Unlock()
		return map[string]any{"servers": servers}
	case "list_mcp_agents", "listMcpAgents":
		s.mu.Lock()
		agents := s.publicAgentsLocked(nil)
		s.mu.Unlock()
		return map[string]any{"agents": agents}
	case "list_mcp_tools", "listMcpTools":
		target := firstString(args, "target", "server_id", "agent_id")
		if target == "hub" {
			return map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": hubTools()}}
		}
		if strings.HasPrefix(target, "shell:") {
			return map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": shellTools()}}
		}
		jobID := s.enqueueRelay(target, "tools/list", map[string]any{})
		return s.waitRelay(jobID, s.cfg.DefaultTimeout)
	case "call_mcp_tool", "callMcpTool":
		target := firstString(args, "target", "server_id", "agent_id")
		toolName := firstString(args, "tool_name", "name")
		callArgs := mapValue(args["arguments"])
		if target == "hub" {
			resp, _ := s.callHubTool(toolName, callArgs)
			return map[string]any{"server_id": target, "status": "completed", "response": resp}
		}
		if strings.HasPrefix(target, "shell:") {
			return s.callShellTool(target, toolName, callArgs, truthy(args["background"]), s.cfg.DefaultTimeout)
		}
		jobID := s.enqueueRelay(target, "tools/call", map[string]any{"name": toolName, "arguments": callArgs})
		if truthy(args["background"]) {
			return map[string]any{"server_id": target, "status": "running", "background": true, "job_id": jobID}
		}
		return s.waitRelay(jobID, s.cfg.DefaultTimeout)
	case "get_mcp_job", "getMcpJob":
		jobID := firstString(args, "job_id")
		s.mu.Lock()
		if j := s.relayJobs[jobID]; j != nil {
			resp := relayJobResponse(j)
			s.mu.Unlock()
			return resp
		}
		if j := s.shellJobs[jobID]; j != nil {
			resp := shellJobResponse(j)
			s.mu.Unlock()
			return resp
		}
		s.mu.Unlock()
		return map[string]any{"status": "failed", "error": "unknown job", "job_id": jobID}
	default:
		return map[string]any{"error": "unknown tool", "tool": name}
	}
}

func appsSDKTools() []map[string]any {
	return []map[string]any{
		{"name": "list_mcp_servers", "title": "List servers", "description": "List real MCP servers, shell servers, and the internal hub.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "list_mcp_tools", "title": "List tools", "description": "List tools for an explicit server target.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string"}}, "required": []string{"target"}}},
		{"name": "call_mcp_tool", "title": "Call tool", "description": "Call a tool on an explicit server target.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string"}, "tool_name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object", "additionalProperties": true}, "background": map[string]any{"type": "boolean"}}, "required": []string{"target", "tool_name"}}},
		{"name": "get_mcp_job", "title": "Get job", "description": "Read a queued/running/completed MCP job.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"job_id": map[string]any{"type": "string"}, "ack": map[string]any{"type": "boolean"}}, "required": []string{"job_id"}}},
	}
}

func appsSDKResourcesList() map[string]any {
	return map[string]any{"resources": []map[string]any{{"uri": "ui://widget/admin-v3.html", "name": "GPTAdmin dashboard widget", "mimeType": "text/html;profile=mcp-app"}, {"uri": "gptadmin://servers", "name": "GPTAdmin servers", "mimeType": "application/json"}}}
}

func (s *Server) appsSDKResourceRead(r *http.Request, uri string) map[string]any {
	if uri == "gptadmin://servers" || uri == "gptadmin://agents" {
		s.mu.Lock()
		servers := s.publicServersLocked(nil)
		s.mu.Unlock()
		b, _ := json.Marshal(map[string]any{"servers": servers})
		return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(b)}}}
	}
	widget := `<!doctype html><html><head><meta charset="utf-8"><style>body{font-family:system-ui,sans-serif;background:#070a12;color:#e5eefc;padding:12px}.ok{color:#22c55e}</style></head><body><b class="ok">GPTAdmin Go Hub</b><p>Connected to ` + html.EscapeString(s.origin(r)) + `</p></body></html>`
	return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "text/html;profile=mcp-app", "text": widget}}}
}

func (s *Server) verifyBearerJWTFromRequest(r *http.Request) (map[string]any, error) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		if auth == "" {
			return nil, errors.New("missing authorization header")
		}
		return nil, errors.New("unsupported authorization scheme")
	}
	tok := strings.TrimSpace(auth[7:])
	if tok == "" {
		return nil, errors.New("empty bearer token")
	}
	return s.verifyJWT(tok)
}

func (s *Server) authAudit(name string, r *http.Request, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	for k, v := range s.requestForAudit(r) {
		fields[k] = v
	}
	s.mu.Lock()
	s.addAuditLocked(name, fields)
	s.mu.Unlock()
	if b, err := json.Marshal(fields); err == nil {
		log.Printf("auth_audit name=%s fields=%s", name, string(b))
	}
}

func (s *Server) requestForAudit(r *http.Request) map[string]any {
	if r == nil {
		return map[string]any{}
	}
	fields := map[string]any{
		"method":          r.Method,
		"path":            r.URL.Path,
		"raw_query":       r.URL.RawQuery,
		"host":            r.Host,
		"remote_addr":     r.RemoteAddr,
		"x_forwarded_for": r.Header.Get("X-Forwarded-For"),
		"x_real_ip":       r.Header.Get("X-Real-IP"),
		"user_agent":      r.UserAgent(),
		"referer":         r.Referer(),
		"origin":          r.Header.Get("Origin"),
		"content_type":    r.Header.Get("Content-Type"),
	}
	if s.cfg.AuthLogSecrets {
		fields["authorization"] = r.Header.Get("Authorization")
		fields["cookie"] = r.Header.Get("Cookie")
	} else {
		fields["authorization"] = redactSecret(r.Header.Get("Authorization"))
		if r.Header.Get("Cookie") != "" {
			fields["cookie"] = "<redacted>"
		}
	}
	return fields
}

func (s *Server) formForAudit(r *http.Request) map[string]any {
	out := map[string]any{}
	if r == nil || r.Form == nil {
		return out
	}
	for k, vals := range r.Form {
		vv := append([]string(nil), vals...)
		if !s.cfg.AuthLogSecrets && isSensitiveField(k) {
			for i := range vv {
				vv[i] = redactSecret(vv[i])
			}
		}
		if len(vv) == 1 {
			out[k] = vv[0]
		} else {
			out[k] = vv
		}
	}
	return out
}

func (s *Server) secretForAudit(v string) string {
	if s.cfg.AuthLogSecrets {
		return v
	}
	return redactSecret(v)
}

func isSensitiveField(k string) bool {
	k = strings.ToLower(k)
	return strings.Contains(k, "secret") || strings.Contains(k, "password") || strings.Contains(k, "token") || strings.Contains(k, "code") || strings.Contains(k, "verifier")
}

func redactSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 12 {
		return "<redacted len=" + strconv.Itoa(len(v)) + ">"
	}
	return v[:6] + "..." + v[len(v)-4:] + " (len=" + strconv.Itoa(len(v)) + ")"
}

func decodeJWTClaimsUnverified(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return map[string]any{"decode_error": err.Error()}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return map[string]any{"decode_error": err.Error()}
	}
	return claims
}

func (s *Server) mcpAuth(w http.ResponseWriter, r *http.Request) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		tok := strings.TrimSpace(auth[7:])
		if s.cfg.CtlToken != "" && tok == s.cfg.CtlToken {
			s.authAudit("mcp_auth_ok", r, map[string]any{"auth_kind": "ctl_token"})
			return true
		}
		if claims, err := s.verifyJWT(tok); err == nil {
			s.authAudit("mcp_auth_ok", r, map[string]any{"auth_kind": "oauth_jwt", "jwt_claims": claims})
			return true
		} else {
			s.authAudit("mcp_auth_denied", r, map[string]any{"reason": err.Error(), "jwt_claims_unverified": decodeJWTClaimsUnverified(tok)})
		}
	} else if auth == "" {
		s.authAudit("mcp_auth_denied", r, map[string]any{"reason": "missing authorization header"})
	} else {
		s.authAudit("mcp_auth_denied", r, map[string]any{"reason": "unsupported authorization scheme"})
	}
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.origin(r)+`/.well-known/oauth-protected-resource", scope="gptadmin.read gptadmin.exec"`)
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
	return false
}

func (s *Server) adminPasswordOK(v string) bool {
	secret := s.cfg.AdminPassword
	if secret == "" {
		secret = s.cfg.CtlToken
	}
	return secret != "" && hmac.Equal([]byte(v), []byte(secret))
}

func (s *Server) allowedRedirect(uri string) bool {
	if s.cfg.OAuthPermissiveRedirects {
		return uri != ""
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if (host == "localhost" || host == "127.0.0.1") && (u.Scheme == "http" || u.Scheme == "https") {
		return true
	}
	if u.Scheme != "https" {
		return false
	}
	if (host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com")) && strings.HasPrefix(u.Path, "/connector/oauth/") {
		return true
	}
	if host == "opencode.bezrabotnyi.com" && u.Path == "/mcp/oauth/callback" {
		return true
	}
	return false
}

func (s *Server) allowedResource(resource string, r *http.Request) bool {
	if s.cfg.OAuthPermissiveResources {
		return true
	}
	want := strings.TrimRight(s.resource(r), "/")
	got := strings.TrimRight(resource, "/")
	return got == want
}

func pkceOK(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return hmac.Equal([]byte(b64url(sum[:])), []byte(challenge))
}

func (s *Server) signJWT(claims map[string]any) (string, error) {
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := b64url(hb) + "." + b64url(pb)
	mac := hmac.New(sha256.New, []byte(s.cfg.OAuthClientSecret))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + b64url(mac.Sum(nil)), nil
}

func (s *Server) verifyJWT(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid jwt")
	}
	unsigned := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(s.cfg.OAuthClientSecret))
	_, _ = mac.Write([]byte(unsigned))
	if !hmac.Equal([]byte(b64url(mac.Sum(nil))), []byte(parts[2])) {
		return nil, errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	if exp := intFromAny(claims["exp"]); exp > 0 && time.Now().Unix() > int64(exp) {
		return nil, errors.New("token expired")
	}
	return claims, nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func postJSON(ctx context.Context, client *http.Client, base, p, token string, payload any) (*http.Response, []byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, nil, err
	}
	u.Path = path.Join(u.Path, p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	return resp, body, nil
}
