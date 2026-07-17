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
	"net/http/httputil"
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
	Addr                       string
	ConfigDir                  string
	PublicDir                  string
	ArtifactDir                string
	CtlToken                   string
	RelayAgentToken            string
	ShellToken                 string
	DefaultTimeout             time.Duration
	PollMaxTimeout             time.Duration
	OutputDir                  string
	PublicOrigin               string
	MCPResource                string
	AdminPassword              string
	OAuthClientSecret          string
	EnvFile                    string
	OAuthPermissiveRedirects   bool
	OAuthPermissiveResources   bool
	AuthLogSecrets             bool
	BridgeKey                  string
	RegistryStateFile          string
	FailoverConfigFile         string
	FailoverStateFile          string
	FailoverReclaimCommandFile string
}

func FromEnv() Config {
	port := env("GPTADMIN_HUB_PORT", env("HUB_PORT", env("PORT", "9001")))
	host := env("GPTADMIN_HUB_HOST", env("HUB_HOST", ""))
	root := env("GPTADMIN_ROOT", ".")
	cfgDir := env("GPTADMIN_CONFIG_DIR", filepath.Join(root, "config"))
	defTimeout := secondsEnv("MCP_RELAY_DEFAULT_TIMEOUT", 30)
	pollTimeout := secondsEnv("MCP_RELAY_POLL_MAX_TIMEOUT", 55)
	return Config{
		Addr:                       host + ":" + port,
		ConfigDir:                  cfgDir,
		PublicDir:                  env("GPTADMIN_PUBLIC_DIR", filepath.Join(root, "public")),
		ArtifactDir:                env("GPTADMIN_ARTIFACT_DIR", filepath.Join(root, "build")),
		CtlToken:                   env("CTL_TOKEN", env("GPTADMIN_CTL_TOKEN", "")),
		RelayAgentToken:            env("MCP_RELAY_AGENT_TOKEN", env("GPTADMIN_MCP_RELAY_AGENT_TOKEN", "")),
		ShellToken:                 env("SHELL_TOKEN", env("SHELLMCP_TOKEN", "")),
		DefaultTimeout:             time.Duration(defTimeout) * time.Second,
		PollMaxTimeout:             time.Duration(pollTimeout) * time.Second,
		OutputDir:                  env("GPTADMIN_OUTPUT_DIR", filepath.Join(cfgDir, "outputs")),
		PublicOrigin:               strings.TrimRight(env("PUBLIC_ORIGIN", ""), "/"),
		MCPResource:                strings.TrimRight(env("MCP_RESOURCE", env("PUBLIC_ORIGIN", "")), "/"),
		AdminPassword:              env("ADMIN_PASSWORD", ""),
		OAuthClientSecret:          env("OAUTH_CLIENT_SECRET", ""),
		EnvFile:                    env("GPTADMIN_ENV_FILE", "/etc/gptadmin/gptadmin.env"),
		OAuthPermissiveRedirects:   truthyString(env("OAUTH_PERMISSIVE_REDIRECTS", "0")),
		OAuthPermissiveResources:   truthyString(env("OAUTH_PERMISSIVE_RESOURCES", "0")),
		AuthLogSecrets:             truthyString(env("AUTH_LOG_SECRETS", "0")),
		BridgeKey:                  env("MCP_BRIDGE_KEY", env("CTL_TOKEN", "")),
		RegistryStateFile:          env("GPTADMIN_REGISTRY_STATE_FILE", filepath.Join(cfgDir, "registry_state.json")),
		FailoverConfigFile:         env("GPTADMIN_FAILOVER_CONFIG_FILE", filepath.Join(cfgDir, "failover_config.json")),
		FailoverStateFile:          env("GPTADMIN_FAILOVER_STATE_FILE", filepath.Join(cfgDir, "failover_state.json")),
		FailoverReclaimCommandFile: env("GPTADMIN_FAILOVER_RECLAIM_COMMAND_FILE", filepath.Join(cfgDir, "failover_reclaim_command.json")),
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
	ToolName  string         `json:"tool_name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Cmd       string         `json:"cmd,omitempty"`
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

// managedMCPToken stores revocation metadata only. The bearer value is never
// persisted, so an operator can revoke or rotate a client without creating a
// second secret database.
type managedMCPToken struct {
	ID         string `json:"id"`
	ClientID   string `json:"client_id"`
	TokenKind  string `json:"token_kind,omitempty"`
	Scope      string `json:"scope"`
	AccessMode string `json:"access_mode"`
	IssuedAt   int64  `json:"issued_at"`
	ExpiresAt  int64  `json:"expires_at"`
	RevokedAt  int64  `json:"revoked_at,omitempty"`
}

type managedMCPTokenState struct {
	Tokens map[string]managedMCPToken `json:"tokens"`
}

type idempotencyEntry struct {
	Fingerprint string
	CreatedAt   time.Time
	Done        chan struct{}
	JobID       string
	Response    map[string]any
	Status      int
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
	idempotency map[string]*idempotencyEntry
	oauthCodes  map[string]oauthCode
	managedMCP  map[string]managedMCPToken
	audit       []auditEvent
	failover    FailoverConfig

	updateStatePath string
	updateLockPath  string
	updateLauncher  *UpdateLauncher
}

func New(cfg Config) *Server {
	s := &Server{
		cfg:         cfg,
		agents:      map[string]*Agent{},
		relayQueues: map[string][]string{},
		relayJobs:   map[string]*relayJob{},
		shellQueues: map[string][]string{},
		shellJobs:   map[string]*shellJob{},
		idempotency: map[string]*idempotencyEntry{},
		oauthCodes:  map[string]oauthCode{},
		managedMCP:  map[string]managedMCPToken{},
		audit:       []auditEvent{},
	}
	s.cond = sync.NewCond(&s.mu)
	if err := s.loadRegistryState(); err != nil {
		log.Printf("registry state load failed path=%s err=%v", s.registryStatePath(), err)
	}
	if err := s.loadManagedMCPState(); err != nil {
		log.Printf("MCP token state load failed path=%s err=%v", s.managedMCPStatePath(), err)
	}
	s.failover = s.loadFailoverConfig()
	home := os.Getenv("GPTADMIN_HOME")
	if home == "" {
		userHome, _ := os.UserHomeDir()
		home = userHome + "/.gptadmin"
	}
	s.updateStatePath = home + "/update_state.json"
	s.updateLockPath = home + "/update.lock"
	s.updateLauncher = DefaultUpdateLauncher()
	return s
}

func (s *Server) managedMCPStatePath() string {
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "mcp_tokens_state.json")
}

func (s *Server) loadManagedMCPState() error {
	path := s.managedMCPStatePath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state managedMCPTokenState
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	if state.Tokens != nil {
		s.managedMCP = state.Tokens
	}
	return nil
}

func (s *Server) saveManagedMCPStateLocked() error {
	path := s.managedMCPStatePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(managedMCPTokenState{Tokens: s.managedMCP}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	mux.HandleFunc("/artifacts/shellmcp-android-arm64.json", s.requireCtl(s.androidShellmcpArtifactManifest))
	mux.HandleFunc("/artifacts/shellmcp-android-arm64.bin", s.requireCtl(s.androidShellmcpArtifactDownload))
	// Legacy rootd artifact aliases: old services still point ROOTD_UPDATE_MANIFEST_URL here.
	mux.HandleFunc("/artifacts/rootd.json", s.requireCtl(s.shellmcpArtifactManifest))
	mux.HandleFunc("/artifacts/rootd.tar.gz", s.requireCtl(s.shellmcpArtifactDownload))
	mux.HandleFunc("/heartbeat", s.requireShell(s.heartbeat))
	mux.HandleFunc("/servers", s.requireCtl(s.serversList))
	mux.HandleFunc("/bulk/exec", s.requireCtl(s.bulkExec))
	mux.HandleFunc("/queue/", s.requireShell(s.queue))
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
	mux.HandleFunc("/mcp-relay/shell_exec", s.requireCtl(s.mcpRelayShellExec))
	mux.HandleFunc("/mcp-relay/get_mcp_job/", s.requireCtl(s.mcpRelayJob))
	mux.HandleFunc("/mcp-relay/job/", s.requireCtl(s.mcpRelayJob))
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.oauthProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.oauthAuthorizationServer)
	mux.HandleFunc("/register", s.oauthRegister)
	mux.HandleFunc("/authorize", s.oauthAuthorize)
	mux.HandleFunc("/token", s.oauthToken)
	mux.HandleFunc("/mcp", s.mcpEndpoint)
	mux.HandleFunc("/_services/", s.httpServiceEndpoint)
	mux.HandleFunc("/server/", s.serverMCPEndpoint)
	// Legacy alias kept for old pinned MCP URLs.
	mux.HandleFunc("/agent/", s.agentMCPEndpoint)
	mux.HandleFunc("/mcp-prompt/prompt", s.mcpPrompt)
	mux.HandleFunc("/mcp-prompt/call", s.mcpPromptCall)
	mux.HandleFunc("/admin/api/mcp/manage", s.requireCtl(s.adminMCPManage))
	mux.HandleFunc("/admin/api/mcp/issue-token", s.requireCtl(s.adminMCPIssueToken))
	mux.HandleFunc("/admin/api/mcp/tokens/", s.requireCtl(s.adminMCPTokenAction))
	mux.HandleFunc("/admin/api/mcp/resources/list", s.requireCtl(s.adminMCPResourcesList))
	mux.HandleFunc("/admin/api/mcp/resources/read", s.requireCtl(s.adminMCPResourceRead))
	mux.HandleFunc("/admin/api/auth/rotate-oauth", s.requireCtl(s.adminRotateOAuth))
	mux.HandleFunc("/admin/api/security/env", s.requireCtl(s.adminSecurityEnv))
	mux.HandleFunc("/admin/api/clients/revoke-all", s.requireCtl(s.adminClientsRevokeAll))
	mux.HandleFunc("/admin/api/clients/", s.requireCtl(s.adminClientDelete))
	mux.HandleFunc("/admin/api/overview", s.requireCtl(s.adminOverview))
	mux.HandleFunc("/admin/api/update", s.requireCtl(s.adminTriggerUpdate))
	mux.HandleFunc("/admin/api/failover/state", s.requireCtl(s.adminFailoverState))
	mux.HandleFunc("/admin/api/failover/reclaim/accept", s.requireCtl(s.adminFailoverReclaimAccept))
	mux.HandleFunc("/admin/api/failover/reclaim", s.requireCtl(s.adminFailoverReclaim))
	mux.HandleFunc("/admin/api/failover", s.requireCtl(s.adminFailover))
	mux.HandleFunc("/admin/api/jobs", s.requireCtl(s.adminJobs))
	mux.HandleFunc("/admin/api/audit", s.requireCtl(s.adminAudit))
	mux.HandleFunc("/admin/api/clients", s.requireCtl(s.adminClients))
	mux.HandleFunc("/admin/login", s.adminLogin)
	mux.HandleFunc("/admin/logout", s.adminLogout)
	mux.HandleFunc("/admin/", s.adminStatic)
	mux.HandleFunc("/admin", s.adminIndex)
	return withCORS(mux)
}

func (s *Server) httpServiceEndpoint(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/_services/")
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	serviceSlug, endpointName := parts[0], parts[1]
	var endpoint map[string]any
	s.mu.Lock()
	for _, a := range s.agents {
		if agentSlug(a.AgentID) != serviceSlug && agentSlug(a.Name) != serviceSlug {
			continue
		}
		for _, raw := range sliceValue(a.Meta["http_endpoints"]) {
			ep := mapValue(raw)
			if firstString(ep, "name") == endpointName {
				endpoint = ep
				break
			}
		}
		break
	}
	s.mu.Unlock()
	if endpoint == nil {
		http.NotFound(w, r)
		return
	}
	// Only endpoints that explicitly opt in as a public capability are exposed
	// through the tunnel. This prevents a private MCP that happens to register an
	// http_endpoints entry from accidentally becoming publicly reachable.
	if !isPublicCapability(endpoint) {
		http.NotFound(w, r)
		return
	}
	targetRaw := firstString(endpoint, "local_url")
	target, err := url.Parse(targetRaw)
	if err != nil || target.Scheme != "http" || !isLoopbackServiceHost(target.Hostname()) || target.Port() == "" {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "invalid service endpoint"})
		return
	}
	prefix := "/_services/" + serviceSlug + "/" + endpointName
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		if truthyAny(endpoint["strip_prefix"]) {
			rest := strings.TrimPrefix(req.URL.Path, prefix)
			if rest == "" {
				rest = "/"
			}
			req.URL.Path = rest
		}
		req.Header.Set("X-Forwarded-Prefix", prefix)
		req.Header.Set("X-Forwarded-Proto", requestScheme(r))
		req.Header.Set("X-Forwarded-Host", r.Host)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("http service proxy failed agent=%s endpoint=%s target=%s err=%v", serviceSlug, endpointName, targetRaw, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "service unavailable"})
	}
	proxy.ServeHTTP(w, r)
}

func isLoopbackServiceHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// isPublicCapability reports whether an http_endpoints entry has explicitly
// opted in to being reachable through the public tunnel ingress.
func isPublicCapability(endpoint map[string]any) bool {
	return strings.TrimSpace(strings.ToLower(firstString(endpoint, "visibility"))) == "public-capability"
}

func truthyAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return truthyString(x)
	default:
		return false
	}
}

func sliceValue(v any) []any {
	if xs, ok := v.([]any); ok {
		return xs
	}
	return nil
}

func requestScheme(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); v != "" {
		return v
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
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
		if s.cfg.CtlToken != "" && tokenMatches(r, s.cfg.CtlToken) {
			s.authAudit("ctl_auth_ok", r, map[string]any{"auth_kind": "ctl_token"})
			next(w, r)
			return
		}
		// An unset admin password means the dashboard has no cookie gate; it must
		// not turn every request into an authenticated relay API request.
		if s.cfg.AdminPassword != "" && s.adminSessionValid(r) {
			s.authAudit("ctl_auth_ok", r, map[string]any{"auth_kind": "admin_cookie"})
			next(w, r)
			return
		}
		if claims, err := s.verifyBearerJWTFromRequest(r); err == nil {
			s.authAudit("ctl_auth_ok", r, map[string]any{"auth_kind": "oauth_jwt", "jwt_claims": claims})
			*r = *requestWithAuthClaims(r, claims)
			if !mcpClientHTTPPathAllowed(r.URL.Path) {
				detail := "MCP client credentials cannot access the admin API"
				if requestAccessMode(r) == accessModeReadonly {
					detail = "read-only client cannot access the admin API"
				}
				writeJSON(w, http.StatusForbidden, map[string]any{"detail": detail})
				return
			}
			next(w, r)
			return
		} else {
			s.authAudit("ctl_auth_denied", r, map[string]any{"reason": err.Error()})
		}
		s.writeCtlUnauthorized(w, r)
	}
}

func (s *Server) requireRelay(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.RelayAgentToken != "" && (tokenMatches(r, s.cfg.RelayAgentToken) || hmac.Equal([]byte(r.Header.Get("X-MCP-Relay-Token")), []byte(s.cfg.RelayAgentToken))) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "unauthorized"})
	return false
}

func (s *Server) requireShell(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.ShellToken == "" || !tokenMatches(r, s.cfg.ShellToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func tokenMatches(r *http.Request, expected string) bool {
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
  version: "1.0.0"
  description: |
    Compact control API: discover → schema → execute. Poll job when background=true.

    Shell hosts and MCP services are exposed as GPTAdmin servers with ids like shell:<server_name>.
    The hub itself is exposed as target "hub" for registry and approval tools.
servers:
  - url: %s
security:
  - bearerAuth: []
paths:
  /mcp-relay/servers:
    get:
      operationId: discover
      summary: Discover targets
      description: List compact MCP targets. Add detail=full only when metadata is needed.
      parameters:
        - name: detail
          in: query
          required: false
          description: Opt in to transport, capabilities and metadata.
          schema:
            type: string
            enum: [full]
      responses:
        "200":
          description: Available MCP servers
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/DiscoverResponse"
  /mcp-relay/tools:
    post:
      operationId: schema
      summary: Get target schema
      description: List tools for one target from discover. Never use target="default".
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/SchemaRequest"
      responses:
        "200":
          description: Tool list response or background job reference
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Result"
  /mcp-relay/call:
    post:
      operationId: execute
      summary: Execute one tool
      description: Execute one tool on one selected target. Use schema first.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/ExecuteRequest"
      responses:
        "200":
          description: Tool call response or background job reference
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Result"
  /mcp-relay/job/{job_id}:
    get:
      operationId: job
      summary: Get job
      description: Read a background job by id.
      parameters:
        - name: job_id
          in: path
          required: true
          description: Job id returned by execute.
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
                $ref: "#/components/schemas/Job"
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
  schemas:
    DiscoverResponse:
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
                  description: Target id to use in schema and execute.
        name:
          type: string
        kind:
          type: string
          enum: [real_mcp, virtual_shell, virtual_hub, hub]
        transport:
          type: string
          nullable: true
        status:
          type: string
          enum: [online, offline, stale]
        last_seen:
          type: number
          nullable: true
        capabilities:
          type: array
          items:
            type: string
        meta:
          type: object
          additionalProperties: true
    SchemaRequest:
      type: object
      additionalProperties: false
      required: [target]
      properties:
        target:
          type: string
          description: Target id from discover. Never use "default".
        timeout:
          type: integer
          nullable: true
          minimum: 1
          maximum: 35
          default: 30
        background:
          type: boolean
          default: false
    ExecuteRequest:
      type: object
      additionalProperties: true
      required: [target, tool]
      properties:
        target:
          type: string
          description: Target id from discover. Never use "default".
        tool:
          type: string
          description: Tool name from schema.
        tool_name:
          type: string
        arguments:
          type: object
          additionalProperties: true
          default: {}
        args:
          type: object
          additionalProperties: true
          default: {}
        cmd:
          type: string
          nullable: true
        query:
          type: string
          nullable: true
        cwd:
          type: string
          nullable: true
        timeout:
          type: integer
          nullable: true
          minimum: 1
          maximum: 35
          default: 30
        background:
          type: boolean
          default: false
        idempotency_key:
          type: string
          minLength: 1
          maxLength: 200
          description: Reuse only for the same operation.
    Result:
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
          type: object
          nullable: true
          additionalProperties: true
        background:
          type: boolean
        job_id:
          type: string
          nullable: true
        message:
          type: string
          nullable: true
        error:
          type: object
          nullable: true
          additionalProperties: true
    Job:
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
          type: string
          nullable: true
        response:
          type: object
          nullable: true
          additionalProperties: true
        error:
          type: object
          nullable: true
          additionalProperties: true
        acked:
          type: boolean
          default: false
    McpError:
      type: object
      additionalProperties: true
      properties:
        message:
          type: string
          nullable: true
        code:
          type: string
          nullable: true
`, origin)
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

func (s *Server) androidShellmcpBinaryPath() string {
	return filepath.Join(s.cfg.ArtifactDir, "android-arm64", "bin", "shellmcp")
}

func (s *Server) androidShellmcpBuildVersion() (int, error) {
	versionPath := filepath.Join(s.cfg.ArtifactDir, "gptadmin-android-arm64.version")
	raw, err := os.ReadFile(versionPath)
	if err != nil {
		return 0, fmt.Errorf("read Android artifact version: %w", err)
	}
	version, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("invalid Android artifact version %q", strings.TrimSpace(string(raw)))
	}
	return version, nil
}

func (s *Server) androidShellmcpArtifactManifest(w http.ResponseWriter, r *http.Request) {
	binary := s.androidShellmcpBinaryPath()
	st, err := os.Stat(binary)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Android shellmcp binary not found: " + binary})
		return
	}
	version, err := s.androidShellmcpBuildVersion()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	sha, err := sha256File(binary)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"component": "shellmcp-android-arm64", "build_version": version, "sha256": sha, "size": st.Size(), "url": s.origin(r) + "/artifacts/shellmcp-android-arm64.bin"})
}

func (s *Server) androidShellmcpArtifactDownload(w http.ResponseWriter, r *http.Request) {
	binary := s.androidShellmcpBinaryPath()
	if _, err := os.Stat(binary); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Android shellmcp binary not found: " + binary})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="shellmcp-android-arm64"`)
	http.ServeFile(w, r, binary)
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
	s.touchShellPollLocked(name, r)
	for {
		s.touchShellPollLocked(name, r)
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

func (s *Server) touchShellPollLocked(name string, r *http.Request) {
	if name == "" {
		return
	}
	now := nowFloat()
	agentID := "shell:" + name
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = "long_poll"
	}
	meta := map[string]any{
		"mode":                  mode,
		"transport_role":        firstNonEmpty(r.URL.Query().Get("transport_role"), "shellmcp_transport_layer"),
		"backend":               firstNonEmpty(r.URL.Query().Get("backend"), "local"),
		"poll_heartbeat":        true,
		"heartbeat_best_effort": true,
	}
	for _, key := range []string{"server_id", "public_key", "fingerprint", "base_url", "os", "git_commit", "default_user", "default_home", "default_cwd"} {
		if v := strings.TrimSpace(r.URL.Query().Get(key)); v != "" {
			meta[key] = v
		}
	}
	for _, key := range []string{"cores", "mem_mb", "build_version"} {
		if v := intFromString(r.URL.Query().Get(key)); v > 0 {
			meta[key] = v
		}
	}
	if a := s.agents[agentID]; a != nil {
		a.Status = "online"
		a.LastSeen = now
		a.Transport = mode
		if a.Meta == nil {
			a.Meta = map[string]any{}
		}
		for k, v := range meta {
			a.Meta[k] = v
		}
		return
	}
	s.agents[agentID] = &Agent{AgentID: agentID, Name: "Shell: " + name, Kind: "virtual_shell", Transport: mode, Status: "online", LastSeen: now, Capabilities: []string{"shell", "system", "tasks", "logs"}, Meta: meta}
	s.addAuditLocked("queue_poll_register", map[string]any{"agent_id": agentID, "transport": mode})
	if err := s.saveRegistryStateLocked(); err != nil {
		log.Printf("registry state save failed: %v", err)
	}
	if err := s.saveFailoverStateBundleLocked(); err != nil {
		log.Printf("failover state save failed: %v", err)
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
	s.touchShellPollLocked(name, r)
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
	for _, entry := range s.idempotency {
		if entry.JobID == job.ID {
			entry.Response = cloneMap(relayJobResponse(job))
		}
	}
	s.addAuditLocked("mcp_result", map[string]any{"server_id": agentID, "job_id": res.ID, "status": job.Status})
	s.cond.Broadcast()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) mcpRelayServers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	servers := s.publicServersLockedWithDetail(r, fullDetailRequested(r.URL.Query().Get("detail")))
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
	return s.publicServersLockedWithDetail(r, false)
}

func (s *Server) publicServersLockedWithDetail(r *http.Request, detail bool) []map[string]any {
	agents := s.publicAgentsLocked(r)
	servers := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		server := agentAsServer(a)
		if !detail {
			server = compactServer(server)
		}
		servers = append(servers, server)
	}
	return servers
}

func compactServer(server map[string]any) map[string]any {
	return map[string]any{
		"server_id": server["server_id"],
		"name":      server["name"],
		"kind":      server["kind"],
		"status":    server["status"],
	}
}

func fullDetailRequested(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "full") || truthyString(v)
	default:
		return false
	}
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

// selectMCPRelayTarget validates a target before it can create a relay job.
// The relay must never infer a target because that can route an operation to
// an unrelated server.
func (s *Server) selectMCPRelayTarget(target string) (string, int, string) {
	target = strings.TrimSpace(target)
	if target == "" || target == "default" {
		return "", http.StatusBadRequest, "Explicit MCP target is required. Call listMcpServers first and pass one returned server_id. There is no default target."
	}
	if target == "hub" {
		return target, http.StatusOK, ""
	}

	s.mu.Lock()
	_, exists := s.agents[target]
	s.mu.Unlock()
	if exists {
		return target, http.StatusOK, ""
	}
	if strings.HasPrefix(target, "shell:") {
		return "", http.StatusNotFound, fmt.Sprintf("unknown shell server %s", strings.TrimPrefix(target, "shell:"))
	}
	return "", http.StatusNotFound, fmt.Sprintf("unknown MCP relay server %s", target)
}

func (s *Server) mcpRelayTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	_ = readJSON(r, &req)
	target := firstString(req, "target", "server_id", "agent_id")
	selectedTarget, status, detail := s.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]any{"detail": detail})
		return
	}
	target = selectedTarget
	if target == "hub" {
		writeJSON(w, http.StatusOK, withActionToolHints(map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": toolsForRequest(r, target, hubTools())}}, target))
		return
	}
	if strings.HasPrefix(target, "shell:") {
		writeJSON(w, http.StatusOK, withActionToolHints(map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": toolsForRequest(r, target, shellTools())}}, target))
		return
	}
	if requestAccessMode(r) == accessModeReadonly {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": []map[string]any{}}})
		return
	}
	jobID := s.enqueueRelay(target, "tools/list", nil)
	if truthy(req["background"]) {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": target, "status": "running", "background": true, "job_id": jobID})
		return
	}
	resp := withActionToolHints(s.waitRelay(jobID, timeoutFromReq(req, s.cfg.DefaultTimeout)), target)
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
	toolName := firstString(req, "tool", "tool_name", "name")
	args := mapValue(req["arguments"])
	if len(args) == 0 {
		args = mapValue(req["args"])
	}
	if len(args) == 0 {
		args = toolArgsFromTopLevel(req)
	}
	if toolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing tool_name"})
		return
	}
	if err := authorizeToolCall(r, target, toolName); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
		return
	}
	selectedTarget, status, detail := s.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]any{"detail": detail})
		return
	}
	target = selectedTarget
	resp, status := s.executeMCPTool(r, target, toolName, args, truthy(req["background"]), timeoutFromReq(req, s.cfg.DefaultTimeout), firstString(req, "idempotency_key"))
	writeJSON(w, status, resp)
}

const (
	idempotencyTTL     = 15 * time.Minute
	idempotencyMaxSize = 1024
	idempotencyKeyMax  = 200
)

func (s *Server) executeMCPTool(r *http.Request, target, toolName string, args map[string]any, background bool, timeout time.Duration, key string) (map[string]any, int) {
	operation := func() (map[string]any, int) {
		if target == "hub" {
			resp, status := s.callHubTool(toolName, args)
			return map[string]any{"server_id": target, "status": "completed", "response": resp}, status
		}
		if strings.HasPrefix(target, "shell:") {
			return s.callShellTool(target, toolName, args, background, timeout), http.StatusOK
		}
		jobID := s.enqueueRelay(target, "tools/call", map[string]any{"name": toolName, "arguments": args})
		if background {
			return map[string]any{"server_id": target, "status": "running", "background": true, "job_id": jobID}, http.StatusOK
		}
		return s.waitRelay(jobID, timeout), http.StatusOK
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return operation()
	}
	if len(key) > idempotencyKeyMax {
		return map[string]any{"detail": fmt.Sprintf("idempotency_key must be at most %d characters", idempotencyKeyMax)}, http.StatusBadRequest
	}
	fingerprintBytes, err := json.Marshal(struct {
		Target    string         `json:"target"`
		ToolName  string         `json:"tool_name"`
		Arguments map[string]any `json:"arguments"`
	}{target, toolName, args})
	if err != nil {
		return map[string]any{"detail": "arguments cannot be serialized for idempotency"}, http.StatusBadRequest
	}
	fingerprint := sha256Hex(fingerprintBytes)
	authorization := ""
	if r != nil {
		authorization = r.Header.Get("Authorization")
	}
	scope := sha256Hex([]byte(authorization))
	entryKey := scope + ":" + key

	s.mu.Lock()
	now := time.Now()
	for existingKey, entry := range s.idempotency {
		if now.Sub(entry.CreatedAt) > idempotencyTTL {
			delete(s.idempotency, existingKey)
		}
	}
	if len(s.idempotency) >= idempotencyMaxSize {
		for existingKey, existingEntry := range s.idempotency {
			select {
			case <-existingEntry.Done:
				delete(s.idempotency, existingKey)
			default:
			}
			if len(s.idempotency) < idempotencyMaxSize {
				break
			}
		}
		if len(s.idempotency) >= idempotencyMaxSize {
			s.mu.Unlock()
			return map[string]any{"detail": "idempotency store is temporarily full; retry later"}, http.StatusTooManyRequests
		}
	}
	if entry := s.idempotency[entryKey]; entry != nil {
		if entry.Fingerprint != fingerprint {
			s.mu.Unlock()
			return map[string]any{"detail": "idempotency_key was already used for different target, tool_name, or arguments"}, http.StatusConflict
		}
		done := entry.Done
		s.mu.Unlock()
		select {
		case <-done:
			s.mu.Lock()
			response, status := cloneMap(entry.Response), entry.Status
			s.mu.Unlock()
			return response, status
		case <-time.After(timeout):
			return map[string]any{"status": "running", "idempotency_key": key, "message": "the original MCP call is still running"}, http.StatusAccepted
		}
	}
	entry := &idempotencyEntry{Fingerprint: fingerprint, CreatedAt: now, Done: make(chan struct{})}
	s.idempotency[entryKey] = entry
	s.mu.Unlock()

	response, status := operation()
	s.mu.Lock()
	entry.JobID = firstString(response, "job_id")
	entry.Response = cloneMap(response)
	entry.Status = status
	close(entry.Done)
	s.mu.Unlock()
	return response, status
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func toolArgsFromTopLevel(req map[string]any) map[string]any {
	reserved := map[string]bool{
		"target": true, "server_id": true, "agent_id": true,
		"tool": true, "tool_name": true, "name": true,
		"arguments": true, "args": true,
		"background":      true,
		"idempotency_key": true,
	}
	args := map[string]any{}
	for k, v := range req {
		if reserved[k] || v == nil {
			continue
		}
		args[k] = v
	}
	return args
}

func withActionToolHints(resp map[string]any, target string) map[string]any {
	response := mapValue(resp["response"])
	rawTools, ok := response["tools"].([]map[string]any)
	if !ok {
		if items, ok := response["tools"].([]any); ok {
			rawTools = make([]map[string]any, 0, len(items))
			for _, item := range items {
				if tool, ok := item.(map[string]any); ok {
					rawTools = append(rawTools, tool)
				}
			}
		}
	}
	for _, tool := range rawTools {
		name := firstString(tool, "name")
		if name == "" {
			continue
		}
		hint := map[string]any{"operationId": "callMcpTool", "target": target, "tool_name": name}
		for _, field := range actionShortcutFields(tool) {
			hint[field] = "<" + field + ">"
		}
		tool["gptadmin_action_call"] = hint
	}
	return resp
}

func actionShortcutFields(tool map[string]any) []string {
	schema := mapValue(tool["inputSchema"])
	props := mapValue(schema["properties"])
	out := []string{}
	for _, key := range []string{"cmd", "query", "cwd", "timeout", "run_as_user"} {
		if _, ok := props[key]; ok {
			out = append(out, key)
		}
	}
	return out
}

func (s *Server) mcpRelayShellExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if err := authorizeToolCall(r, "shell:direct", "shell_exec"); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	target := firstString(req, "target", "server_id", "agent_id")
	cmd := firstString(req, "cmd", "command")
	if cmd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing cmd"})
		return
	}
	selectedTarget, status, detail := s.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]any{"detail": detail})
		return
	}
	target = selectedTarget
	if !strings.HasPrefix(target, "shell:") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target must be a shell:* agent"})
		return
	}
	args := map[string]any{
		"cmd":         cmd,
		"cwd":         req["cwd"],
		"timeout":     req["timeout"],
		"run_as_user": firstString(req, "run_as_user", "user"),
	}
	resp := s.callShellTool(target, "shell_exec", args, truthy(req["background"]), timeoutFromReq(req, s.cfg.DefaultTimeout))
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
	case "discover", "listMcpServers", "list_mcp_servers":
		servers := s.publicServersLockedWithDetail(nil, fullDetailRequested(args["detail"]))
		return map[string]any{"servers": servers}, http.StatusOK
	case "listMcpAgents", "list_mcp_agents":
		agents := s.publicAgentsLocked(nil)
		return map[string]any{"agents": agents}, http.StatusOK
	case "pending", "list_pending_servers":
		return map[string]any{"pending": []any{}, "count": 0}, http.StatusOK
	case "hub_status", "status":
		return map[string]any{"ok": true, "servers": len(s.agents), "relay_jobs": len(s.relayJobs), "shell_jobs": len(s.shellJobs)}, http.StatusOK
	default:
		return map[string]any{"error": "unsupported hub tool", "tool": name, "arguments": args}, http.StatusBadRequest
	}
}

func canonicalShellQueueName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "homeassistant", "home-assistant":
		return "haos"
	default:
		return strings.TrimSpace(name)
	}
}

func (s *Server) callShellTool(target, toolName string, args map[string]any, background bool, timeout time.Duration) map[string]any {
	server := canonicalShellQueueName(strings.TrimPrefix(target, "shell:"))
	if toolName == "" {
		return map[string]any{"server_id": target, "status": "failed", "error": "missing tool name"}
	}
	job := &shellJob{ID: newID(), Server: server, ToolName: toolName, Arguments: cloneMap(args), CreatedAt: nowFloat(), Status: "queued"}
	if toolName == "shell_exec" {
		job.Cmd = firstString(args, "cmd", "command")
		if job.Cmd == "" {
			return map[string]any{"server_id": target, "status": "failed", "error": "missing cmd"}
		}
		job.Cwd = firstString(args, "cwd")
		job.Timeout = intFromAny(args["timeout"])
		job.Env = mapValue(args["env"])
	}
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
		{"name": "discover", "description": "List registered targets", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "pending", "description": "List pending approvals", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "status", "description": "Return Hub status", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	}
}

func shellTools() []map[string]any {
	return []map[string]any{
		{"name": "system_inspect", "description": "Read bounded redacted files/directories; no commands", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"read_file", "list_directory"}}, "path": map[string]any{"type": "string"}, "max_bytes": map[string]any{"type": []string{"integer", "null"}, "minimum": 1, "maximum": 1048576}}, "required": []string{"action", "path"}, "additionalProperties": false}},
		{"name": "shell_exec", "description": "Run one command as the default non-root user", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}, "cwd": map[string]any{"type": []string{"string", "null"}}, "timeout": map[string]any{"type": []string{"integer", "null"}}, "run_as_user": map[string]any{"type": []string{"string", "null"}, "description": "Use root only when intentional"}}, "required": []string{"cmd"}}},
		{"name": "mcp_manage", "description": "Manage child MCPs", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "upsert", "remove", "enable", "disable", "restart", "status", "config"}}, "ref": map[string]any{"type": []string{"string", "null"}}, "config": map[string]any{"type": []string{"object", "null"}, "additionalProperties": true}}, "required": []string{"action"}, "additionalProperties": false}},
		{"name": "mcp_tools", "description": "List child MCP tools", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"ref": map[string]any{"type": "string"}}, "required": []string{"ref"}, "additionalProperties": false}},
		{"name": "mcp_call", "description": "Call a child MCP tool", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"ref": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": []string{"object", "null"}, "additionalProperties": true}}, "required": []string{"ref", "name"}, "additionalProperties": false}},
	}
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
	s.mu.Lock()
	revoked := 0
	for id, record := range s.managedMCP {
		if record.TokenKind == "legacy_ctl" {
			continue
		}
		if record.RevokedAt == 0 {
			record.RevokedAt = time.Now().Unix()
			s.managedMCP[id] = record
			revoked++
		}
	}
	err := s.saveManagedMCPStateLocked()
	s.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked_count": revoked})
}

func (s *Server) adminClientDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/api/clients/"))
	if err != nil || id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid token id"})
		return
	}
	s.mu.Lock()
	record, ok := s.managedMCP[id]
	if ok && record.RevokedAt == 0 {
		record.RevokedAt = time.Now().Unix()
		s.managedMCP[id] = record
		err = s.saveManagedMCPStateLocked()
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "MCP token not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": true, "token_id": id})
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
	clients := s.managedMCPClientsLocked()
	s.mu.Unlock()
	hubPublicURL := firstNonEmpty(os.Getenv("HUB_PUBLIC_URL"), s.cfg.PublicOrigin, s.origin(r))
	tunnel := map[string]any{
		"mode":       os.Getenv("TUNNEL_MODE"),
		"public_url": hubPublicURL,
		"frp": map[string]any{
			"enabled":       truthyString(os.Getenv("FRP_ENABLE")),
			"domain":        os.Getenv("FRP_DOMAIN"),
			"subdomain":     os.Getenv("FRP_SUBDOMAIN"),
			"server_addr":   os.Getenv("FRP_SERVER_ADDR"),
			"server_port":   os.Getenv("FRP_SERVER_PORT"),
			"token_present": os.Getenv("FRP_TOKEN") != "",
		},
	}
	// Aggregate shell builds from heartbeat data.
	shellBuilds := map[string]any{
		"latest":   0,
		"oldest":   0,
		"versions": map[string]int{},
	}
	{
		buildCounts := map[string]int{}
		for _, srv := range servers {
			if meta, ok := srv["meta"].(map[string]any); ok {
				if bv, ok := meta["build_version"]; ok && bv != nil {
					ver := fmt.Sprintf("%v", bv)
					if f, ok := bv.(float64); ok {
						ver = fmt.Sprintf("%d", int(f))
					}
					if ver != "" && ver != "0" {
						buildCounts[ver]++
					}
				}
			}
		}
		versions := map[string]int{}
		latest := 0
		oldest := 0
		for ver, count := range buildCounts {
			versions[ver] = count
			v, _ := strconv.Atoi(ver)
			if v > latest {
				latest = v
			}
			if oldest == 0 || (v > 0 && v < oldest) {
				oldest = v
			}
		}
		shellBuilds["latest"] = latest
		shellBuilds["oldest"] = oldest
		shellBuilds["versions"] = versions
	}
	// Read update state.
	updateState := map[string]any{
		"current":     map[string]string{"status": "idle"},
		"last_result": nil,
	}
	if st, err := ReadUpdateState(s.updateStatePath); err == nil {
		st = EnsureDefaultUpdateState(st)
		updateState["current"] = map[string]string{"status": st.Current.Status}
		if st.LastResult != nil {
			updateState["last_result"] = st.LastResult
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "build": map[string]any{"name": "gptadmin-go-hub", "build_version": BuildVersion, "git_commit": GitCommit}, "now": time.Now().Unix(), "now_fmt": time.Now().Format("2006-01-02 15:04:05 MST"), "hub_public_url": hubPublicURL, "public_origin": s.cfg.PublicOrigin, "mcp_resource": s.resource(r), "tunnel": tunnel, "servers": servers, "server_counts": serverStatusCounts(servers), "shell_builds": shellBuilds, "update": updateState, "clients": clients, "client_count": len(clients), "clients_with_multiple_ips": []any{}, "jobs": jobs, "audit": audit, "state_files": map[string]any{"mode": "go-persistent", "registry_state": s.registryStatePath(), "mcp_token_state": s.managedMCPStatePath(), "failover_config": s.failoverConfigPath(), "failover_state": s.failoverStatePath()}, "failover_config": s.failover})
}

func (s *Server) adminTriggerUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}

	// Check if an update is already running.
	st, _ := ReadUpdateState(s.updateStatePath)
	st = EnsureDefaultUpdateState(st)
	if st.Current.Status == "running" || (s.updateLauncher != nil && s.updateLauncher.CheckUpdateRunning()) {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "update already running"})
		return
	}

	// Try to acquire lock.
	lock, err := AcquireUpdateLock(s.updateLockPath)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "update already running"})
		return
	}

	// Mark running in state file.
	st.Current.Status = "running"
	now := time.Now().Unix()
	if st.LastResult != nil {
		st.LastResult.StartedAt = now
	} else {
		st.LastResult = &UpdateResult{StartedAt: now}
	}
	if err := WriteUpdateState(s.updateStatePath, st); err != nil {
		ReleaseUpdateLock(lock)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to write state"})
		return
	}
	ReleaseUpdateLock(lock) // release — the external supervisor holds its own lifecycle

	// Launch via external supervisor.
	if s.updateLauncher == nil {
		st.Current.Status = "idle"
		st.LastResult = &UpdateResult{
			Status:     "error",
			Message:    "update launcher not initialized",
			StartedAt:  now,
			FinishedAt: time.Now().Unix(),
		}
		WriteUpdateState(s.updateStatePath, st)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "update launcher not configured"})
		return
	}
	if err := s.updateLauncher.LaunchUpdate(); err != nil {
		// Reset state on launch failure.
		st.Current.Status = "idle"
		st.LastResult = &UpdateResult{
			Status:     "error",
			Message:    err.Error(),
			StartedAt:  now,
			FinishedAt: time.Now().Unix(),
		}
		WriteUpdateState(s.updateStatePath, st)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to start update"})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "running"})
}

func (s *Server) adminMCPIssueToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req struct {
		ClientID   string `json:"client_id"`
		TTLDays    int    `json:"ttl_days"`
		AccessMode string `json:"access_mode"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		clientID = "custom-mcp-client"
	}
	ttlDays := req.TTLDays
	if ttlDays <= 0 {
		ttlDays = 365
	}
	origin := s.origin(r)
	resource := s.resource(r)
	accessMode := strings.ToLower(strings.TrimSpace(req.AccessMode))
	if accessMode == "" {
		accessMode = accessModeFull
	}
	if accessMode != accessModeFull && accessMode != accessModeReadonly {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "access_mode must be full or readonly"})
		return
	}
	token, record, err := s.issueManagedMCPTokenWithMode(clientID, ttlDays, origin, resource, accessMode)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"client_id":    clientID,
		"access_mode":  record.AccessMode,
		"token_id":     record.ID,
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   ttlDays * 24 * 3600,
		"issuer":       origin,
		"audience":     resource,
		"mcp_url":      origin + "/mcp",
	})
}

func (s *Server) issueManagedMCPToken(clientID string, ttlDays int, origin, resource string) (string, managedMCPToken, error) {
	return s.issueManagedMCPTokenWithMode(clientID, ttlDays, origin, resource, accessModeFull)
}

func (s *Server) issueManagedMCPTokenWithMode(clientID string, ttlDays int, origin, resource, accessMode string) (string, managedMCPToken, error) {
	now := time.Now().Unix()
	scope := "gptadmin.read gptadmin.exec"
	if accessMode == accessModeReadonly {
		scope = "gptadmin.read gptadmin.inspect"
	}
	record := managedMCPToken{ID: newID(), ClientID: clientID, Scope: scope, AccessMode: accessMode, IssuedAt: now, ExpiresAt: now + int64(ttlDays)*24*3600}
	token, err := s.signJWT(map[string]any{
		"sub": "admin", "scope": record.Scope, "access_mode": record.AccessMode, "client_id": clientID, "jti": record.ID,
		"iss": origin, "aud": resource, "resource": resource, "exp": record.ExpiresAt, "iat": now,
	})
	if err != nil {
		return "", managedMCPToken{}, err
	}
	s.mu.Lock()
	s.managedMCP[record.ID] = record
	err = s.saveManagedMCPStateLocked()
	s.mu.Unlock()
	return token, record, err
}

func (s *Server) adminMCPTokenAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/mcp/tokens/"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "rotate" || r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "not found"})
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid token id"})
		return
	}
	s.mu.Lock()
	record, ok := s.managedMCP[id]
	if ok && record.RevokedAt == 0 {
		record.RevokedAt = time.Now().Unix()
		s.managedMCP[id] = record
		err = s.saveManagedMCPStateLocked()
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "MCP token not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	remainingDays := int((record.ExpiresAt - time.Now().Unix()) / 86400)
	if remainingDays < 1 {
		remainingDays = 1
	}
	accessMode := record.AccessMode
	if accessMode == "" {
		accessMode = accessModeFull
	}
	token, replacement, err := s.issueManagedMCPTokenWithMode(record.ClientID, remainingDays, s.origin(r), s.resource(r), accessMode)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "replaced_token_id": id, "token_id": replacement.ID, "client_id": replacement.ClientID, "access_mode": replacement.AccessMode, "access_token": token, "token_type": "Bearer", "mcp_url": s.origin(r) + "/mcp"})
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

func (s *Server) adminRotateOAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if strings.TrimSpace(s.cfg.EnvFile) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "OAuth env file is not configured"})
		return
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to generate OAuth secret"})
		return
	}
	secret := hex.EncodeToString(secretBytes)
	if err := replaceEnvValue(s.cfg.EnvFile, "OAUTH_CLIENT_SECRET", secret); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist OAuth secret"})
		return
	}
	s.cfg.OAuthClientSecret = secret
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"restart_required": true,
		"message":          "OAuth secret rotated. Restart the Hub to load the persisted value for all workers.",
	})
}

func (s *Server) adminSecurityEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	data, err := os.ReadFile(s.cfg.EnvFile)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "env file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to read env metadata"})
		return
	}
	variables := make([]map[string]any, 0)
	heartbeat := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "SHELLMCP_HEARTBEAT" {
			heartbeat = truthyString(value)
		}
		variables = append(variables, map[string]any{
			"key":       key,
			"present":   value != "",
			"length":    len(value),
			"sensitive": sensitiveEnvKey(key),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"variables": variables, "shellmcp_heartbeat": heartbeat})
}

func sensitiveEnvKey(key string) bool {
	key = strings.ToUpper(key)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "BEARER", "API_KEY", "PRIVATE_KEY"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func replaceEnvValue(filename, key, value string) error {
	data, err := os.ReadFile(filename)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	prefix := key + "="
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = prefix + value
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, prefix+value)
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		return err
	}
	tmp := filename + ".tmp-" + newID()
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filename); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Server) adminClients(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	clients := s.managedMCPClientsLocked()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"clients": clients, "client_count": len(clients)})
}

func (s *Server) managedMCPClientsLocked() []managedMCPToken {
	clients := make([]managedMCPToken, 0, len(s.managedMCP)+1)
	for _, record := range s.managedMCP {
		clients = append(clients, record)
	}
	if s.cfg.CtlToken != "" {
		clients = append(clients, managedMCPToken{
			ID:         "legacy-ctl",
			ClientID:   "legacy-ctl",
			TokenKind:  "legacy_ctl",
			Scope:      "legacy transition credential",
			AccessMode: accessModeFull,
		})
	}
	return clients
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

const adminSessionCookieName = "gptadmin_admin_session"
const adminSessionTTL = 12 * time.Hour

func (s *Server) adminIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}
	if !s.adminSessionValid(r) {
		s.renderAdminLogin(w, r, "")
		return
	}
	http.Redirect(w, r, "/admin/index.html", http.StatusFound)
}

func (s *Server) adminStatic(w http.ResponseWriter, r *http.Request) {
	if !s.adminSessionValid(r) {
		if wantsHTML(r) || r.URL.Path == "/admin/" || r.URL.Path == "/admin/index.html" {
			s.renderAdminLogin(w, r, "")
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	root := filepath.Join(s.cfg.PublicDir, "admin")
	fs := http.StripPrefix("/admin/", http.FileServer(http.Dir(root)))
	fs.ServeHTTP(w, r)
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.adminSessionValid(r) {
			http.Redirect(w, r, safeAdminNext(r.FormValue("next")), http.StatusFound)
			return
		}
		s.renderAdminLogin(w, r, "")
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			s.renderAdminLogin(w, r, "bad request")
			return
		}
		password := r.FormValue("password")
		if s.cfg.AdminPassword == "" || !hmac.Equal([]byte(password), []byte(s.cfg.AdminPassword)) {
			s.authAudit("admin_login_denied", r, map[string]any{"reason": "bad_password"})
			s.renderAdminLogin(w, r, "неверный пароль")
			return
		}
		expires := time.Now().Add(adminSessionTTL)
		http.SetCookie(w, &http.Cookie{
			Name:     adminSessionCookieName,
			Value:    s.signAdminSession(expires),
			Path:     "/",
			Expires:  expires,
			MaxAge:   int(adminSessionTTL.Seconds()),
			HttpOnly: true,
			Secure:   isSecureRequest(r) || strings.HasPrefix(s.origin(r), "https://"),
			SameSite: http.SameSiteLaxMode,
		})
		s.authAudit("admin_login_ok", r, map[string]any{"auth_kind": "admin_password"})
		http.Redirect(w, r, safeAdminNext(r.FormValue("next")), http.StatusFound)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isSecureRequest(r) || strings.HasPrefix(s.origin(r), "https://"), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/admin/login", http.StatusFound)
}

func (s *Server) renderAdminLogin(w http.ResponseWriter, r *http.Request, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	next := safeAdminNext(r.URL.Query().Get("next"))
	if next == "/admin/login" || next == "/admin/logout" {
		next = "/admin/"
	}
	errHTML := ""
	if errMsg != "" {
		errHTML = `<div class="err">` + html.EscapeString(errMsg) + `</div>`
	}
	page := `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>GPTAdmin Login</title><style>
:root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(circle at 20% 0,#1d2b64 0,#090d18 36%,#05070c 100%);color:#e5eefc;font-family:Inter,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.card{width:min(460px,calc(100vw - 32px));padding:30px;border:1px solid rgba(148,163,184,.24);border-radius:26px;background:rgba(15,23,42,.86);box-shadow:0 24px 80px rgba(0,0,0,.42);backdrop-filter:blur(16px)}h1{margin:0 0 8px;font-size:28px}.muted{margin:0 0 22px;color:#94a3b8;line-height:1.45}.hint{margin:0 0 18px;padding:12px 14px;border-radius:16px;background:rgba(56,189,248,.08);border:1px solid rgba(56,189,248,.18);color:#cbd5e1;line-height:1.45}.hint code{color:#fff}.err{margin:0 0 14px;padding:10px 12px;border-radius:14px;background:rgba(239,68,68,.14);border:1px solid rgba(239,68,68,.35);color:#fecaca}label{display:block;margin-bottom:8px;color:#cbd5e1;font-size:14px}input,button{width:100%;padding:14px 15px;border-radius:16px;font-size:16px}input{border:1px solid #334155;background:#0b1220;color:#fff;outline:none}input:focus{border-color:#38bdf8;box-shadow:0 0 0 3px rgba(56,189,248,.16)}button{margin-top:14px;border:0;background:linear-gradient(135deg,#7c3aed,#06b6d4);color:white;font-weight:800;cursor:pointer}.foot{margin-top:16px;color:#64748b;font-size:12px;text-align:center}</style></head><body><main class="card"><h1>GPTAdmin</h1><p class="muted">Введите admin-пароль. Без cookie-сессии админка и её API не отдаются.</p><div class="hint">Для браузерной админки нужен <strong>admin-пароль</strong>. Для Custom GPT / generated Action schema используйте <code>Authorization: Bearer &lt;CTL_TOKEN&gt;</code> или Bearer JWT, выпущенный через OAuth.</div>` + errHTML + `<form method="post" action="/admin/login"><input type="hidden" name="next" value="` + html.EscapeString(next) + `"><label for="password">Пароль</label><input id="password" name="password" type="password" autocomplete="current-password" autofocus required><button type="submit">Войти</button></form><div class="foot">session cookie · 12h</div></main></body></html>`
	_, _ = io.WriteString(w, page)
}

func (s *Server) adminSessionValid(r *http.Request) bool {
	if s.cfg.AdminPassword == "" {
		return true
	}
	c, err := r.Cookie(adminSessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || exp < time.Now().Unix() {
		return false
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want := s.adminSessionMAC(parts[0])
	return hmac.Equal(mac, want)
}

func (s *Server) signAdminSession(expires time.Time) string {
	payload := strconv.FormatInt(expires.Unix(), 10)
	return payload + "." + base64.RawURLEncoding.EncodeToString(s.adminSessionMAC(payload))
}

func (s *Server) adminSessionMAC(payload string) []byte {
	secret := firstNonEmpty(s.cfg.OAuthClientSecret, s.cfg.AdminPassword, s.cfg.CtlToken, "gptadmin-admin-session")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("admin-session:" + payload))
	return mac.Sum(nil)
}

func wantsHTML(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/html")
}

func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") || strings.EqualFold(r.Header.Get("X-Forwarded-Ssl"), "on")
}

func safeAdminNext(v string) string {
	if v == "" {
		return "/admin/"
	}
	if !strings.HasPrefix(v, "/admin/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/admin/api/") {
		return "/admin/"
	}
	return v
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

func intFromString(v string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n
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
		"scopes_supported":       []string{"gptadmin.read", "gptadmin.inspect", "gptadmin.exec"},
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
		"scopes_supported":                      []string{"gptadmin.read", "gptadmin.inspect", "gptadmin.exec"},
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
	page := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize GPTAdmin MCP</title><style>body{font-family:system-ui,sans-serif;background:#070a12;color:#e5eefc;display:grid;place-items:center;min-height:100vh;margin:0}.card{max-width:560px;padding:28px;border:1px solid #1e293b;border-radius:24px;background:#0f1623}.hint{margin:16px 0;padding:12px 14px;border-radius:16px;background:rgba(56,189,248,.08);border:1px solid rgba(56,189,248,.18);color:#cbd5e1;line-height:1.45}.hint code{color:#fff}input,button{width:100%;box-sizing:border-box;padding:14px;border-radius:14px;margin-top:10px}input{background:#111827;color:#fff;border:1px solid #334155}button{border:0;background:linear-gradient(135deg,#7c3aed,#06b6d4);color:#fff;font-weight:800}.muted{color:#94a3b8;word-break:break-all}</style></head><body><main class="card"><h1>Authorize GPTAdmin MCP</h1><p class="muted">Client: ` + html.EscapeString(q.Get("client_id")) + `</p><p class="muted">Resource: ` + html.EscapeString(resource) + `</p><p>Scopes: ` + html.EscapeString(scope) + `</p><div class="hint">Эта страница выпускает Bearer JWT для MCP/Custom GPT. Если у вас уже есть <code>CTL_TOKEN</code> или готовый Bearer JWT, можете использовать его напрямую в generated Action schema без повторной авторизации.</div><form method="POST" action="/authorize">` + hidden + `<label>Admin password</label><input type="password" name="password" autofocus required autocomplete="current-password"><button type="submit">Authorize</button></form></main></body></html>`
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
	if tail != "" && tail != "mcp" && tail != "card" && tail != "health" && !strings.HasPrefix(tail, "actions/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown server endpoint", "endpoint": tail})
		return
	}
	if tail == "actions/openapi.yaml" || tail == "actions/openapi.yml" || tail == "actions/openapi.json" {
		s.serverActionsOpenAPI(w, r, agent)
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
	if strings.HasPrefix(tail, "actions/tools/") {
		s.serverActionToolCall(w, r, agent, tail)
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

func (s *Server) serverActionsOpenAPI(w http.ResponseWriter, r *http.Request, agent Agent) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	result, rpcErr := s.agentToolsList(agent)
	if rpcErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "failed to list MCP server tools", "server_id": agent.AgentID, "error": rpcErr})
		return
	}
	tools := mcpToolsFromResult(result)
	slug := agentSlug(agent.AgentID)
	if slug == "" {
		slug = agentSlug(agent.Name)
	}
	body := buildServerActionsOpenAPI(s.origin(r), slug, agent, tools)
	ct := "application/yaml; charset=utf-8"
	if strings.HasSuffix(r.URL.Path, ".json") {
		ct = "application/json; charset=utf-8"
		body = buildServerActionsOpenAPIJSON(s.origin(r), slug, agent, tools)
	}
	b := []byte(body)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) serverActionToolCall(w http.ResponseWriter, r *http.Request, agent Agent, tail string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	toolName, _ := url.PathUnescape(strings.TrimPrefix(tail, "actions/tools/"))
	toolName = strings.Trim(toolName, "/")
	if toolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing tool name"})
		return
	}
	args := map[string]any{}
	if r.Body != nil && r.ContentLength != 0 {
		if err := readJSON(r, &args); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
	}
	var authErr error
	if agent.AgentID == "hub" {
		authErr = authorizeFacadeCall(r, toolName, args)
	} else {
		authErr = authorizeToolCall(r, agent.AgentID, toolName)
	}
	if authErr != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": authErr.Error()})
		return
	}
	result, rpcErr := s.agentToolCall(agent, toolName, args)
	if rpcErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"server_id": agent.AgentID, "tool_name": toolName, "status": "failed", "error": rpcErr})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_id": agent.AgentID, "tool_name": toolName, "status": "completed", "response": result})
}

func mcpToolsFromResult(v any) []map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := m["tools"]
	if !ok {
		return nil
	}
	switch items := raw.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			out = append(out, item)
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if tool, ok := item.(map[string]any); ok {
				out = append(out, tool)
			}
		}
		return out
	default:
		return nil
	}
}

func buildServerActionsOpenAPI(origin, slug string, agent Agent, tools []map[string]any) string {
	var b strings.Builder
	serverPath := "/server/" + slug + "/actions/tools/"
	b.WriteString("openapi: 3.1.0\n")
	b.WriteString("info:\n")
	b.WriteString("  title: " + yamlQuote("GPTAdmin proxy for "+agent.Name) + "\n")
	b.WriteString("  version: \"1.0.0\"\n")
	b.WriteString("  description: " + yamlQuote("OpenAPI action proxy generated from MCP tools/list for one GPTAdmin MCP server: "+agent.AgentID) + "\n")
	b.WriteString("servers:\n")
	b.WriteString("  - url: " + yamlQuote(origin) + "\n")
	b.WriteString("security:\n")
	b.WriteString("  - bearerAuth: []\n")
	b.WriteString("paths:\n")
	if len(tools) == 0 {
		b.WriteString("  {}\n")
	} else {
		used := map[string]int{}
		for _, tool := range tools {
			name := firstString(tool, "name")
			if name == "" {
				continue
			}
			opID := openAPIActionOperationID(name, used)
			desc := firstString(tool, "description", "title")
			if desc == "" {
				desc = "Call MCP tool " + name
			}
			schema := openAPIActionInputSchema(tool)
			b.WriteString("  " + yamlQuote(serverPath+url.PathEscape(name)) + ":\n")
			b.WriteString("    post:\n")
			b.WriteString("      operationId: " + yamlQuote(opID) + "\n")
			b.WriteString("      summary: " + yamlQuote(name) + "\n")
			b.WriteString("      description: " + yamlQuote(desc) + "\n")
			b.WriteString("      requestBody:\n")
			b.WriteString("        required: true\n")
			b.WriteString("        content:\n")
			b.WriteString("          application/json:\n")
			b.WriteString("            schema: " + compactJSON(schema) + "\n")
			b.WriteString("      responses:\n")
			b.WriteString("        \"200\":\n")
			b.WriteString("          description: MCP tool result\n")
			b.WriteString("          content:\n")
			b.WriteString("            application/json:\n")
			b.WriteString("              schema: {\"type\":\"object\",\"additionalProperties\":true}\n")
		}
	}
	b.WriteString("components:\n")
	b.WriteString("  securitySchemes:\n")
	b.WriteString("    bearerAuth:\n")
	b.WriteString("      type: http\n")
	b.WriteString("      scheme: bearer\n")
	return b.String()
}

func buildServerActionsOpenAPIJSON(origin, slug string, agent Agent, tools []map[string]any) string {
	paths := map[string]any{}
	used := map[string]int{}
	for _, tool := range tools {
		name := firstString(tool, "name")
		if name == "" {
			continue
		}
		desc := firstString(tool, "description", "title")
		if desc == "" {
			desc = "Call MCP tool " + name
		}
		paths["/server/"+slug+"/actions/tools/"+url.PathEscape(name)] = map[string]any{"post": map[string]any{
			"operationId": openAPIActionOperationID(name, used),
			"summary":     name,
			"description": desc,
			"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": openAPIActionInputSchema(tool)}}},
			"responses":   map[string]any{"200": map[string]any{"description": "MCP tool result", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": true}}}}},
		}}
	}
	payload := map[string]any{
		"openapi":    "3.1.0",
		"info":       map[string]any{"title": "GPTAdmin proxy for " + agent.Name, "version": "1.0.0", "description": "OpenAPI action proxy generated from MCP tools/list for one GPTAdmin MCP server: " + agent.AgentID},
		"servers":    []map[string]any{{"url": origin}},
		"security":   []map[string]any{{"bearerAuth": []any{}}},
		"paths":      paths,
		"components": map[string]any{"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"}}},
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(b) + "\n"
}

func openAPIActionInputSchema(tool map[string]any) map[string]any {
	if schema, ok := tool["inputSchema"].(map[string]any); ok && schema != nil {
		return schema
	}
	if schema, ok := tool["input_schema"].(map[string]any); ok && schema != nil {
		return schema
	}
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
}

func openAPIActionOperationID(name string, used map[string]int) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" {
		out = "call_tool"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "call_" + out
	}
	used[out]++
	if used[out] > 1 {
		return out + "_" + strconv.Itoa(used[out])
	}
	return out
}

func yamlQuote(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"type":"object","additionalProperties":true}`
	}
	return string(b)
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
		result, err := s.agentToolsListForRequest(r, agent)
		return result, err, false
	case "tools/call":
		name := firstString(params, "name")
		args := mapValue(params["arguments"])
		if name == "" {
			return nil, map[string]any{"code": -32602, "message": "tool name is required"}, false
		}
		var authErr error
		if agent.AgentID == "hub" {
			authErr = authorizeFacadeCall(r, name, args)
		} else {
			authErr = authorizeToolCall(r, agent.AgentID, name)
		}
		if authErr != nil {
			return nil, map[string]any{"code": -32003, "message": authErr.Error()}, false
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

func (s *Server) agentToolsListForRequest(r *http.Request, agent Agent) (any, any) {
	if requestAccessMode(r) != accessModeReadonly {
		return s.agentToolsList(agent)
	}
	if agent.AgentID == "hub" {
		return map[string]any{"tools": appsSDKToolsForRequest(r)}, nil
	}
	if strings.HasPrefix(agent.AgentID, "shell:") {
		return map[string]any{"tools": toolsForRequest(r, agent.AgentID, shellTools())}, nil
	}
	return map[string]any{"tools": []map[string]any{}}, nil
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
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": "GPTAdmin MCP", "tools": appsSDKToolsForRequest(r)})
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
		result = map[string]any{"tools": appsSDKToolsForRequest(r)}
	case "tools/call":
		name := firstString(params, "name")
		args := mapValue(params["arguments"])
		if name == "" {
			rpcErr = map[string]any{"code": -32602, "message": "tool name is required"}
		} else if err := authorizeFacadeCall(r, name, args); err != nil {
			rpcErr = map[string]any{"code": -32003, "message": err.Error()}
		} else {
			result = mcpToolResult(s.appsSDKCallForRequest(r, name, args))
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
	case "ui", "render_gptadmin_dashboard", "renderGptadminDashboard":
		s.mu.Lock()
		servers := s.publicServersLocked(nil)
		s.mu.Unlock()
		return map[string]any{
			"status":       "ready",
			"app":          "GPTAdmin MCP",
			"server_count": len(servers),
			"servers":      servers,
			"hint":         "Interactive dashboard rendered. The widget can call discover, schema, execute, and job through the MCP Apps bridge.",
		}
	case "discover", "list_mcp_servers", "listMcpServers":
		s.mu.Lock()
		servers := s.publicServersLockedWithDetail(nil, fullDetailRequested(args["detail"]))
		s.mu.Unlock()
		return map[string]any{"servers": servers}
	case "list_mcp_agents", "listMcpAgents":
		s.mu.Lock()
		agents := s.publicAgentsLocked(nil)
		s.mu.Unlock()
		return map[string]any{"agents": agents}
	case "schema", "list_mcp_tools", "listMcpTools":
		target := firstString(args, "target", "server_id", "agent_id")
		selectedTarget, status, detail := s.selectMCPRelayTarget(target)
		if status != http.StatusOK {
			return map[string]any{"server_id": target, "status": "failed", "error": map[string]any{"status_code": status, "message": detail}}
		}
		target = selectedTarget
		if target == "hub" {
			return map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": hubTools()}}
		}
		if strings.HasPrefix(target, "shell:") {
			return map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": shellTools()}}
		}
		jobID := s.enqueueRelay(target, "tools/list", map[string]any{})
		return s.waitRelay(jobID, s.cfg.DefaultTimeout)
	case "inspect", "inspect_system", "inspectSystem":
		target := firstString(args, "target", "server_id", "agent_id")
		selectedTarget, status, detail := s.selectMCPRelayTarget(target)
		if status != http.StatusOK {
			return map[string]any{"server_id": target, "status": "failed", "error": map[string]any{"status_code": status, "message": detail}}
		}
		if !strings.HasPrefix(selectedTarget, "shell:") {
			return map[string]any{"server_id": selectedTarget, "status": "failed", "error": "system inspection requires a shell:* target"}
		}
		return s.callShellTool(selectedTarget, "system_inspect", toolArgsFromTopLevel(args), false, s.cfg.DefaultTimeout)
	case "execute", "call_mcp_tool", "callMcpTool":
		return s.appsSDKCallMCP(nil, name, args)
	case "job", "get_mcp_job", "getMcpJob":
		jobID := firstString(args, "id", "job_id")
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

func (s *Server) appsSDKCallForRequest(r *http.Request, name string, args map[string]any) any {
	if requestAccessMode(r) == accessModeReadonly && (name == "schema" || name == "list_mcp_tools" || name == "listMcpTools") {
		target := firstString(args, "target", "server_id", "agent_id")
		if target != "hub" && !strings.HasPrefix(target, "shell:") {
			return map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": []map[string]any{}}}
		}
	}
	if name == "call_mcp_tool" || name == "callMcpTool" {
		return s.appsSDKCallMCP(r, name, args)
	}
	result := s.appsSDKCall(name, args)
	if requestAccessMode(r) != accessModeReadonly || (name != "schema" && name != "list_mcp_tools" && name != "listMcpTools") {
		return result
	}
	payload, ok := result.(map[string]any)
	if !ok {
		return result
	}
	target := firstString(args, "target", "server_id", "agent_id")
	response := mapValue(payload["response"])
	if raw, ok := response["tools"].([]map[string]any); ok {
		response["tools"] = toolsForRequest(r, target, raw)
	}
	return payload
}

func (s *Server) appsSDKCallMCP(r *http.Request, name string, args map[string]any) any {
	target := firstString(args, "target", "server_id", "agent_id")
	toolName := firstString(args, "tool", "tool_name", "name")
	if toolName == "" {
		return map[string]any{"server_id": target, "status": "failed", "error": "missing tool_name"}
	}
	callArgs := mapValue(args["arguments"])
	if len(callArgs) == 0 {
		callArgs = mapValue(args["args"])
	}
	if len(callArgs) == 0 {
		callArgs = toolArgsFromTopLevel(args)
	}
	selectedTarget, status, detail := s.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		return map[string]any{"server_id": target, "status": "failed", "error": map[string]any{"status_code": status, "message": detail}}
	}
	response, status := s.executeMCPTool(r, selectedTarget, toolName, callArgs, truthy(args["background"]), s.cfg.DefaultTimeout, firstString(args, "idempotency_key"))
	if status >= http.StatusBadRequest {
		return map[string]any{"server_id": selectedTarget, "status": "failed", "error": response}
	}
	return response
}

func appsSDKTools() []map[string]any {
	readScopes := []string{"gptadmin.read"}
	execScopes := []string{"gptadmin.read", "gptadmin.exec"}
	readSecurity := []map[string]any{{"type": "oauth2", "scopes": readScopes}}
	execSecurity := []map[string]any{{"type": "oauth2", "scopes": execScopes}}
	readMeta := map[string]any{
		"securitySchemes":                readSecurity,
		"openai/toolInvocation/invoking": "Loading…",
		"openai/toolInvocation/invoked":  "Loaded.",
	}
	execMeta := map[string]any{
		"securitySchemes":                execSecurity,
		"openai/toolInvocation/invoking": "Running…",
		"openai/toolInvocation/invoked":  "Done.",
	}
	renderMeta := map[string]any{
		"securitySchemes":                readSecurity,
		"ui":                             map[string]any{"resourceUri": "ui://widget/admin-v3.html", "visibility": []string{"model", "app"}},
		"openai/outputTemplate":          "ui://widget/admin-v3.html",
		"openai/widgetAccessible":        true,
		"openai/toolInvocation/invoking": "Opening GPTAdmin…",
		"openai/toolInvocation/invoked":  "GPTAdmin ready.",
	}
	return []map[string]any{
		{
			"name":            "ui",
			"title":           "Open UI",
			"description":     "Open the GPTAdmin UI when interactive server or tool selection is needed.",
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			"outputSchema":    map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string"}, "app": map[string]any{"type": "string"}, "server_count": map[string]any{"type": "integer"}, "servers": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}}, "hint": map[string]any{"type": "string"}}, "required": []string{"status", "app"}, "additionalProperties": true},
			"annotations":     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
			"securitySchemes": readSecurity,
			"_meta":           renderMeta,
		},
		{
			"name":            "discover",
			"title":           "Discover",
			"description":     "List compact MCP targets. Set detail=full only when metadata is needed.",
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{"detail": map[string]any{"type": "string", "enum": []string{"full"}, "description": "Opt in to transport, capabilities and metadata."}}, "additionalProperties": false},
			"outputSchema":    map[string]any{"type": "object", "properties": map[string]any{"servers": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}}}, "required": []string{"servers"}, "additionalProperties": true},
			"annotations":     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
			"securitySchemes": readSecurity,
			"_meta":           readMeta,
		},
		{
			"name":            "schema",
			"title":           "Schema",
			"description":     "List tools for one target selected by discover. Never use target=default.",
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string"}}, "required": []string{"target"}, "additionalProperties": false},
			"outputSchema":    map[string]any{"type": "object", "properties": map[string]any{"server_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "response": map[string]any{"type": "object", "additionalProperties": true}}, "additionalProperties": true},
			"annotations":     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
			"securitySchemes": readSecurity,
			"_meta":           readMeta,
		},
		{
			"name":        "inspect",
			"title":       "Inspect",
			"description": "Read a bounded file or directory on a shell target; no command execution.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"target":    map[string]any{"type": "string", "description": "Explicit shell:* server id"},
				"action":    map[string]any{"type": "string", "enum": []string{"read_file", "list_directory"}},
				"path":      map[string]any{"type": "string"},
				"max_bytes": map[string]any{"type": []string{"integer", "null"}, "minimum": 1, "maximum": 1048576},
			}, "required": []string{"target", "action", "path"}, "additionalProperties": false},
			"outputSchema":    map[string]any{"type": "object", "additionalProperties": true},
			"annotations":     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
			"securitySchemes": readSecurity,
			"_meta":           readMeta,
		},
		{
			"name":            "execute",
			"title":           "Execute",
			"description":     "Execute one tool on one target. Use schema first. Retry the same operation with the same idempotency_key.",
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string"}, "tool": map[string]any{"type": "string"}, "args": map[string]any{"type": "object", "additionalProperties": true}, "background": map[string]any{"type": "boolean"}, "idempotency_key": map[string]any{"type": "string", "minLength": 1, "maxLength": idempotencyKeyMax}}, "required": []string{"target", "tool"}, "additionalProperties": true},
			"outputSchema":    map[string]any{"type": "object", "additionalProperties": true},
			"annotations":     map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": true},
			"securitySchemes": execSecurity,
			"_meta":           execMeta,
		},
		{
			"name":            "job",
			"title":           "Job",
			"description":     "Read a background job by id.",
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "ack": map[string]any{"type": "boolean"}}, "required": []string{"id"}, "additionalProperties": false},
			"outputSchema":    map[string]any{"type": "object", "additionalProperties": true},
			"annotations":     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
			"securitySchemes": readSecurity,
			"_meta":           readMeta,
		},
	}
}

func appsSDKResourcesList() map[string]any {
	return map[string]any{"resources": []map[string]any{
		{
			"uri":         "ui://widget/admin-v3.html",
			"name":        "GPTAdmin dashboard widget",
			"title":       "GPTAdmin MCP",
			"description": "Interactive GPTAdmin dashboard for servers, tools, shell commands, and jobs.",
			"mimeType":    "text/html;profile=mcp-app",
			"_meta":       appsSDKWidgetMeta(),
		},
		{"uri": "gptadmin://servers", "name": "GPTAdmin servers", "mimeType": "application/json"},
	}}
}

func appsSDKWidgetMeta() map[string]any {
	connectDomains := []string{"https://gptadmin.bezrabotnyi.com", "https://gptadminmcp.bezrabotnyi.com", "https://u-f1102930.t.gptadmin.bezrabotnyi.com"}
	resourceDomains := []string{"https://u-f1102930.t.gptadmin.bezrabotnyi.com", "https://gptadmin.bezrabotnyi.com", "https://persistent.oaistatic.com"}
	return map[string]any{
		"ui": map[string]any{
			"domain":        "https://u-f1102930.t.gptadmin.bezrabotnyi.com",
			"prefersBorder": true,
			"csp":           map[string]any{"connectDomains": connectDomains, "resourceDomains": resourceDomains},
		},
		"openai/widgetDescription":   "Interactive GPTAdmin dashboard for selecting MCP servers, listing tools, running calls, and polling jobs.",
		"openai/widgetPrefersBorder": true,
		"openai/widgetDomain":        "https://u-f1102930.t.gptadmin.bezrabotnyi.com",
		"openai/widgetCSP":           map[string]any{"connect_domains": connectDomains, "resource_domains": resourceDomains, "redirect_domains": []string{"https://gptadmin.bezrabotnyi.com", "https://u-f1102930.t.gptadmin.bezrabotnyi.com"}},
	}
}

func (s *Server) appsSDKResourceRead(r *http.Request, uri string) map[string]any {
	if uri == "gptadmin://servers" || uri == "gptadmin://agents" {
		s.mu.Lock()
		servers := s.publicServersLocked(nil)
		s.mu.Unlock()
		b, _ := json.Marshal(map[string]any{"servers": servers})
		return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(b)}}}
	}
	if uri != "ui://widget/admin-v3.html" {
		return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "text/plain", "text": "unknown GPTAdmin resource"}}}
	}
	return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "text/html;profile=mcp-app", "text": appsSDKWidgetHTML(s.origin(r)), "_meta": appsSDKWidgetMeta()}}}
}

func appsSDKWidgetHTML(origin string) string {
	return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>GPTAdmin MCP</title>
<style>
:root{color-scheme:dark;--bg:#070a12;--card:#0f1623;--card2:#111827;--line:#243244;--text:#dbeafe;--muted:#94a3b8;--ok:#22c55e;--warn:#f59e0b;--bad:#fb7185;--accent:#8b5cf6;--accent2:#38bdf8}
*{box-sizing:border-box}body{margin:0;background:linear-gradient(135deg,#050711,#0b1020);color:var(--text);font:13px/1.45 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;padding:10px}.wrap{display:grid;gap:10px}.card{background:rgba(15,22,35,.95);border:1px solid var(--line);border-radius:14px;padding:12px;box-shadow:0 10px 30px rgba(0,0,0,.22)}.top{display:flex;align-items:center;gap:10px}.logo{font-weight:800;color:#fff;font-size:16px}.pill{font-size:11px;color:#c4b5fd;border:1px solid #4c1d95;background:#2e1065;border-radius:999px;padding:2px 8px}.muted{color:var(--muted)}.grid{display:grid;grid-template-columns:minmax(210px,.9fr) minmax(260px,1.1fr);gap:10px}@media(max-width:720px){.grid{grid-template-columns:1fr}}button,input,select,textarea{font:inherit}button{border:1px solid var(--line);background:#172033;color:var(--text);border-radius:9px;padding:7px 10px;cursor:pointer}button:hover{border-color:var(--accent2);color:white}button.primary{background:linear-gradient(135deg,#4f46e5,#7c3aed);border-color:#6d5dfc}.row{display:flex;gap:7px;align-items:center;flex-wrap:wrap}.stack{display:grid;gap:8px}.list{display:grid;gap:6px;max-height:310px;overflow:auto}.item{border:1px solid var(--line);background:var(--card2);border-radius:10px;padding:8px;cursor:pointer}.item:hover,.item.sel{border-color:var(--accent2)}.title{font-weight:700}.small{font-size:11px}.ok{color:var(--ok)}.bad{color:var(--bad)}.warn{color:var(--warn)}input,select,textarea{width:100%;border:1px solid var(--line);background:#08111f;color:var(--text);border-radius:9px;padding:8px}textarea{min-height:96px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;resize:vertical}.pre{white-space:pre-wrap;word-break:break-word;font:12px/1.35 ui-monospace,SFMono-Regular,Menlo,monospace;color:#bfdbfe;background:#06101e;border:1px solid var(--line);border-radius:10px;padding:9px;max-height:260px;overflow:auto}.kv{display:grid;grid-template-columns:auto 1fr;gap:3px 8px}.dot{width:8px;height:8px;border-radius:99px;background:var(--ok);display:inline-block}.dot.w{background:var(--warn)}.dot.b{background:var(--bad)}
</style>
</head>
<body>
<div class="wrap">
  <div class="card top"><span id="dot" class="dot"></span><div><div class="logo">GPTAdmin MCP</div><div id="status" class="muted small">loading...</div></div><span class="pill">Apps SDK</span><button id="refresh" style="margin-left:auto">Refresh</button></div>
  <div class="grid">
    <div class="card stack"><div class="row"><button id="serversBtn" class="primary">Servers</button></div><input id="filter" placeholder="filter servers..."><div id="servers" class="list"></div></div>
    <div class="card stack"><div class="kv small"><div class="muted">Target</div><div id="target">—</div><div class="muted">Tools</div><div id="toolCount">—</div></div><select id="tool"></select><textarea id="args" spellcheck="false">{}</textarea><div class="row"><button id="listTools">List tools</button><button id="call" class="primary">Call tool</button><button id="poll" style="display:none">Poll job</button></div><div id="out" class="pre">Waiting for tool result...</div></div>
  </div>
</div>
<script>
(function(){
var ORIGIN='` + html.EscapeString(origin) + `';
var state={servers:[],target:'',tools:[],job_id:''};
var seq=1,pending={};
function el(id){return document.getElementById(id)}
function setStatus(t,k){el('status').textContent=t;el('dot').className='dot'+(k==='w'?' w':k==='b'?' b':'')}
function redact(v){var re=/(token|secret|password|authorization|bearer|api[_-]?key|jwt)/i;if(Array.isArray(v))return v.map(redact);if(v&&typeof v==='object'){var o={};Object.keys(v).forEach(function(k){o[k]=re.test(k)?'***':redact(v[k])});return o}if(typeof v==='string'&&/Bearer\s+/.test(v))return v.replace(/Bearer\s+\S+/g,'Bearer ***');return v}
function pretty(v){try{return JSON.stringify(redact(v),null,2)}catch(e){return String(v)}}
function show(v){el('out').textContent=pretty(v);var j=v&&((v.structuredContent&&v.structuredContent.job_id)||v.job_id||(v.response&&v.response.job_id));if(j){state.job_id=j;el('poll').style.display='inline-block'}}
function resize(){try{if(window.openai&&window.openai.notifyIntrinsicHeight)window.openai.notifyIntrinsicHeight(Math.min(document.body.scrollHeight+8,760))}catch(e){}}
function rpc(method,params){return new Promise(function(resolve,reject){var id=seq++;pending[id]={resolve:resolve,reject:reject};window.parent.postMessage({jsonrpc:'2.0',id:id,method:method,params:params||{}},'*');setTimeout(function(){if(pending[id]){delete pending[id];reject(new Error('MCP Apps bridge timeout'))}},30000)})}
window.addEventListener('message',function(event){if(event.source!==window.parent)return;var m=event.data||{};if(m.id&&pending[m.id]){var p=pending[m.id];delete pending[m.id];if(m.error)p.reject(m.error);else p.resolve(m.result);return}if(m.jsonrpc==='2.0'&&m.method==='ui/notifications/tool-result'){show(m.params||{});setStatus('tool result','');resize()}if(m.jsonrpc==='2.0'&&m.method==='ui/notifications/tool-input'){setStatus('tool input','w')}});
async function callTool(name,args){setStatus('calling '+name+'...','w');var r;if(window.openai&&window.openai.callTool){r=await window.openai.callTool(name,args||{})}else{r=await rpc('tools/call',{name:name,arguments:args||{}})}var sc=(r&&r.structuredContent)||r;show(sc);setStatus('ready','');resize();return sc}
function normalizeResult(r,key){if(!r)return[];if(r[key])return r[key];if(r.structuredContent&&r.structuredContent[key])return r.structuredContent[key];if(r.response&&r.response[key])return r.response[key];return[]}
function renderServers(items){var q=el('filter').value.toLowerCase();var box=el('servers');box.innerHTML='';items.filter(function(a){return !q||pretty(a).toLowerCase().indexOf(q)>=0}).forEach(function(a){var id=a.agent_id||a.server_id||a.id||'';var d=document.createElement('div');d.className='item'+(id===state.target?' sel':'');d.innerHTML='<div class="title">'+id.replace(/&/g,'&amp;').replace(/</g,'&lt;')+'</div><div class="small muted">'+(a.status||'')+' · '+(a.kind||'')+' · '+(a.name||'')+'</div>';d.onclick=function(){state.target=id;el('target').textContent=id;renderServers(items);listTools()};box.appendChild(d)});resize()}
async function loadServers(){var r=await callTool('discover',{});state.servers=normalizeResult(r,'servers');renderServers(state.servers);return state.servers}
async function listTools(){if(!state.target){setStatus('choose target','w');return}var r=await callTool('schema',{target:state.target});var tools=normalizeResult(r.response||r,'tools');state.tools=tools;el('toolCount').textContent=String(tools.length);var sel=el('tool');sel.innerHTML='';tools.forEach(function(t){var o=document.createElement('option');o.value=t.name;o.textContent=t.name+(t.description?' — '+t.description.slice(0,80):'');sel.appendChild(o)});if(tools.length){sel.value=tools[0].name;fillArgs()}resize()}
function fillArgs(){var name=el('tool').value;if(name==='shell_exec')el('args').value=JSON.stringify({cmd:'pwd',cwd:null,timeout:30},null,2);else el('args').value='{}'}
async function callSelected(){if(!state.target){setStatus('choose target','w');return}var args={};try{args=JSON.parse(el('args').value||'{}')}catch(e){setStatus('bad JSON: '+e.message,'b');return}await callTool('execute',{target:state.target,tool:el('tool').value,args:args})}
async function poll(){if(!state.job_id){setStatus('no job','w');return}await callTool('job',{id:state.job_id,ack:false})}
el('refresh').onclick=loadServers;el('serversBtn').onclick=loadServers;el('filter').oninput=function(){renderServers(state.servers)};el('listTools').onclick=listTools;el('call').onclick=callSelected;el('poll').onclick=poll;el('tool').onchange=fillArgs;
try{setStatus('ready at '+ORIGIN,'');var initial=(window.openai&&(window.openai.toolOutput||window.openai.toolResponseMetadata));if(initial)show(initial);loadServers().catch(function(e){setStatus(String(e.message||e),'b');show({error:String(e.message||e)})})}catch(e){setStatus(String(e),'b');show({error:String(e)})}
})();
</script>
</body>
</html>`
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
		"raw_query":       queryForAudit(r.URL.RawQuery),
		"host":            r.Host,
		"remote_addr":     r.RemoteAddr,
		"x_forwarded_for": r.Header.Get("X-Forwarded-For"),
		"x_real_ip":       r.Header.Get("X-Real-IP"),
		"user_agent":      r.UserAgent(),
		"referer":         r.Referer(),
		"origin":          r.Header.Get("Origin"),
		"content_type":    r.Header.Get("Content-Type"),
	}
	fields["authorization"] = redactSecret(r.Header.Get("Authorization"))
	if r.Header.Get("Cookie") != "" {
		fields["cookie"] = "<redacted>"
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
		if isSensitiveField(k) {
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
	return redactSecret(v)
}

func isSensitiveField(k string) bool {
	k = strings.ToLower(k)
	return strings.Contains(k, "secret") || strings.Contains(k, "password") || strings.Contains(k, "token") || strings.Contains(k, "code") || strings.Contains(k, "verifier")
}

func queryForAudit(raw string) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "<unparseable>"
	}
	for key, entries := range values {
		if isSensitiveField(key) {
			for i := range entries {
				entries[i] = redactSecret(entries[i])
			}
			values[key] = entries
		}
	}
	return values.Encode()
}

func redactSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	return "<redacted len=" + strconv.Itoa(len(v)) + ">"
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
			*r = *requestWithAuthClaims(r, claims)
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

func (s *Server) writeCtlUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.origin(r)+`/.well-known/oauth-protected-resource", scope="gptadmin.read gptadmin.exec"`)
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"detail": "unauthorized",
		"auth": map[string]any{
			"admin_login":          s.origin(r) + "/admin/login",
			"oauth_authorize":      s.origin(r) + "/authorize",
			"oauth_resource_meta":  s.origin(r) + "/.well-known/oauth-protected-resource",
			"accepts_bearer_token": true,
		},
	})
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
	if s.cfg.OAuthClientSecret == "" {
		return "", errors.New("OAuth client secret is not configured")
	}
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
	if s.cfg.OAuthClientSecret == "" {
		return nil, errors.New("OAuth client secret is not configured")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid jwt")
	}
	var header struct {
		Alg string `json:"alg"`
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerBytes, &header) != nil || header.Alg != "HS256" {
		return nil, errors.New("unsupported jwt algorithm")
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
	exp := intFromAny(claims["exp"])
	if exp <= 0 {
		return nil, errors.New("token expiry is required")
	}
	if time.Now().Unix() > int64(exp) {
		return nil, errors.New("token expired")
	}
	if jti, _ := claims["jti"].(string); jti != "" {
		s.mu.Lock()
		record, known := s.managedMCP[jti]
		s.mu.Unlock()
		if known && record.RevokedAt != 0 {
			return nil, errors.New("token revoked")
		}
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
