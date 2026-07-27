package hub

import (
	"bufio"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var BuildVersion = "go-dev"
var GitCommit = "worktree"

const defaultJWTKeyID = "gptadmin-hs256-v1"

// legacyCtlTokenDeadline is the fixed end of the one-week migration window.
// After this instant only AdminPassword sessions and scoped OAuth JWTs may
// authenticate human/MCP requests.
var legacyCtlTokenDeadline = time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)

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
	OAuthKeyID                 string
	EnvFile                    string
	OAuthPermissiveRedirects   bool
	OAuthPermissiveResources   bool
	AuthLogSecrets             bool
	AuthRateLimit              int
	BridgeKey                  string
	LegacyCtlTokenDeadline     time.Time
	Now                        func() time.Time
	RegistryStateFile          string
	FailoverConfigFile         string
	FailoverStateFile          string
	FailoverReclaimCommandFile string
	StartupInstructionsFile    string
	StartupInstructions        string
	InstructionSetsStateFile   string
	NetworkProxyStateFile      string
	NetworkProxyRelayKeyFile   string
	NetworkProxyRelayRevokeURL string
	WebhookConfigFile          string
	WebhookStateFile           string
	AuditStateFile             string
	SecurityStateFile          string
	TelemetryStateFile         string
	TelemetryOTLPEndpoint      string
	SecretStoreDir             string
	SecretStoreKeyFile         string
	SecretIngressStateFile     string
	SecretIngressTTL           time.Duration
	WebhookRoutes              []WebhookRoute
}

func FromEnv() Config {
	port := env("GPTADMIN_HUB_PORT", env("HUB_PORT", env("PORT", "9001")))
	// Keep the installer/legacy HUB_BIND input while failing closed to the
	// loopback interface when no explicit deployment host is configured. Public
	// HAOS/failover deployments set HUB_HOST explicitly at their boundary.
	host := env("GPTADMIN_HUB_HOST", env("HUB_HOST", env("HUB_BIND", "127.0.0.1")))
	root := env("GPTADMIN_ROOT", ".")
	cfgDir := env("GPTADMIN_CONFIG_DIR", filepath.Join(root, "config"))
	defTimeout := secondsEnv("MCP_RELAY_DEFAULT_TIMEOUT", 30)
	pollTimeout := secondsEnv("MCP_RELAY_POLL_MAX_TIMEOUT", 55)
	secretTTL := secondsEnv("GPTADMIN_SECRET_INGRESS_TTL", 15*60)
	if secretTTL < 60 || secretTTL > 3600 {
		secretTTL = 15 * 60
	}
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
		OAuthClientSecret:        env("OAUTH_CLIENT_SECRET", ""),
		OAuthKeyID:               env("GPTADMIN_JWT_KEY_ID", defaultJWTKeyID),
		EnvFile:                  env("GPTADMIN_ENV_FILE", "/etc/gptadmin/gptadmin.env"),
		OAuthPermissiveRedirects: truthyString(env("OAUTH_PERMISSIVE_REDIRECTS", "0")),
		OAuthPermissiveResources: truthyString(env("OAUTH_PERMISSIVE_RESOURCES", "0")),
		AuthLogSecrets:           truthyString(env("AUTH_LOG_SECRETS", "0")),
		AuthRateLimit:            positiveIntEnv("GPTADMIN_AUTH_RATE_LIMIT", 60),
		BridgeKey:                env("MCP_BRIDGE_KEY", env("CTL_TOKEN", "")),
		// Existing installations may still rely on this deprecated credential.
		// New installs do not create it; explicit rotation/removal is the cutoff.
		LegacyCtlTokenDeadline:     time.Time{},
		Now:                        time.Now,
		RegistryStateFile:          env("GPTADMIN_REGISTRY_STATE_FILE", filepath.Join(cfgDir, "registry_state.json")),
		FailoverConfigFile:         env("GPTADMIN_FAILOVER_CONFIG_FILE", filepath.Join(cfgDir, "failover_config.json")),
		FailoverStateFile:          env("GPTADMIN_FAILOVER_STATE_FILE", filepath.Join(cfgDir, "failover_state.json")),
		FailoverReclaimCommandFile: env("GPTADMIN_FAILOVER_RECLAIM_COMMAND_FILE", filepath.Join(cfgDir, "failover_reclaim_command.json")),
		StartupInstructionsFile:    env("GPTADMIN_STARTUP_INSTRUCTIONS_FILE", filepath.Join(cfgDir, "startup_instructions.md")),
		StartupInstructions:        env("GPTADMIN_STARTUP_INSTRUCTIONS", ""),
		InstructionSetsStateFile:   env("GPTADMIN_INSTRUCTION_SETS_STATE_FILE", filepath.Join(cfgDir, "instruction_sets_state.json")),
		NetworkProxyStateFile:      env("GPTADMIN_NETWORK_PROXY_STATE_FILE", filepath.Join(cfgDir, "network_proxy_state.json")),
		NetworkProxyRelayKeyFile:   env("GPTADMIN_NETWORK_PROXY_RELAY_KEY_FILE", ""),
		NetworkProxyRelayRevokeURL: strings.TrimRight(env("GPTADMIN_NETWORK_PROXY_RELAY_REVOKE_URL", ""), "/"),
		WebhookConfigFile:          env("GPTADMIN_WEBHOOK_CONFIG_FILE", filepath.Join(cfgDir, "webhooks.json")),
		WebhookStateFile:           env("GPTADMIN_WEBHOOK_STATE_FILE", filepath.Join(cfgDir, "webhook_state.json")),
		AuditStateFile:             env("GPTADMIN_AUDIT_STATE_FILE", filepath.Join(cfgDir, "audit.jsonl")),
		SecurityStateFile:          env("GPTADMIN_SECURITY_STATE_FILE", filepath.Join(cfgDir, securityStateFilename)),
		TelemetryStateFile:         env("GPTADMIN_TELEMETRY_STATE_FILE", filepath.Join(cfgDir, telemetryStateFilename)),
		TelemetryOTLPEndpoint:      env("GPTADMIN_OTLP_ENDPOINT", ""),
		SecretStoreDir:             env("GPTADMIN_SECRET_STORE_DIR", filepath.Join(cfgDir, "secrets")),
		SecretStoreKeyFile:         env("GPTADMIN_SECRET_STORE_KEY_FILE", filepath.Join(cfgDir, "secret-store.key")),
		SecretIngressStateFile:     env("GPTADMIN_SECRET_INGRESS_STATE_FILE", filepath.Join(cfgDir, "secrets", "requests.json")),
		SecretIngressTTL:           time.Duration(secretTTL) * time.Second,
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

func positiveIntEnv(k string, d int) int {
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

func (s *Server) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) legacyCtlTokenAllowed() bool { return true }

func (s *Server) markLegacyCtlToken(w http.ResponseWriter) {
	w.Header().Set("Deprecation", "true")
	if !s.cfg.LegacyCtlTokenDeadline.IsZero() {
		w.Header().Set("Sunset", s.cfg.LegacyCtlTokenDeadline.UTC().Format(http.TimeFormat))
	}
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
	ID          string         `json:"id"`
	AgentID     string         `json:"agent_id,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	TraceParent string         `json:"traceparent,omitempty"`
	Method      string         `json:"method"`
	Params      map[string]any `json:"params,omitempty"`
	CreatedAt   float64        `json:"created_at"`
	StartedAt   float64        `json:"started_at,omitempty"`
	DoneAt      float64        `json:"completed_at,omitempty"`
	Status      string         `json:"status"`
	Result      map[string]any `json:"result,omitempty"`
	Error       any            `json:"error,omitempty"`
}

type shellJob struct {
	ID           string         `json:"id"`
	Server       string         `json:"server,omitempty"`
	TraceID      string         `json:"trace_id,omitempty"`
	TraceParent  string         `json:"traceparent,omitempty"`
	ToolName     string         `json:"tool_name,omitempty"`
	Arguments    map[string]any `json:"arguments,omitempty"`
	Cmd          string         `json:"cmd,omitempty"`
	Cwd          string         `json:"cwd,omitempty"`
	Timeout      int            `json:"timeout,omitempty"`
	Env          map[string]any `json:"env,omitempty"`
	SecretValues []string       `json:"-"`
	CreatedAt    float64        `json:"created_at"`
	StartedAt    float64        `json:"started_at,omitempty"`
	DoneAt       float64        `json:"completed_at,omitempty"`
	Status       string         `json:"status"`
	Result       any            `json:"result,omitempty"`
	Error        any            `json:"error,omitempty"`
}

type auditEvent struct {
	Time   string         `json:"time"`
	Name   string         `json:"name"`
	Fields map[string]any `json:"fields,omitempty"`
}

type approvalRequest struct {
	ID              string    `json:"approval_id"`
	ProfileID       string    `json:"profile_id"`
	Actor           string    `json:"actor"`
	Target          string    `json:"target"`
	Tool            string    `json:"tool"`
	ArgumentsDigest string    `json:"arguments_digest"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Status          string    `json:"status"`
}

type autonomousBudget struct {
	WindowStart time.Time
	Count       int
}

const (
	autonomousCallLimit  = 32
	autonomousWindowSize = 5 * time.Minute
)

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
	ID           string   `json:"id"`
	ClientID     string   `json:"client_id"`
	TokenDigest  string   `json:"token_digest,omitempty"`
	TokenKind    string   `json:"token_kind,omitempty"`
	Audience     string   `json:"audience,omitempty"`
	Status       string   `json:"status,omitempty"`
	RedirectURIs []string `json:"redirect_uris,omitempty"`
	Scope        string   `json:"scope"`
	AccessMode   string   `json:"access_mode,omitempty"`
	ProfileID    string   `json:"profile_id,omitempty"`
	IssuedAt     int64    `json:"issued_at"`
	CreatedAt    int64    `json:"created_at,omitempty"`
	ExpiresAt    int64    `json:"expires_at"`
	RevokedAt    int64    `json:"revoked_at,omitempty"`
}

type managedMCPTokenState struct {
	Tokens map[string]managedMCPToken `json:"tokens"`
}

type authRateWindow struct {
	Started time.Time
	Count   int
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

	mu                sync.Mutex
	authRateMu        sync.Mutex
	cond              *sync.Cond
	agents            map[string]*Agent
	relayQueues       map[string][]string
	relayJobs         map[string]*relayJob
	shellQueues       map[string][]string
	shellJobs         map[string]*shellJob
	idempotency       map[string]*idempotencyEntry
	oauthCodes        map[string]oauthCode
	managedMCP        map[string]managedMCPToken
	oauthClients      map[string]oauthClientMetadata
	accessProfiles    map[string]AccessProfile
	approvals         map[string]*approvalRequest
	autonomous        map[string]*autonomousBudget
	security          securitySettings
	securityPath      string
	webauthnState     webAuthnState
	webauthnPath      string
	webauthnSessions  map[string]webAuthnSession
	telemetry         telemetryState
	telemetryPath     string
	telemetryExporter *telemetryExporter
	secretStore       *SecretStore
	secretStoreErr    error
	audit             []auditEvent
	authRate          map[string]authRateWindow
	failover          FailoverConfig

	updateStatePath     string
	updateLockPath      string
	updateLauncher      *UpdateLauncher
	networkProxy        *NetworkProxyController
	webhookRoutes       map[string]WebhookRoute
	webhookJobs         map[string]*webhookJob
	webhookDeliveries   map[string]*webhookDelivery
	webhookStateWriteMu sync.Mutex

	instructionMu      sync.RWMutex
	instructionWriteMu sync.Mutex
	instructionSet     InstructionSet
	instructionSetsMu  sync.RWMutex
	instructionSets    map[string]InstructionSet
}

func New(cfg Config) *Server {
	if cfg.AuthRateLimit <= 0 {
		cfg.AuthRateLimit = 60
	}
	webhookRoutes := append([]WebhookRoute(nil), cfg.WebhookRoutes...)
	if loadedRoutes, err := loadWebhookRoutes(cfg.WebhookConfigFile); err != nil {
		log.Printf("webhook config load failed path=%s err=%v", cfg.WebhookConfigFile, err)
	} else {
		webhookRoutes = append(webhookRoutes, loadedRoutes...)
	}
	if err := validateWebhookRoutes(webhookRoutes); err != nil {
		log.Printf("webhook config rejected path=%s err=%v", cfg.WebhookConfigFile, err)
		webhookRoutes = nil
	}
	securityPath := cfg.SecurityStateFile
	if securityPath == "" && cfg.ConfigDir != "" {
		securityPath = filepath.Join(cfg.ConfigDir, securityStateFilename)
	}
	securityKey := firstNonEmpty(cfg.AdminPassword, cfg.OAuthClientSecret, cfg.CtlToken, "gptadmin-security-state")
	security, err := loadSecuritySettings(securityPath, securityKey)
	if err != nil {
		log.Printf("security settings load failed path=%s err=%v", securityPath, err)
		security = defaultSecuritySettings()
	}
	telemetryPath := cfg.TelemetryStateFile
	if telemetryPath == "" && cfg.ConfigDir != "" {
		telemetryPath = filepath.Join(cfg.ConfigDir, telemetryStateFilename)
	}
	telemetry, err := loadTelemetryState(telemetryPath)
	if err != nil {
		log.Printf("telemetry state load failed path=%s err=%v", telemetryPath, err)
		telemetry = defaultTelemetryState()
	}
	telemetryExporter, err := newTelemetryExporter(cfg.TelemetryOTLPEndpoint)
	if err != nil {
		log.Printf("OTLP telemetry disabled: %v", err)
	}
	webauthnPath := ""
	if cfg.ConfigDir != "" {
		webauthnPath = filepath.Join(cfg.ConfigDir, webAuthnStateFilename)
	}
	webauthnState, err := loadWebAuthnState(webauthnPath)
	if err != nil {
		log.Printf("webauthn state load failed path=%s err=%v", webauthnPath, err)
		webauthnState = defaultWebAuthnState()
	}
	s := &Server{
		cfg:               cfg,
		agents:            map[string]*Agent{},
		relayQueues:       map[string][]string{},
		relayJobs:         map[string]*relayJob{},
		shellQueues:       map[string][]string{},
		shellJobs:         map[string]*shellJob{},
		idempotency:       map[string]*idempotencyEntry{},
		oauthCodes:        map[string]oauthCode{},
		managedMCP:        map[string]managedMCPToken{},
		oauthClients:      map[string]oauthClientMetadata{},
		accessProfiles:    map[string]AccessProfile{},
		approvals:         map[string]*approvalRequest{},
		autonomous:        map[string]*autonomousBudget{},
		security:          security,
		securityPath:      securityPath,
		webauthnState:     webauthnState,
		webauthnPath:      webauthnPath,
		webauthnSessions:  map[string]webAuthnSession{},
		telemetry:         telemetry,
		telemetryPath:     telemetryPath,
		telemetryExporter: telemetryExporter,
		audit:             []auditEvent{},
		authRate:          map[string]authRateWindow{},
		webhookRoutes:     webhookRouteMap(webhookRoutes),
		webhookJobs:       map[string]*webhookJob{},
		webhookDeliveries: map[string]*webhookDelivery{},
		instructionSets:   map[string]InstructionSet{},
	}
	if cfg.ConfigDir != "" || cfg.SecretStoreDir != "" || cfg.SecretStoreKeyFile != "" || cfg.SecretIngressStateFile != "" {
		if cfg.SecretStoreDir == "" {
			cfg.SecretStoreDir = filepath.Join(cfg.ConfigDir, "secrets")
		}
		if cfg.SecretStoreKeyFile == "" {
			cfg.SecretStoreKeyFile = filepath.Join(cfg.ConfigDir, "secret-store.key")
		}
		if cfg.SecretIngressStateFile == "" {
			cfg.SecretIngressStateFile = filepath.Join(cfg.SecretStoreDir, "requests.json")
		}
		if cfg.SecretIngressTTL <= 0 {
			cfg.SecretIngressTTL = 15 * time.Minute
		}
		s.cfg = cfg
		secretStore, secretStoreErr := NewSecretStoreWithStateFile(cfg.ConfigDir, cfg.SecretStoreDir, cfg.SecretStoreKeyFile, cfg.SecretIngressStateFile, cfg.Now)
		s.secretStore = secretStore
		s.secretStoreErr = secretStoreErr
		if secretStoreErr != nil {
			log.Printf("secret ingress store unavailable: %v", secretStoreErr)
		}
	}
	s.cond = sync.NewCond(&s.mu)
	networkProxyStatePath := cfg.NetworkProxyStateFile
	if networkProxyStatePath == "" && cfg.ConfigDir != "" {
		networkProxyStatePath = filepath.Join(cfg.ConfigDir, "network_proxy_state.json")
	}
	networkProxy, err := NewNetworkProxyController(networkProxyStatePath, cfg.Now, nil)
	if err != nil {
		log.Printf("network proxy state load failed path=%s err=%v", networkProxyStatePath, err)
		networkProxy = newUnavailableNetworkProxyController(cfg.Now, err)
	}
	s.networkProxy = networkProxy
	if cfg.NetworkProxyRelayKeyFile != "" {
		key, keyErr := os.ReadFile(cfg.NetworkProxyRelayKeyFile)
		key = []byte(strings.TrimRight(string(key), " \t\r\n"))
		if keyErr != nil {
			log.Printf("network proxy relay key load failed path=%s err=%v", cfg.NetworkProxyRelayKeyFile, keyErr)
		} else if len(key) < 32 {
			log.Printf("network proxy relay key rejected path=%s: key must contain at least 32 bytes", cfg.NetworkProxyRelayKeyFile)
		} else {
			networkProxy.SetRelayKey(key)
			if cfg.NetworkProxyRelayRevokeURL != "" {
				relayKey := append([]byte(nil), key...)
				relayURL := cfg.NetworkProxyRelayRevokeURL
				networkProxy.SetOnRevoke(func(capabilityID string) {
					go s.sendNetworkProxyRelayRevoke(relayURL, relayKey, capabilityID)
				})
			}
		}
	}
	s.instructionSet = newInstructionSet(cfg)
	if err := s.loadInstructionSetsState(); err != nil {
		log.Printf("instruction sets state load failed path=%s err=%v", s.instructionSetsStatePath(), err)
	}
	if err := s.loadRegistryState(); err != nil {
		log.Printf("registry state load failed path=%s err=%v", s.registryStatePath(), err)
	}
	if err := s.loadManagedMCPState(); err != nil {
		log.Printf("MCP token state load failed path=%s err=%v", s.managedMCPStatePath(), err)
	}
	if err := s.loadOAuthClientsState(); err != nil {
		log.Printf("OAuth client state load failed path=%s err=%v", s.oauthClientsStatePath(), err)
	}
	if err := s.loadAccessProfilesState(); err != nil {
		log.Printf("access profile state load failed path=%s err=%v", s.accessProfilesStatePath(), err)
	}
	if err := s.loadWebhookState(); err != nil {
		log.Printf("webhook state load failed path=%s err=%v", s.webhookStatePath(), err)
	}
	if err := s.loadAuditState(); err != nil {
		log.Printf("audit state load failed path=%s err=%v", s.auditStatePath(), err)
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

func (s *Server) sendNetworkProxyRelayRevoke(relayURL string, key []byte, capabilityID string) {
	ticket, err := signNetworkProxyRevocation(key, capabilityID, time.Now().UTC().Add(30*time.Second))
	if err != nil {
		log.Printf("network proxy relay revoke ticket failed capability=%s err=%v", capabilityID, err)
		return
	}
	body, err := json.Marshal(map[string]string{"ticket": ticket})
	if err != nil {
		return
	}
	request, err := http.NewRequest(http.MethodPost, relayURL+"/v1/control/revoke", bytes.NewReader(body))
	if err != nil {
		log.Printf("network proxy relay revoke request failed capability=%s err=%v", capabilityID, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		log.Printf("network proxy relay revoke delivery failed capability=%s err=%v", capabilityID, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		log.Printf("network proxy relay revoke rejected capability=%s status=%d", capabilityID, response.StatusCode)
	}
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
	if s.secretStoreErr != nil {
		return fmt.Errorf("secret ingress store unavailable: %w", s.secretStoreErr)
	}
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
	mux.HandleFunc("/metrics", s.hubMetrics)
	mux.HandleFunc("/actions/openapi.yaml", s.actionsOpenAPI)
	mux.HandleFunc("/artifacts/shellmcp.json", s.requireArtifact(s.shellmcpArtifactManifest))
	mux.HandleFunc("/artifacts/shellmcp.tar.gz", s.requireArtifact(s.shellmcpArtifactDownload))
	mux.HandleFunc("/artifacts/shellmcp-android-arm64.json", s.requireArtifact(s.androidShellmcpArtifactManifest))
	mux.HandleFunc("/artifacts/shellmcp-android-arm64.bin", s.requireArtifact(s.androidShellmcpArtifactDownload))
	// Legacy rootd artifact aliases: old services still point ROOTD_UPDATE_MANIFEST_URL here.
	mux.HandleFunc("/artifacts/rootd.json", s.requireArtifact(s.shellmcpArtifactManifest))
	mux.HandleFunc("/artifacts/rootd.tar.gz", s.requireArtifact(s.shellmcpArtifactDownload))
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
	mux.HandleFunc("/webhooks/v1/", s.webhookEndpoint)
	mux.HandleFunc("/webhook-jobs/", s.webhookJobEndpoint)
	mux.HandleFunc("/webhook-routes", s.requireCtl(s.webhookRoutesEndpoint))
	mux.HandleFunc("/webhook-routes/", s.requireCtl(s.webhookRoutesEndpoint))
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.oauthProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.oauthAuthorizationServer)
	mux.HandleFunc("/register", s.oauthRegister)
	mux.HandleFunc("/authorize", s.oauthAuthorize)
	mux.HandleFunc("/token", s.oauthToken)
	// Canonical OAuth paths are namespaced; root aliases remain for clients
	// pinned to the pre-contract endpoint names during the migration window.
	mux.HandleFunc("/oauth/authorize", s.oauthAuthorize)
	mux.HandleFunc("/oauth/token", s.oauthToken)
	mux.HandleFunc("/mcp", s.mcpEndpoint)
	mux.HandleFunc("/connect", s.connectionPage)
	mux.HandleFunc("/connect.json", s.connectionPage)
	mux.HandleFunc("/connect/callback", s.browserOAuthCallback)
	mux.HandleFunc("/secret-input/", s.secretIngress)
	mux.HandleFunc("/_services/", s.httpServiceEndpoint)
	mux.HandleFunc("/server/", s.serverMCPEndpoint)
	// Legacy alias kept for old pinned MCP URLs.
	mux.HandleFunc("/agent/", s.agentMCPEndpoint)
	mux.HandleFunc("/mcp-prompt/prompt", s.mcpPrompt)
	mux.HandleFunc("/mcp-prompt/call", s.mcpPromptCall)
	mux.HandleFunc("/proxy-control/v1/request", s.requireCtl(s.networkProxyRequestHTTP))
	mux.HandleFunc("/proxy-control/v1/approve", s.requireCtl(s.networkProxyApproveHTTP))
	mux.HandleFunc("/proxy-control/v1/issue", s.requireCtl(s.networkProxyIssueHTTP))
	mux.HandleFunc("/proxy-control/v1/open", s.requireCtl(s.networkProxyOpenHTTP))
	mux.HandleFunc("/proxy-control/v1/status", s.requireCtl(s.networkProxyStatusHTTP))
	mux.HandleFunc("/proxy-control/v1/revoke", s.requireCtl(s.networkProxyRevokeHTTP))
	mux.HandleFunc("/admin/api/mcp/manage", s.requireCtl(s.adminMCPManage))
	mux.HandleFunc("/admin/api/mcp/issue-token", s.requireCtl(s.adminMCPIssueToken))
	mux.HandleFunc("/admin/api/mcp/tokens/", s.requireCtl(s.adminMCPTokenAction))
	mux.HandleFunc("/admin/api/access-profiles", s.requireCtl(s.adminAccessProfiles))
	mux.HandleFunc("/admin/api/access-profiles/", s.requireCtl(s.adminAccessProfile))
	mux.HandleFunc("/admin/api/client-bindings/", s.requireCtl(s.adminClientBinding))
	mux.HandleFunc("/admin/api/mcp/resources/list", s.requireCtl(s.adminMCPResourcesList))
	mux.HandleFunc("/admin/api/mcp/resources/read", s.requireCtl(s.adminMCPResourceRead))
	mux.HandleFunc("/admin/api/auth/rotate-oauth", s.requireCtl(s.adminRotateOAuth))
	mux.HandleFunc("/admin/api/security/env", s.requireCtl(s.adminSecurityEnv))
	mux.HandleFunc("/admin/api/security/reauth", s.requireCtl(s.adminSecurityReauth))
	mux.HandleFunc("/admin/api/security/heartbeat", s.requireCtl(s.adminSecurityHeartbeat))
	mux.HandleFunc("/admin/api/security/preset", s.requireCtl(s.adminSecurityPreset))
	mux.HandleFunc("/admin/api/security/mfa/totp/enroll", s.requireCtl(s.adminTOTPEnroll))
	mux.HandleFunc("/admin/api/security/mfa/totp/verify", s.requireCtl(s.adminTOTPVerify))
	mux.HandleFunc("/admin/api/security/mfa/webauthn/register/begin", s.requireCtl(s.adminWebAuthnRegisterBegin))
	mux.HandleFunc("/admin/api/security/mfa/webauthn/register/finish", s.requireCtl(s.adminWebAuthnRegisterFinish))
	// Login ceremonies are the one admin surface reachable before the admin
	// session exists. The handler still requires an exact same-origin browser
	// request (or an existing internal credential), so it cannot become a
	// cross-site credential oracle.
	mux.HandleFunc("/admin/api/security/mfa/webauthn/login/begin", s.requireWebAuthnBrowser(s.adminWebAuthnLoginBegin))
	mux.HandleFunc("/admin/api/security/mfa/webauthn/login/finish", s.requireWebAuthnBrowser(s.adminWebAuthnLoginFinish))
	mux.HandleFunc("/admin/api/telemetry", s.requireCtl(s.adminTelemetry))
	mux.HandleFunc("/admin/api/telemetry/event", s.requireCtl(s.adminTelemetry))
	mux.HandleFunc("/admin/api/clients/revoke-all", s.requireCtl(s.adminClientsRevokeAll))
	mux.HandleFunc("/admin/api/clients/", s.requireCtl(s.adminClientDelete))
	mux.HandleFunc("/admin/api/overview", s.requireCtl(s.adminOverview))
	mux.HandleFunc("/admin/api/instruction-sets/default", s.requireCtl(s.adminDefaultInstructionSet))
	mux.HandleFunc("/admin/api/instruction-sets", s.requireCtl(s.adminInstructionSets))
	mux.HandleFunc("/admin/api/instruction-sets/", s.requireCtl(s.adminInstructionSet))
	mux.HandleFunc("/admin/api/update", s.requireCtl(s.adminTriggerUpdate))
	mux.HandleFunc("/admin/api/failover/state", s.requireCtl(s.adminFailoverState))
	mux.HandleFunc("/admin/api/failover/reclaim/accept", s.requireCtl(s.adminFailoverReclaimAccept))
	mux.HandleFunc("/admin/api/failover/reclaim", s.requireCtl(s.adminFailoverReclaim))
	mux.HandleFunc("/admin/api/failover", s.requireCtl(s.adminFailover))
	mux.HandleFunc("/admin/api/jobs", s.requireCtl(s.adminJobs))
	mux.HandleFunc("/admin/api/audit", s.requireCtl(s.adminAudit))
	mux.HandleFunc("/admin/api/approvals", s.requireCtl(s.adminApprovals))
	mux.HandleFunc("/admin/api/approvals/", s.requireCtl(s.adminApproval))
	mux.HandleFunc("/admin/api/clients", s.requireCtl(s.adminClients))
	mux.HandleFunc("/admin/login", s.adminLogin)
	mux.HandleFunc("/admin/logout", s.adminLogout)
	mux.HandleFunc("/admin/legacy/", s.adminLegacyStatic)
	mux.HandleFunc("/admin/", s.adminStatic)
	mux.HandleFunc("/admin", s.adminIndex)
	return withRequestTrace(withCORS(mux))
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
		w.Header().Set("Access-Control-Allow-Headers", "authorization,content-type,if-match,x-ctl-token,x-mcp-relay-token")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
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
		if r.URL.Path == "/admin/api/instruction-sets/default" && (r.Method == http.MethodGet || r.Method == http.MethodPut) {
			w.Header().Set("Cache-Control", "no-store")
		}
		if strings.HasPrefix(r.URL.Path, "/admin/api/instruction-sets/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if strings.HasPrefix(r.URL.Path, "/admin/api/access-profiles/") || strings.HasPrefix(r.URL.Path, "/admin/api/client-bindings/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if s.cfg.CtlToken != "" && tokenMatches(r, s.cfg.CtlToken) {
			s.markLegacyCtlToken(w)
			if !s.legacyCtlTokenAllowed() {
				s.authAudit("ctl_auth_denied", r, map[string]any{"reason": "legacy ctl token migration deadline passed"})
				s.writeCtlUnauthorized(w, r)
				return
			}
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
			*r = *s.applyAccessProfileContext(r, claims)
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
		if s.authFailureRateLimited(w, r) {
			return
		}
		s.writeCtlUnauthorized(w, r)
	}
}

func (s *Server) requireWebAuthnBrowser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.CtlToken != "" && tokenMatches(r, s.cfg.CtlToken) {
			next(w, r)
			return
		}
		if s.cfg.AdminPassword != "" && s.adminSessionValid(r) {
			next(w, r)
			return
		}
		expected := strings.TrimRight(s.origin(r), "/")
		requestOrigin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
		if requestOrigin == "" {
			if referer, err := url.Parse(r.Referer()); err == nil && referer.Scheme != "" && referer.Host != "" {
				requestOrigin = strings.TrimRight(referer.Scheme+"://"+referer.Host, "/")
			}
		}
		if requestOrigin != expected {
			s.authAudit("webauthn_browser_denied", r, map[string]any{"reason": "same-origin required"})
			writeJSON(w, http.StatusForbidden, map[string]any{"detail": "same-origin browser request required"})
			return
		}
		next(w, r)
	}
}

func (s *Server) requireRelay(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.RelayAgentToken != "" && (tokenMatches(r, s.cfg.RelayAgentToken) || hmac.Equal([]byte(r.Header.Get("X-MCP-Relay-Token")), []byte(s.cfg.RelayAgentToken))) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "unauthorized"})
	return false
}

func (s *Server) requireArtifact(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ShellMCP updates use the agent-bound credential, so they remain
		// possible after the human CLI bearer migration deadline.
		if s.cfg.ShellToken != "" && tokenMatches(r, s.cfg.ShellToken) && (s.cfg.ShellToken != s.cfg.CtlToken || s.legacyCtlTokenAllowed()) {
			next(w, r)
			return
		}
		s.requireCtl(next)(w, r)
	}
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
        "428":
          description: A write-capable call is waiting for one administrator approval.
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ApprovalRequired"
        "429":
          description: The bounded-autonomous profile has exhausted its write budget for the current window.
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
  /webhooks/v1/{route}:
    post:
      operationId: webhookIngress
      summary: Accept one authenticated webhook event
      description: The configured route selects the target action; the event cannot select a target or callback URL.
      security:
        - webhookToken: []
      parameters:
        - name: route
          in: path
          required: true
          schema:
            type: string
      requestBody:
        required: true
        content:
          application/json: {}
      responses:
        "202":
          description: Accepted webhook job
  /webhook-jobs/{job_id}:
    get:
      operationId: webhookJob
      summary: Read an authenticated webhook job
      security:
        - webhookToken: []
      parameters:
        - name: job_id
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: Durable webhook job state
  /webhook-routes/{route}:
    put:
      operationId: replaceWebhookRoute
      summary: Replace an operator-owned webhook route
    delete:
      operationId: deleteWebhookRoute
      summary: Delete an operator-owned webhook route
  /proxy-control/v1/request:
    post:
      operationId: networkProxyRequest
      summary: Request a bounded Network Tunnel capability
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [profile_id, policy]
              properties:
                profile_id: {type: string}
                policy: {type: object, additionalProperties: true}
      responses:
        "201": {description: Pending capability}
  /proxy-control/v1/approve:
    post:
      operationId: networkProxyApprove
      summary: Approve a pending Network Tunnel capability
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [capability_id]
              properties:
                capability_id: {type: string}
      responses:
        "200": {description: Active capability}
  /proxy-control/v1/issue:
    post:
      operationId: networkProxyIssue
      summary: Issue one client and one agent stream grant
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [capability_id, target]
              properties:
                capability_id: {type: string}
                target: {type: string}
      responses:
        "200": {description: Short-lived role-bound grants}
  /proxy-control/v1/open:
    post:
      operationId: networkProxyOpen
      summary: Consume a one-time stream grant
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [token, role]
              properties:
                token: {type: string}
                role: {type: string, enum: [client, agent]}
      responses:
        "200": {description: Consumed grant metadata without the bearer token}
  /proxy-control/v1/status:
    get:
      operationId: networkProxyStatus
      summary: Read Network Tunnel capability state
      parameters:
        - name: capability_id
          in: query
          required: true
          schema: {type: string}
      responses:
        "200": {description: Capability state}
  /proxy-control/v1/revoke:
    post:
      operationId: networkProxyRevoke
      summary: Revoke and drain a Network Tunnel capability
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [capability_id]
              properties:
                capability_id: {type: string}
      responses:
        "200": {description: Draining capability}
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
    webhookToken:
      type: http
      scheme: bearer
  schemas:
    ApprovalRequired:
      type: object
      additionalProperties: false
      required: [status, approval_id, expires_at, target, tool]
      properties:
        status:
          type: string
          enum: [approval_required]
        approval_id:
          type: string
          description: Opaque one-time approval handle; never a copy of the request arguments.
        expires_at:
          type: string
          format: date-time
        target:
          type: string
        tool:
          type: string
        message:
          type: string
    BoundedAutonomousLimit:
      type: object
      additionalProperties: false
      required: [status, retry_at, limit, window]
      properties:
        status:
          type: string
          enum: [bounded_autonomous_limit]
        retry_at:
          type: string
          format: date-time
        limit:
          type: integer
          minimum: 1
        window:
          type: string
        message:
          type: string
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
        schema_version:
          type: string
          description: Version returned by the selected target schema.
        schema_digest_sha256:
          type: string
          minLength: 64
          maxLength: 64
          pattern: '^[a-f0-9]{64}$'
          description: Digest returned by the selected target schema.
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
          description: Schema responses include schema_version and schema_digest_sha256.
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
	responseStatus := http.StatusOK
	approvalID := firstString(req, "approval_id")
	for _, srv := range targets {
		target := "shell:" + strings.TrimPrefix(srv, "shell:")
		args := map[string]any{"cmd": cmd, "cwd": firstString(req, "cwd"), "timeout": req["timeout"]}
		if approvalID != "" {
			args["approval_id"] = approvalID
		}
		policyRequest := requestWithAutomationProfile(r, "bulk-exec", target, "shell_exec", approvalModeAskBeforeWrite)
		result, status := s.executeMCPTool(policyRequest, target, "shell_exec", args, true, s.cfg.DefaultTimeout, "")
		results[srv] = result
		if status > responseStatus {
			responseStatus = status
		}
	}
	writeJSON(w, responseStatus, map[string]any{"ok": responseStatus < http.StatusBadRequest, "results": results})
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
	identity := shellIdentityFromMap(meta)
	s.mu.Lock()
	approved := s.shellIdentityApprovedLocked(agentID, identity)
	meta["approved"] = approved
	status := "online"
	if !approved {
		status = "awaiting_approval"
	}
	s.agents[agentID] = &Agent{AgentID: agentID, Name: "Shell: " + name, Kind: "virtual_shell", Transport: transport, Status: status, LastSeen: now, Capabilities: []string{"shell", "system", "tasks", "logs"}, Meta: meta}
	auditName := "heartbeat"
	if !approved {
		auditName = "heartbeat_awaiting_approval"
	}
	s.addAuditLocked(auditName, map[string]any{"agent_id": agentID, "transport": transport})
	if err := s.saveRegistryStateLocked(); err != nil {
		log.Printf("registry state save failed: %v", err)
	}
	if err := s.saveFailoverStateBundleLocked(); err != nil {
		log.Printf("failover state save failed: %v", err)
	}
	s.mu.Unlock()
	responseStatus := "registered"
	if !approved {
		responseStatus = "awaiting_approval"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent_id": agentID, "status": responseStatus})
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
	if !s.touchShellPollLocked(name, r) {
		writeJSON(w, http.StatusOK, map[string]any{"server_id": "shell:" + name, "status": "awaiting_approval"})
		return
	}
	for {
		if !s.touchShellPollLocked(name, r) {
			writeJSON(w, http.StatusOK, map[string]any{"server_id": "shell:" + name, "status": "awaiting_approval"})
			return
		}
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

func (s *Server) touchShellPollLocked(name string, r *http.Request) bool {
	if name == "" {
		return false
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
	approved := s.shellIdentityApprovedLocked(agentID, shellIdentityFromMap(meta))
	meta["approved"] = approved
	if a := s.agents[agentID]; a != nil {
		if !approved {
			a.Status = "awaiting_approval"
		} else {
			a.Status = "online"
		}
		a.LastSeen = now
		a.Transport = mode
		if a.Meta == nil {
			a.Meta = map[string]any{}
		}
		for k, v := range meta {
			a.Meta[k] = v
		}
		return approved
	}
	status := "online"
	auditName := "queue_poll_register"
	if !approved {
		status = "awaiting_approval"
		auditName = "queue_poll_awaiting_approval"
	}
	s.agents[agentID] = &Agent{AgentID: agentID, Name: "Shell: " + name, Kind: "virtual_shell", Transport: mode, Status: status, LastSeen: now, Capabilities: []string{"shell", "system", "tasks", "logs"}, Meta: meta}
	s.addAuditLocked(auditName, map[string]any{"agent_id": agentID, "transport": mode})
	if err := s.saveRegistryStateLocked(); err != nil {
		log.Printf("registry state save failed: %v", err)
	}
	if err := s.saveFailoverStateBundleLocked(); err != nil {
		log.Printf("failover state save failed: %v", err)
	}
	return approved
}

func shellIdentityFromMap(values map[string]any) map[string]string {
	identity := map[string]string{}
	for _, key := range []string{"server_id", "public_key", "fingerprint"} {
		if value := strings.TrimSpace(fmt.Sprint(values[key])); value != "" && value != "<nil>" {
			identity[key] = value
		}
	}
	return identity
}

func (s *Server) shellIdentityApprovedLocked(agentID string, identity map[string]string) bool {
	if len(identity) == 0 {
		return true
	}
	agent := s.agents[agentID]
	if agent == nil {
		return false
	}
	for key, expected := range identity {
		actual := strings.TrimSpace(fmt.Sprint(agent.Meta[key]))
		if actual != "" && actual != "<nil>" && actual != expected {
			return false
		}
	}
	if approved, ok := agent.Meta["approved"].(bool); ok {
		return approved
	}
	return true
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
	fields := map[string]any{"server": name, "job_id": res.ID, "status": job.Status}
	if job.TraceID != "" {
		fields["trace_id"] = job.TraceID
	}
	if job.TraceParent != "" {
		fields["traceparent"] = job.TraceParent
	}
	s.addAuditLocked("shell_result", fields)
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
			payload := map[string]any{"id": job.ID, "method": job.Method, "params": job.Params}
			if job.TraceID != "" {
				payload["trace_id"] = job.TraceID
			}
			if job.TraceParent != "" {
				payload["traceparent"] = job.TraceParent
			}
			writeJSON(w, http.StatusOK, payload)
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
	if job.AgentID != agentID {
		s.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "relay result does not belong to this agent"})
		return
	}
	if job.Status == "completed" || job.Status == "failed" {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "relay job already has a terminal result"})
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
	fields := map[string]any{"server_id": agentID, "job_id": res.ID, "status": job.Status}
	if job.TraceID != "" {
		fields["trace_id"] = job.TraceID
	}
	if job.TraceParent != "" {
		fields["traceparent"] = job.TraceParent
	}
	s.addAuditLocked("mcp_result", fields)
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
		"meta":         redactPublicMetadata(a.Meta),
	}
}

// redactPublicMetadata removes credentials from agent metadata before it is
// returned by discovery. Agent transport arguments are operator-supplied and
// can contain authorization headers even when their enclosing key is benign.
func redactPublicMetadata(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, nested := range typed {
			if isSensitiveMetadataKey(key) {
				redacted[key] = "<redacted>"
				continue
			}
			redacted[key] = redactPublicMetadata(nested)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, nested := range typed {
			redacted[index] = redactPublicMetadata(nested)
		}
		return redacted
	case string:
		if isSensitiveMetadataValue(typed) {
			return "<redacted>"
		}
	}
	return value
}

func isSensitiveMetadataKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{"token", "secret", "password", "authorization", "credential", "api_key"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isSensitiveMetadataValue(value string) bool {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "authorization:") || strings.Contains(lower, "bearer ") {
		return true
	}
	for _, key := range []string{"token", "secret", "password", "api_key"} {
		if strings.Contains(lower, key+"=") {
			return true
		}
	}
	return false
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
	if err := authorizeFacadeCall(r, "schema", req); err != nil {
		s.auditToolDecision(r, firstString(req, "target", "server_id", "agent_id"), "schema", req, "deny", err.Error(), nil, http.StatusForbidden)
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
		return
	}
	target := firstString(req, "target", "server_id", "agent_id")
	selectedTarget, status, detail := s.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]any{"detail": detail})
		return
	}
	target = selectedTarget
	if target == "hub" {
		writeJSON(w, http.StatusOK, withActionToolHints(withSchemaContractMetadata(map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": toolsForRequest(r, target, hubTools())}}), target))
		return
	}
	if strings.HasPrefix(target, "shell:") {
		writeJSON(w, http.StatusOK, withActionToolHints(withSchemaContractMetadata(map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": toolsForRequest(r, target, shellTools())}}), target))
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
	resp := withSchemaContractMetadata(s.waitRelay(jobID, timeoutFromReq(req, s.cfg.DefaultTimeout)))
	resp = withActionToolHints(resp, target)
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
		s.auditToolDecision(r, target, toolName, args, "deny", err.Error(), nil, http.StatusForbidden)
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
		return
	}
	selectedTarget, status, detail := s.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]any{"detail": detail})
		return
	}
	target = selectedTarget
	if response, blocked := s.validateSchemaContract(r, target, req); blocked {
		writeJSON(w, http.StatusConflict, response)
		return
	}
	resp, status := s.executeMCPTool(r, target, toolName, args, truthy(req["background"]), timeoutFromReq(req, s.cfg.DefaultTimeout), firstString(req, "idempotency_key"))
	writeJSON(w, status, resp)
}

const (
	idempotencyTTL     = 15 * time.Minute
	idempotencyMaxSize = 1024
	idempotencyKeyMax  = 200
)

func (s *Server) executeMCPTool(r *http.Request, target, toolName string, args map[string]any, background bool, timeout time.Duration, key string) (map[string]any, int) {
	callArgs, approvalID := approvalArguments(args)
	if response, blocked := s.approvalGate(r, target, toolName, callArgs, approvalID); blocked {
		s.auditToolDecision(r, target, toolName, callArgs, "deny", "approval required", response, http.StatusPreconditionRequired)
		return response, http.StatusPreconditionRequired
	}
	if response, blocked := s.boundedAutonomousGate(r, target, toolName); blocked {
		s.auditToolDecision(r, target, toolName, callArgs, "deny", "bounded autonomous budget exhausted", response, http.StatusTooManyRequests)
		return response, http.StatusTooManyRequests
	}
	args = callArgs
	secretValues := []string(nil)
	if toolName == "shell_exec" {
		resolvedArgs, resolvedSecrets, err := s.resolveSecretEnvForRequest(r, target, args)
		if err != nil {
			s.auditToolDecision(r, target, toolName, callArgs, "deny", err.Error(), nil, http.StatusForbidden)
			return map[string]any{"status": "failed", "error": err.Error()}, http.StatusForbidden
		}
		args = resolvedArgs
		secretValues = resolvedSecrets
	}
	operation := func() (map[string]any, int) {
		if target == "hub" {
			resp, status := s.callHubToolForRequest(r, toolName, args)
			return map[string]any{"server_id": target, "status": "completed", "response": resp}, status
		}
		if strings.HasPrefix(target, "shell:") {
			return s.callShellToolWithTraceParentAndSecrets(target, toolName, args, background, timeout, requestTraceID(r), requestTraceParent(r), secretValues), http.StatusOK
		}
		jobID := s.enqueueRelayWithTraceParent(target, "tools/call", map[string]any{"name": toolName, "arguments": args}, requestTraceID(r), requestTraceParent(r))
		if background {
			response := map[string]any{"server_id": target, "status": "running", "background": true, "job_id": jobID}
			if traceID := requestTraceID(r); traceID != "" {
				response["trace_id"] = traceID
			}
			if parent := requestTraceParent(r); parent != "" {
				response["traceparent"] = parent
			}
			return response, http.StatusOK
		}
		return s.waitRelay(jobID, timeout), http.StatusOK
	}
	key = strings.TrimSpace(key)
	if key == "" {
		response, status := operation()
		s.auditToolDecision(r, target, toolName, args, "allow", "", response, status)
		return response, status
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
	s.auditToolDecision(r, target, toolName, args, "allow", "", response, status)
	s.mu.Lock()
	entry.JobID = firstString(response, "job_id")
	entry.Response = cloneMap(response)
	entry.Status = status
	close(entry.Done)
	s.mu.Unlock()
	return response, status
}

func approvalArguments(args map[string]any) (map[string]any, string) {
	callArgs := cloneMap(args)
	approvalID := firstString(callArgs, "approval_id")
	delete(callArgs, "approval_id")
	return callArgs, approvalID
}

func (s *Server) approvalGate(r *http.Request, target, toolName string, args map[string]any, approvalID string) (map[string]any, bool) {
	profile, bound := AccessProfileFromRequest(r)
	if !bound || profile.ApprovalMode != approvalModeAskBeforeWrite || isReadOnlyTool(target, toolName) {
		return nil, false
	}
	actor := s.actorForRequest(r)
	digestBytes, err := json.Marshal(args)
	if err != nil {
		return map[string]any{"status": "failed", "error": "arguments cannot be serialized"}, true
	}
	digest := sha256Hex(digestBytes)
	now := s.now()
	if approvalID != "" {
		s.mu.Lock()
		approval := s.approvals[approvalID]
		if approval != nil && approval.Status == "approved" && now.Before(approval.ExpiresAt) &&
			approval.ProfileID == profile.ID && approval.Actor == actor && approval.Target == target &&
			approval.Tool == toolName && approval.ArgumentsDigest == digest {
			approval.Status = "consumed"
			s.mu.Unlock()
			return nil, false
		}
		s.mu.Unlock()
	}
	approval := &approvalRequest{
		ID: newID(), ProfileID: profile.ID, Actor: actor, Target: target, Tool: toolName,
		ArgumentsDigest: digest, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute), Status: "pending",
	}
	s.mu.Lock()
	if len(s.approvals) >= 256 {
		s.mu.Unlock()
		return map[string]any{"status": "failed", "error": "approval queue is full"}, true
	}
	s.approvals[approval.ID] = approval
	s.mu.Unlock()
	return map[string]any{
		"status": "approval_required", "approval_id": approval.ID,
		"expires_at": approval.ExpiresAt, "target": target, "tool": toolName,
		"message": "An administrator must approve this write before execution.",
	}, true
}

func (s *Server) boundedAutonomousGate(r *http.Request, target, toolName string) (map[string]any, bool) {
	profile, bound := AccessProfileFromRequest(r)
	if !bound || profile.ApprovalMode != approvalModeBoundedAutonomous || isReadOnlyTool(target, toolName) {
		return nil, false
	}
	key := profile.ID + "\x00" + s.actorForRequest(r)
	now := s.now()
	s.mu.Lock()
	budget := s.autonomous[key]
	if budget == nil || !now.Before(budget.WindowStart.Add(autonomousWindowSize)) {
		budget = &autonomousBudget{WindowStart: now}
		s.autonomous[key] = budget
	}
	if budget.Count >= autonomousCallLimit {
		retryAt := budget.WindowStart.Add(autonomousWindowSize)
		s.mu.Unlock()
		return map[string]any{
			"status":   "bounded_autonomous_limit",
			"retry_at": retryAt,
			"limit":    autonomousCallLimit,
			"window":   autonomousWindowSize.String(),
			"message":  "The bounded autonomous write budget is exhausted; retry after the window.",
		}, true
	}
	budget.Count++
	s.mu.Unlock()
	return nil, false
}

func isReadOnlyTool(target, toolName string) bool {
	if toolName == "resources/list" || toolName == "resources/read" {
		return true
	}
	if target == "hub" {
		switch toolName {
		case "discover", "demo", "list_mcp_servers", "listMcpServers", "list_mcp_agents", "listMcpAgents", "pending", "list_pending_servers", "hub_status", "status", "schema", "list_mcp_tools", "listMcpTools", "job", "get_mcp_job", "getMcpJob":
			return true
		default:
			return false
		}
	}
	return strings.HasPrefix(target, "shell:") && toolName == "system_inspect"
}

func (s *Server) actorForRequest(r *http.Request) string {
	if r == nil {
		return "anonymous"
	}
	if s.cfg.CtlToken != "" && tokenMatches(r, s.cfg.CtlToken) {
		return "legacy_ctl"
	}
	if claims, ok := r.Context().Value(authClaimsContextKey{}).(map[string]any); ok {
		if actor := firstString(claims, "client_id", "sub"); actor != "" {
			return actor
		}
		return "scoped_connection"
	}
	if s.adminSessionValid(r) {
		return "admin_session"
	}
	return "anonymous"
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func (s *Server) auditToolDecision(r *http.Request, target, toolName string, args map[string]any, decision, reason string, response map[string]any, status int) {
	encoded, err := json.Marshal(args)
	if err != nil {
		encoded = []byte("<unserializable>")
	}
	resultReference := "none"
	if response != nil {
		resultReference = firstString(response, "job_id", "id")
		if resultReference == "" {
			resultReference = "inline"
		}
	}
	actor := "anonymous"
	identityFields := map[string]any{}
	if r != nil {
		if s.cfg.CtlToken != "" && tokenMatches(r, s.cfg.CtlToken) {
			actor = "legacy_ctl"
		} else if claims, ok := r.Context().Value(authClaimsContextKey{}).(map[string]any); ok {
			actor = firstString(claims, "client_id", "sub")
			identityFields["client_id"] = firstString(claims, "client_id")
			identityFields["subject"] = firstString(claims, "sub")
			identityFields["jti"] = firstString(claims, "jti")
			if actor == "" {
				actor = "scoped_connection"
			}
		} else if s.adminSessionValid(r) {
			actor = "admin_session"
		}
	}
	fields := map[string]any{
		"actor":            actor,
		"profile_id":       AccessProfileIDFromRequest(r),
		"target":           target,
		"tool":             toolName,
		"policy_decision":  decision,
		"arguments_digest": sha256Hex(encoded),
		"result_reference": resultReference,
		"status":           status,
		"access_mode":      requestAccessMode(r),
	}
	if reason != "" {
		fields["policy_reason"] = reason
	}
	if traceID := requestTraceID(r); traceID != "" {
		fields["trace_id"] = traceID
	}
	for key, value := range identityFields {
		if value != "" {
			fields[key] = value
		}
	}
	s.mu.Lock()
	s.addAuditLocked("tool_policy_decision", fields)
	s.mu.Unlock()
}

func toolArgsFromTopLevel(req map[string]any) map[string]any {
	reserved := map[string]bool{
		"target": true, "server_id": true, "agent_id": true,
		"tool": true, "tool_name": true, "name": true,
		"arguments": true, "args": true,
		"background":           true,
		"idempotency_key":      true,
		"schema_version":       true,
		"schema_digest_sha256": true,
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
	if raw, ok := req["secret_env"]; ok {
		args["secret_env"] = raw
	}
	resp, responseStatus := s.executeMCPTool(r, target, "shell_exec", args, truthy(req["background"]), timeoutFromReq(req, s.cfg.DefaultTimeout), "")
	writeJSON(w, responseStatus, resp)
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
	return s.enqueueRelayWithTrace(agentID, method, params, "")
}

func (s *Server) enqueueRelayWithTrace(agentID, method string, params map[string]any, traceID string) string {
	return s.enqueueRelayWithTraceParent(agentID, method, params, traceID, "")
}

func (s *Server) enqueueRelayWithTraceParent(agentID, method string, params map[string]any, traceID, traceParent string) string {
	id := newID()
	s.mu.Lock()
	s.relayJobs[id] = &relayJob{ID: id, AgentID: agentID, TraceID: traceID, TraceParent: traceParent, Method: method, Params: params, CreatedAt: nowFloat(), Status: "queued"}
	s.relayQueues[agentID] = append(s.relayQueues[agentID], id)
	fields := map[string]any{"server_id": agentID, "job_id": id, "method": method}
	if traceID != "" {
		fields["trace_id"] = traceID
	}
	if traceParent != "" {
		fields["traceparent"] = traceParent
	}
	s.addAuditLocked("mcp_enqueue", fields)
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
	response := map[string]any{"server_id": job.AgentID, "status": job.Status, "job_id": job.ID}
	if job.TraceID != "" {
		response["trace_id"] = job.TraceID
	}
	if job.TraceParent != "" {
		response["traceparent"] = job.TraceParent
	}
	if job.Status == "failed" {
		response["error"] = job.Error
		return response
	}
	response["response"] = spillFriendly(job.Result)
	return response
}

func shellJobResponse(job *shellJob) map[string]any {
	out := map[string]any{"server_id": "shell:" + job.Server, "status": job.Status, "job_id": job.ID, "task_id": job.ID}
	if job.TraceID != "" {
		out["trace_id"] = job.TraceID
	}
	if job.TraceParent != "" {
		out["traceparent"] = job.TraceParent
	}
	if job.Result != nil {
		out["response"] = map[string]any{"content": []map[string]any{{"type": "text", "text": "shell_exec completed on " + job.Server}}, "structuredContent": map[string]any{"server": job.Server, "result": redactSecretValues(job.Result, job.SecretValues)}}
	}
	if job.Error != nil {
		out["error"] = redactSecretValues(job.Error, job.SecretValues)
	}
	return out
}

func (s *Server) callHubTool(name string, args map[string]any) (map[string]any, int) {
	return s.callHubToolForRequest(nil, name, args)
}

func (s *Server) callHubToolForRequest(r *http.Request, name string, args map[string]any) (map[string]any, int) {
	if isNetworkProxyHubTool(name) {
		return s.callNetworkProxyTool(AccessProfileIDFromRequest(r), name, args)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := map[string]any{"tool": name}
	if traceID := requestTraceID(r); traceID != "" {
		fields["trace_id"] = traceID
	}
	s.addAuditLocked("hub_tool", fields)
	switch name {
	case "discover", "listMcpServers", "list_mcp_servers":
		servers := s.publicServersLockedWithDetail(nil, fullDetailRequested(args["detail"]))
		return map[string]any{"servers": servers}, http.StatusOK
	case "listMcpAgents", "list_mcp_agents":
		agents := s.publicAgentsLocked(nil)
		return map[string]any{"agents": agents}, http.StatusOK
	case "pending", "list_pending_servers":
		pending := make([]map[string]any, 0)
		for _, agent := range s.agents {
			if agent != nil && agent.Status == "awaiting_approval" {
				pending = append(pending, agentAsServer(*agent))
			}
		}
		return map[string]any{"pending": pending, "count": len(pending)}, http.StatusOK
	case "approve_pending_server":
		target := firstString(args, "server_id", "agent_id", "name")
		if target == "" {
			return map[string]any{"error": "server_id or name is required"}, http.StatusBadRequest
		}
		if !strings.HasPrefix(target, "shell:") {
			target = "shell:" + target
		}
		agent := s.agents[target]
		if agent == nil || agent.Status != "awaiting_approval" {
			return map[string]any{"error": "pending server not found", "server_id": target}, http.StatusNotFound
		}
		agent.Status = "online"
		agent.LastSeen = nowFloat()
		if agent.Meta == nil {
			agent.Meta = map[string]any{}
		}
		agent.Meta["approved"] = true
		s.addAuditLocked("approve_pending_server", map[string]any{"agent_id": target})
		if err := s.saveRegistryStateLocked(); err != nil {
			log.Printf("registry state save failed: %v", err)
		}
		return map[string]any{"ok": true, "status": "approved", "server_id": target}, http.StatusOK
	case "hub_status", "status":
		awaiting := 0
		for _, agent := range s.agents {
			if agent != nil && agent.Status == "awaiting_approval" {
				awaiting++
			}
		}
		return map[string]any{"ok": true, "servers": len(s.agents), "awaiting_approval": awaiting, "relay_jobs": len(s.relayJobs), "shell_jobs": len(s.shellJobs)}, http.StatusOK
	case "demo":
		return map[string]any{
			"status":        "ok",
			"connection":    map[string]any{"hub_url": s.origin(r), "mcp_endpoint": s.origin(r) + "/mcp"},
			"build_version": BuildVersion,
			"access_mode":   requestAccessMode(r),
			"capabilities":  []string{"mcp", "read_only_demo"},
			"message":       "GPTAdmin safe demo is ready; no command or credential access was used.",
		}, http.StatusOK
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
	return s.callShellToolWithTrace(target, toolName, args, background, timeout, "")
}

func (s *Server) callShellToolWithTrace(target, toolName string, args map[string]any, background bool, timeout time.Duration, traceID string) map[string]any {
	return s.callShellToolWithTraceParent(target, toolName, args, background, timeout, traceID, "")
}

func (s *Server) callShellToolWithTraceParent(target, toolName string, args map[string]any, background bool, timeout time.Duration, traceID, traceParent string) map[string]any {
	return s.callShellToolWithTraceParentAndSecrets(target, toolName, args, background, timeout, traceID, traceParent, nil)
}

func (s *Server) callShellToolWithTraceParentAndSecrets(target, toolName string, args map[string]any, background bool, timeout time.Duration, traceID, traceParent string, secretValues []string) map[string]any {
	server := canonicalShellQueueName(strings.TrimPrefix(target, "shell:"))
	if toolName == "" {
		return map[string]any{"server_id": target, "status": "failed", "error": "missing tool name"}
	}
	job := &shellJob{ID: newID(), Server: server, TraceID: traceID, TraceParent: traceParent, ToolName: toolName, Arguments: cloneMap(args), SecretValues: append([]string(nil), secretValues...), CreatedAt: nowFloat(), Status: "queued"}
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
	fields := map[string]any{"server": server, "job_id": job.ID}
	if traceID != "" {
		fields["trace_id"] = traceID
	}
	if traceParent != "" {
		fields["traceparent"] = traceParent
	}
	s.addAuditLocked("shell_enqueue", fields)
	s.cond.Broadcast()
	s.mu.Unlock()
	if background {
		response := map[string]any{"server_id": target, "status": "running", "background": true, "job_id": job.ID, "task_id": job.ID, "message": "shell job queued"}
		if traceID != "" {
			response["trace_id"] = traceID
		}
		if traceParent != "" {
			response["traceparent"] = traceParent
		}
		return response
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
	tools := []map[string]any{
		{"name": "discover", "description": "List registered targets", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "demo", "description": "Run a safe read-only connection check; no shell or credentials", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
		{"name": "pending", "description": "List pending approvals", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "approve_pending_server", "description": "Approve one ShellMCP device awaiting enrollment", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"server_id": map[string]any{"type": "string", "description": "Exact shell:<name> returned by pending"}}, "required": []string{"server_id"}, "additionalProperties": false}},
		{"name": "status", "description": "Return Hub status", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	}
	tools = append(tools, networkProxyHubTools()...)
	return append(tools, secretHubTools()...)
}

func shellTools() []map[string]any {
	return []map[string]any{
		{"name": "system_inspect", "description": "Read bounded redacted files/directories; no commands", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"read_file", "list_directory"}}, "path": map[string]any{"type": "string"}, "max_bytes": map[string]any{"type": []string{"integer", "null"}, "minimum": 1, "maximum": 1048576}}, "required": []string{"action", "path"}, "additionalProperties": false}},
		{"name": "shell_exec", "description": "Run one command as the default non-root user; secret_env references are resolved by the Hub and never returned", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}, "cwd": map[string]any{"type": []string{"string", "null"}}, "timeout": map[string]any{"type": []string{"integer", "null"}}, "run_as_user": map[string]any{"type": []string{"string", "null"}, "description": "Use root only when intentional"}, "secret_env": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Map environment names to opaque secret_ref values"}}, "required": []string{"cmd"}}},
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
				items = append(items, map[string]any{"task_id": j.ID, "job_id": j.ID, "server": j.Server, "cmd": redactSecretValues(j.Cmd, j.SecretValues), "status": j.Status, "result": redactSecretValues(j.Result, j.SecretValues), "error": redactSecretValues(j.Error, j.SecretValues), "created_at": j.CreatedAt, "started_at": j.StartedAt, "completed_at": j.DoneAt})
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
	if err := authorizeToolCall(r, target, "resources/list"); err != nil {
		s.auditToolDecision(r, target, "resources/list", nil, "deny", err.Error(), nil, http.StatusForbidden)
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
		return
	}
	args := map[string]any{}
	result, status := s.executeMCPTool(r, target, "resources/list", args, truthy(req["background"]), timeoutFromReq(req, s.cfg.DefaultTimeout), "")
	writeJSON(w, status, result)
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
	if err := authorizeToolCall(r, target, "resources/read"); err != nil {
		s.auditToolDecision(r, target, "resources/read", map[string]any{"uri": uri}, "deny", err.Error(), nil, http.StatusForbidden)
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": err.Error()})
		return
	}
	result, status := s.executeMCPTool(r, target, "resources/read", map[string]any{"uri": uri}, truthy(req["background"]), timeoutFromReq(req, s.cfg.DefaultTimeout), "")
	writeJSON(w, status, result)
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
	token, record, err := s.issueManagedMCPTokenWithMode(clientID, ttlDays, origin, resource, accessMode, "")
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
	return s.issueManagedMCPTokenWithMode(clientID, ttlDays, origin, resource, accessModeFull, "")
}

func (s *Server) issueManagedMCPTokenWithMode(clientID string, ttlDays int, origin, resource, accessMode, profileID string) (string, managedMCPToken, error) {
	now := time.Now().Unix()
	scope := "gptadmin.read gptadmin.exec"
	if accessMode == accessModeReadonly {
		scope = "gptadmin.read gptadmin.inspect"
	}
	record := managedMCPToken{ID: newID(), ClientID: clientID, Scope: scope, AccessMode: accessMode, ProfileID: profileID, IssuedAt: now, ExpiresAt: now + int64(ttlDays)*24*3600}
	token, err := s.signJWT(map[string]any{
		"sub": "admin", "scope": record.Scope, "access_mode": record.AccessMode, "client_id": clientID, "jti": record.ID,
		"iss": origin, "aud": resource, "resource": resource, "exp": record.ExpiresAt, "iat": now, "kid": s.jwtKeyID(),
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
	token, replacement, err := s.issueManagedMCPTokenWithMode(record.ClientID, remainingDays, s.origin(r), s.resource(r), accessMode, record.ProfileID)
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
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	query := r.URL.Query()
	limit := 100
	offset := 0
	var err error
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "limit must be between 1 and 500"})
			return
		}
	}
	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "offset must be non-negative"})
			return
		}
	}
	s.mu.Lock()
	all := append([]auditEvent(nil), s.audit...)
	s.mu.Unlock()
	nameFilter := strings.TrimSpace(query.Get("name"))
	actorFilter := strings.TrimSpace(query.Get("actor"))
	targetFilter := strings.TrimSpace(query.Get("target"))
	textFilter := strings.ToLower(strings.TrimSpace(query.Get("q")))
	filtered := make([]auditEvent, 0, len(all))
	for _, event := range all {
		if nameFilter != "" && event.Name != nameFilter {
			continue
		}
		if actorFilter != "" && firstString(event.Fields, "actor", "client_id", "subject") != actorFilter {
			continue
		}
		if targetFilter != "" && firstString(event.Fields, "target", "server_id", "agent_id") != targetFilter {
			continue
		}
		if textFilter != "" {
			encoded, _ := json.Marshal(event)
			if !strings.Contains(strings.ToLower(string(encoded)), textFilter) {
				continue
			}
		}
		filtered = append(filtered, event)
	}
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := filtered[offset:end]
	response := map[string]any{"events": items, "count": len(items), "total": total, "offset": offset, "audit_log": "durable-jsonl"}
	if end < total {
		response["next_offset"] = end
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) adminApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	s.mu.Lock()
	items := s.approvalSnapshotLocked()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"approvals": items})
}

func (s *Server) adminApproval(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/approvals/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid approval id"})
		return
	}
	s.mu.Lock()
	approval, ok := s.approvals[id]
	if ok && approval.Status == "pending" && !s.now().Before(approval.ExpiresAt) {
		approval.Status = "expired"
	}
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "approval not found"})
		return
	}
	if r.Method == http.MethodGet {
		result := *approval
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, result)
		return
	}
	if r.Method != http.MethodPost {
		s.mu.Unlock()
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := readJSON(r, &req); err != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if approval.Status != "pending" {
		status := approval.Status
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "approval is no longer pending", "status": status})
		return
	}
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "approve":
		approval.Status = "approved"
	case "reject":
		approval.Status = "rejected"
	default:
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "action must be approve or reject"})
		return
	}
	result := *approval
	s.mu.Unlock()
	s.addApprovalAudit(result)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) approvalSnapshotLocked() []approvalRequest {
	items := make([]approvalRequest, 0, len(s.approvals))
	now := s.now()
	for _, approval := range s.approvals {
		if approval.Status == "pending" && !now.Before(approval.ExpiresAt) {
			approval.Status = "expired"
		}
		items = append(items, *approval)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items
}

func (s *Server) addApprovalAudit(approval approvalRequest) {
	s.mu.Lock()
	s.addAuditLocked("approval_"+approval.Status, map[string]any{
		"approval_id": approval.ID, "profile_id": approval.ProfileID, "actor": approval.Actor,
		"target": approval.Target, "tool": approval.Tool, "arguments_digest": approval.ArgumentsDigest,
	})
	s.mu.Unlock()
}

func (s *Server) adminRotateOAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if !s.requireSensitiveSecurityReauth(w, r) {
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

func (s *Server) adminSecurityHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if !s.requireSensitiveSecurityReauth(w, r) {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil || req.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "enabled boolean is required"})
		return
	}
	if strings.TrimSpace(s.cfg.EnvFile) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "env file is not configured"})
		return
	}
	value := "0"
	if *req.Enabled {
		value = "1"
	}
	if err := replaceEnvValue(s.cfg.EnvFile, "SHELLMCP_HEARTBEAT", value); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist heartbeat setting"})
		return
	}
	s.addSecurityAudit("security_heartbeat_changed", map[string]any{"enabled": *req.Enabled, "restart_required": true})
	writeJSON(w, http.StatusOK, map[string]any{"enabled": *req.Enabled, "restart_required": true, "message": "Restart the ShellMCP service to apply the setting."})
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
	clients := make([]managedMCPToken, 0, len(s.managedMCP)+len(s.oauthClients)+1)
	for _, record := range s.managedMCP {
		clients = append(clients, record)
	}
	for clientID, metadata := range s.oauthClients {
		clients = append(clients, oauthClientInventory(metadata, clientID))
	}
	if s.cfg.CtlToken != "" && s.legacyCtlTokenAllowed() {
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
	counts := map[string]int{"online": 0, "offline": 0, "stale": 0, "awaiting_approval": 0}
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

// adminLegacyStatic keeps the operational console available while the React
// policy console owns the primary /admin/ entrypoint. It deliberately shares
// the admin session gate and API origin with the primary UI so legacy tools do
// not require a second credential or a hidden bearer token.
func (s *Server) adminLegacyStatic(w http.ResponseWriter, r *http.Request) {
	if !s.adminSessionValid(r) {
		if wantsHTML(r) || strings.HasPrefix(r.URL.Path, "/admin/legacy/") {
			s.renderAdminLogin(w, r, "")
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	root := filepath.Join(s.cfg.PublicDir, "admin-legacy")
	fs := http.StripPrefix("/admin/legacy/", http.FileServer(http.Dir(root)))
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
			if s.authFailureRateLimited(w, r) {
				return
			}
			s.renderAdminLogin(w, r, "неверный пароль")
			return
		}
		if s.securityRequiresMFA() && !s.verifyAdminMFARequest(r, r.FormValue("mfa_code")) {
			s.authAudit("admin_login_denied", r, map[string]any{"reason": "mfa_required_or_invalid"})
			if s.authFailureRateLimited(w, r) {
				return
			}
			s.renderAdminLogin(w, r, "нужен корректный MFA-код")
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
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
	next := safeAdminNext(r.URL.Query().Get("next"))
	if next == "/admin/login" || next == "/admin/logout" {
		next = "/admin/"
	}
	errHTML := ""
	if errMsg != "" {
		errHTML = `<div class="err">` + html.EscapeString(errMsg) + `</div>`
	}
	mfaHTML := ""
	if s.securityRequiresMFA() {
		passkeyEnrolled := s.webAuthnEnrolled()
		mfaRequired := " required"
		if passkeyEnrolled {
			mfaRequired = ""
		}
		mfaHTML = `<label for="mfa_code">MFA-код</label><input id="mfa_code" name="mfa_code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}"` + mfaRequired + `>`
		if passkeyEnrolled {
			mfaHTML += `<button id="webauthn-login" type="button">Войти с passkey</button><div id="webauthn-status" class="foot" role="status" aria-live="polite"></div><script>
(function () {
  const form = document.getElementById('login-form');
  const button = document.getElementById('webauthn-login');
  const status = document.getElementById('webauthn-status');
  const decode = (value) => Uint8Array.from(atob(value.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - value.length % 4) % 4)), (char) => char.charCodeAt(0));
  const encode = (value) => { let binary = ''; new Uint8Array(value).forEach((byte) => { binary += String.fromCharCode(byte); }); return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, ''); };
  const fail = (error) => { status.textContent = error instanceof Error ? error.message : 'Passkey не подтверждён'; button.disabled = false; };
  button.addEventListener('click', async () => {
    button.disabled = true;
    status.textContent = 'Подтвердите passkey в браузере';
    try {
      if (!window.PublicKeyCredential || !navigator.credentials) throw new Error('Этот браузер не поддерживает passkey');
      const begin = await fetch('/admin/api/security/mfa/webauthn/login/begin', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      if (!begin.ok) throw new Error('Не удалось начать проверку passkey');
      const options = (await begin.json()).publicKey;
      options.challenge = decode(options.challenge);
      if (options.allowCredentials) options.allowCredentials = options.allowCredentials.map((item) => ({ ...item, id: decode(item.id) }));
      const credential = await navigator.credentials.get({ publicKey: options });
      if (!credential) throw new Error('Passkey не выбран');
      const response = credential.response;
      const finish = await fetch('/admin/api/security/mfa/webauthn/login/finish', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: credential.id, rawId: encode(credential.rawId), response: { clientDataJSON: encode(response.clientDataJSON), authenticatorData: encode(response.authenticatorData), signature: encode(response.signature), userHandle: response.userHandle ? encode(response.userHandle) : null }, type: credential.type }) });
      if (!finish.ok) throw new Error('Сервер отклонил passkey');
      document.getElementById('mfa_code').required = false;
      form.submit();
    } catch (error) { fail(error); }
  });
}());
</script>`
		}
	}
	page := `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>GPTAdmin Login</title><style>
:root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(circle at 20% 0,#1d2b64 0,#090d18 36%,#05070c 100%);color:#e5eefc;font-family:Inter,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.card{width:min(460px,calc(100vw - 32px));padding:30px;border:1px solid rgba(148,163,184,.24);border-radius:26px;background:rgba(15,23,42,.86);box-shadow:0 24px 80px rgba(0,0,0,.42);backdrop-filter:blur(16px)}h1{margin:0 0 8px;font-size:28px}.muted{margin:0 0 22px;color:#94a3b8;line-height:1.45}.hint{margin:0 0 18px;padding:12px 14px;border-radius:16px;background:rgba(56,189,248,.08);border:1px solid rgba(56,189,248,.18);color:#cbd5e1;line-height:1.45}.hint code{color:#fff}.err{margin:0 0 14px;padding:10px 12px;border-radius:14px;background:rgba(239,68,68,.14);border:1px solid rgba(239,68,68,.35);color:#fecaca}label{display:block;margin-bottom:8px;color:#cbd5e1;font-size:14px}input,button{width:100%;padding:14px 15px;border-radius:16px;font-size:16px}input{border:1px solid #334155;background:#0b1220;color:#fff;outline:none}input:focus{border-color:#38bdf8;box-shadow:0 0 0 3px rgba(56,189,248,.16)}button{margin-top:14px;border:0;background:linear-gradient(135deg,#7c3aed,#06b6d4);color:white;font-weight:800;cursor:pointer}.foot{margin-top:16px;color:#64748b;font-size:12px;text-align:center}</style></head><body><main class="card"><h1>GPTAdmin</h1><p class="muted">Войдите с <strong>AdminPassword</strong>. Без cookie-сессии админка и её API не отдаются.</p><div class="hint">Для настройки MCP client используйте Connect.</div>` + errHTML + `<form id="login-form" method="post" action="/admin/login"><input type="hidden" name="next" value="` + html.EscapeString(next) + `"><label for="password">Пароль</label><input id="password" name="password" type="password" autocomplete="current-password" autofocus required>` + mfaHTML + `<button type="submit">Войти</button></form><div class="foot">session cookie · 12h</div></main></body></html>`
	_, _ = io.WriteString(w, page)
}

func (s *Server) adminSessionValid(r *http.Request) bool {
	if s.cfg.AdminPassword == "" {
		return true
	}
	for _, cookie := range r.Cookies() {
		if cookie.Name != adminSessionCookieName || cookie.Value == "" {
			continue
		}
		parts := strings.Split(cookie.Value, ".")
		if len(parts) != 2 {
			continue
		}
		exp, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || exp < time.Now().Unix() {
			continue
		}
		mac, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		want := s.adminSessionMAC(parts[0])
		if hmac.Equal(mac, want) {
			return true
		}
	}
	return false
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
	event := auditEvent{Time: time.Now().Format(time.RFC3339), Name: name, Fields: fields}
	s.audit = append(s.audit, event)
	s.enqueueTelemetryAudit(event)
	if len(s.audit) > 500 {
		s.audit = s.audit[len(s.audit)-500:]
	}
	if path := s.auditStatePath(); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			log.Printf("audit state directory failed path=%s err=%v", path, err)
			return
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			log.Printf("audit state append failed path=%s err=%v", path, err)
			return
		}
		_ = file.Chmod(0o600)
		if err := json.NewEncoder(file).Encode(event); err != nil {
			log.Printf("audit state encode failed path=%s err=%v", path, err)
		}
		if err := file.Close(); err != nil {
			log.Printf("audit state close failed path=%s err=%v", path, err)
		}
	}
}

func (s *Server) auditStatePath() string {
	if s.cfg.AuditStateFile != "" {
		return s.cfg.AuditStateFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "audit.jsonl")
}

func (s *Server) loadAuditState() error {
	path := s.auditStatePath()
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		var event auditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode audit event: %w", err)
		}
		s.audit = append(s.audit, event)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(s.audit) > 500 {
		s.audit = s.audit[len(s.audit)-500:]
	}
	return nil
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
	if r != nil && (r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	host := ""
	if r != nil {
		host = r.Host
		if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xf != "" {
			host = xf
		}
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
		"authorization_endpoint":                origin + "/oauth/authorize",
		"token_endpoint":                        origin + "/oauth/token",
		"registration_endpoint":                 origin + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
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
	if err := readOAuthRegistrationJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_client_metadata", "error_description": err.Error()})
		return
	}
	redirectURIs, err := oauthRedirectURIsFromRequest(req["redirect_uris"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_client_metadata", "error_description": err.Error()})
		return
	}
	clientID := "gptadmin-" + newID()
	clientSecret := newID()
	s.mu.Lock()
	if len(s.oauthClients) >= oauthClientsMaxItems {
		s.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "temporarily_unavailable", "error_description": "OAuth client registry is full"})
		return
	}
	s.oauthClients[clientID] = oauthClientMetadata{RedirectURIs: redirectURIs, CreatedAt: time.Now().Unix()}
	if err := s.saveOAuthClientsStateLocked(); err != nil {
		delete(s.oauthClients, clientID)
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "server_error", "error_description": "failed to persist OAuth client metadata"})
		return
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_secret":              clientSecret,
		"client_id_issued_at":        time.Now().Unix(),
		"client_secret_expires_at":   0,
		"redirect_uris":              redirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
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
	if !s.oauthClientAllowsRedirect(q.Get("client_id"), redirectURI) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": "redirect_uri is not registered for client"})
		return
	}
	if strings.TrimSpace(q.Get("code_challenge")) == "" || q.Get("code_challenge_method") != "S256" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": "PKCE S256 is required"})
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
	page := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize GPTAdmin MCP</title><style>body{font-family:system-ui,sans-serif;background:#070a12;color:#e5eefc;display:grid;place-items:center;min-height:100vh;margin:0}.card{max-width:560px;padding:28px;border:1px solid #1e293b;border-radius:24px;background:#0f1623}.hint{margin:16px 0;padding:12px 14px;border-radius:16px;background:rgba(56,189,248,.08);border:1px solid rgba(56,189,248,.18);color:#cbd5e1;line-height:1.45}.hint code{color:#fff}input,button{width:100%;box-sizing:border-box;padding:14px;border-radius:14px;margin-top:10px}input{background:#111827;color:#fff;border:1px solid #334155}button{border:0;background:linear-gradient(135deg,#7c3aed,#06b6d4);color:#fff;font-weight:800}.muted{color:#94a3b8;word-break:break-all}</style></head><body><main class="card"><h1>Authorize GPTAdmin MCP</h1><p class="muted">Client: ` + html.EscapeString(q.Get("client_id")) + `</p><p class="muted">Resource: ` + html.EscapeString(resource) + `</p><p>Scopes: ` + html.EscapeString(scope) + `</p><div class="hint">Эта страница выпускает Bearer JWT для MCP/Custom GPT. Используйте OAuth или готовый Bearer JWT, выпущенный через Hub; credential values никогда не показываются на этой странице.</div><form method="POST" action="/oauth/authorize">` + hidden + `<label>Admin password</label><input type="password" name="password" autofocus required autocomplete="current-password"><button type="submit">Authorize</button></form></main></body></html>`
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
	if !s.oauthClientAllowsRedirect(r.Form.Get("client_id"), redirectURI) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": "redirect_uri is not registered for client"})
		return
	}
	if strings.TrimSpace(r.Form.Get("code_challenge")) == "" || r.Form.Get("code_challenge_method") != "S256" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": "PKCE S256 is required"})
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
	grantType := strings.TrimSpace(r.Form.Get("grant_type"))
	if grantType == "refresh_token" {
		s.oauthRefreshToken(w, r)
		return
	}
	if grantType != "" && grantType != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
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
	if r.Form.Get("client_id") != data.ClientID || r.Form.Get("redirect_uri") != data.RedirectURI {
		s.authAudit("oauth_token_denied", r, map[string]any{"reason": "client or redirect mismatch", "client_id": data.ClientID, "form": s.formForAudit(r)})
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "client or redirect mismatch"})
		return
	}
	if data.Challenge != "" && !pkceOK(r.Form.Get("code_verifier"), data.Challenge) {
		s.authAudit("oauth_token_denied", r, map[string]any{"reason": "PKCE verification failed", "client_id": data.ClientID, "resource": resource, "form": s.formForAudit(r)})
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "PKCE verification failed"})
		return
	}
	token, err := s.issueOAuthAccessToken(data.ClientID, resource, data.Scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	refreshToken, refreshRecord, err := s.issueOAuthRefreshToken(data.ClientID, resource, data.Scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.addAuditLocked("oauth_token_issued", map[string]any{"client_id": data.ClientID, "scope": data.Scope, "resource": resource})
	s.mu.Unlock()
	s.authAudit("oauth_token_ok", r, map[string]any{"client_id": data.ClientID, "scope": data.Scope, "resource": resource, "access_token": s.secretForAudit(token), "jwt_claims": decodeJWTClaimsUnverified(token), "form": s.formForAudit(r)})
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 43200, "refresh_token": refreshToken, "refresh_token_expires_in": refreshRecord.ExpiresAt - s.now().Unix()})
}

// oauthRefreshToken rotates a durable, digest-only refresh credential and
// issues a short-lived access JWT for the same client and resource.
func (s *Server) oauthRefreshToken(w http.ResponseWriter, r *http.Request) {
	resource := strings.TrimRight(r.Form.Get("resource"), "/")
	clientID := strings.TrimSpace(r.Form.Get("client_id"))
	refreshToken := strings.TrimSpace(r.Form.Get("refresh_token"))
	record, ok := s.oauthRefreshTokenRecord(refreshToken, clientID, resource)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "refresh token is invalid, expired, or belongs to a different client"})
		return
	}
	accessToken, err := s.issueOAuthAccessToken(record.ClientID, record.Audience, record.Scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	newRefreshToken, newRecord, err := newOAuthRefreshToken(record.ClientID, record.Audience, record.Scope, s.now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !s.rotateOAuthRefreshToken(refreshToken, record, newRecord) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "refresh token is no longer valid"})
		return
	}
	s.authAudit("oauth_refresh_ok", r, map[string]any{"client_id": record.ClientID, "resource": record.Audience, "scope": record.Scope, "refresh_token": s.secretForAudit(refreshToken)})
	writeJSON(w, http.StatusOK, map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 43200, "refresh_token": newRefreshToken, "refresh_token_expires_in": newRecord.ExpiresAt - s.now().Unix()})
}

func (s *Server) issueOAuthAccessToken(clientID, resource, scope string) (string, error) {
	now := s.now()
	claims := map[string]any{"sub": "admin", "scope": scope, "client_id": clientID, "iss": strings.TrimRight(s.cfg.PublicOrigin, "/"), "aud": resource, "resource": resource, "exp": now.Add(12 * time.Hour).Unix(), "iat": now.Unix(), "kid": s.jwtKeyID()}
	if claims["iss"] == "" {
		claims["iss"] = resource
	}
	if profileID := s.oauthClientProfileID(clientID); profileID != "" {
		claims["profile_id"] = profileID
	}
	return s.signJWT(claims)
}

func (s *Server) issueOAuthRefreshToken(clientID, resource, scope string) (string, managedMCPToken, error) {
	token, record, err := newOAuthRefreshToken(clientID, resource, scope, s.now())
	if err != nil {
		return "", managedMCPToken{}, err
	}
	s.mu.Lock()
	s.managedMCP[record.ID] = record
	err = s.saveManagedMCPStateLocked()
	s.mu.Unlock()
	if err != nil {
		return "", managedMCPToken{}, err
	}
	return token, record, nil
}

func newOAuthRefreshToken(clientID, resource, scope string, now time.Time) (string, managedMCPToken, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", managedMCPToken{}, err
	}
	record := managedMCPToken{ID: newID(), ClientID: clientID, TokenKind: "oauth_refresh", Audience: resource, Scope: scope, IssuedAt: now.Unix(), ExpiresAt: now.AddDate(5, 0, 0).Unix()}
	token := "gptr_" + record.ID + "_" + base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	record.TokenDigest = hex.EncodeToString(digest[:])
	return token, record, nil
}

func (s *Server) oauthRefreshTokenRecord(token, clientID, resource string) (managedMCPToken, bool) {
	parts := strings.SplitN(token, "_", 3)
	if len(parts) != 3 || parts[0] != "gptr" || parts[1] == "" || parts[2] == "" || clientID == "" {
		return managedMCPToken{}, false
	}
	digest := sha256.Sum256([]byte(token))
	now := s.now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.managedMCP[parts[1]]
	if !ok || record.TokenKind != "oauth_refresh" || record.RevokedAt != 0 || record.ExpiresAt <= now || record.ClientID != clientID || (resource != "" && strings.TrimRight(record.Audience, "/") != resource) || record.TokenDigest == "" || !hmac.Equal([]byte(record.TokenDigest), []byte(hex.EncodeToString(digest[:]))) {
		return managedMCPToken{}, false
	}
	return record, true
}

func (s *Server) rotateOAuthRefreshToken(token string, oldRecord, newRecord managedMCPToken) bool {
	digest := sha256.Sum256([]byte(token))
	now := s.now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.managedMCP[oldRecord.ID]
	if !ok || stored.TokenKind != "oauth_refresh" || stored.RevokedAt != 0 || stored.ExpiresAt <= now || stored.TokenDigest == "" || !hmac.Equal([]byte(stored.TokenDigest), []byte(hex.EncodeToString(digest[:]))) {
		return false
	}
	stored.RevokedAt = now
	s.managedMCP[stored.ID] = stored
	s.managedMCP[newRecord.ID] = newRecord
	if err := s.saveManagedMCPStateLocked(); err != nil {
		s.managedMCP[stored.ID] = oldRecord
		delete(s.managedMCP, newRecord.ID)
		return false
	}
	return true
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
	callArgs, approvalID := approvalArguments(args)
	if approvalResponse, blocked := s.approvalGate(r, agent.AgentID, toolName, callArgs, approvalID); blocked {
		writeJSON(w, http.StatusPreconditionRequired, approvalResponse)
		return
	}
	if budgetResponse, blocked := s.boundedAutonomousGate(r, agent.AgentID, toolName); blocked {
		writeJSON(w, http.StatusTooManyRequests, budgetResponse)
		return
	}
	result, rpcErr := s.agentToolCall(r, agent, toolName, callArgs)
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
		if agent.AgentID == "hub" || strings.HasPrefix(agent.AgentID, "shell:") {
			return map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}, "resources": map[string]any{}, "prompts": map[string]any{}}, "serverInfo": map[string]any{"name": "gptadmin-server-" + agentSlug(agent.AgentID), "version": BuildVersion}, "instructions": s.startupInstructionsTextForRequest(r)}, nil, false
		}
		jobID := s.enqueueRelay(agent.AgentID, method, params)
		result, rpcErr := unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
		return result, rpcErr, false
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
		callArgs, approvalID := approvalArguments(args)
		if approvalResponse, blocked := s.approvalGate(r, agent.AgentID, name, callArgs, approvalID); blocked {
			return nil, map[string]any{"code": -32004, "message": "approval required", "data": approvalResponse}, false
		}
		if budgetResponse, blocked := s.boundedAutonomousGate(r, agent.AgentID, name); blocked {
			return nil, map[string]any{"code": -32005, "message": "bounded autonomous budget exhausted", "data": budgetResponse}, false
		}
		result, err := s.agentToolCall(r, agent, name, callArgs)
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

func (s *Server) agentToolCall(r *http.Request, agent Agent, name string, args map[string]any) (any, any) {
	if agent.AgentID == "hub" {
		return mcpToolResult(s.appsSDKCall(name, args)), nil
	}
	if strings.HasPrefix(agent.AgentID, "shell:") {
		return unwrapMCPUpstream(s.callShellToolWithTraceParent(agent.AgentID, name, args, false, s.cfg.DefaultTimeout, requestTraceID(r), requestTraceParent(r)))
	}
	jobID := s.enqueueRelayWithTraceParent(agent.AgentID, "tools/call", map[string]any{"name": name, "arguments": args}, requestTraceID(r), requestTraceParent(r))
	return unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
}

func (s *Server) agentResourcesList(r *http.Request, agent Agent) (any, any) {
	if agent.AgentID == "hub" {
		return s.appsSDKResourcesList(), nil
	}
	if strings.HasPrefix(agent.AgentID, "shell:") {
		return map[string]any{"resources": []map[string]any{
			{"uri": startupInstructionsResourceURI, "name": "GPTAdmin startup instructions", "mimeType": "text/markdown"},
			{"uri": "gptadmin://server/" + agentSlug(agent.AgentID), "name": agent.Name + " card", "mimeType": "application/json"},
		}}, nil
	}
	if !hasCapability(agent, "resources/list") {
		return map[string]any{"resources": []map[string]any{{"uri": "gptadmin://server/" + agentSlug(agent.AgentID), "name": agent.Name + " card", "mimeType": "application/json"}}}, nil
	}
	jobID := s.enqueueRelay(agent.AgentID, "resources/list", map[string]any{})
	return unwrapMCPUpstream(s.waitRelay(jobID, s.cfg.DefaultTimeout))
}

func (s *Server) agentResourceRead(r *http.Request, agent Agent, uri string) (any, any) {
	if agent.AgentID == "hub" {
		return s.appsSDKResourceRead(r, uri), nil
	}
	if strings.HasPrefix(agent.AgentID, "shell:") {
		if uri == startupInstructionsResourceURI {
			return s.startupInstructionsResourceRead(r, uri), nil
		}
		b, _ := json.Marshal(s.agentCard(r, agent))
		return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(b)}}}, nil
	}
	if strings.HasPrefix(uri, "gptadmin://server/") || strings.HasPrefix(uri, "gptadmin://agent/") || !hasCapability(agent, "resources/read") {
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
		result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}, "resources": map[string]any{}}, "serverInfo": map[string]any{"name": "gptadmin-go-hub", "version": BuildVersion}, "instructions": s.startupInstructionsTextForRequest(r)}
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
			s.recordActivationTelemetry("failure")
			s.auditToolDecision(r, "hub", name, args, "deny", err.Error(), nil, http.StatusForbidden)
			rpcErr = map[string]any{"code": -32003, "message": err.Error()}
		} else {
			s.recordActivationTelemetry("first_tool")
			structured := s.appsSDKCallForRequest(r, name, args)
			result = mcpToolResult(structured)
			s.auditToolDecision(r, "hub", name, args, "allow", "", map[string]any{}, http.StatusOK)
		}
	case "resources/list":
		result = s.appsSDKResourcesList()
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

func (s *Server) bridgeKeyMatches(key string) bool {
	if s.cfg.BridgeKey == "" || key != s.cfg.BridgeKey {
		return false
	}
	// The default bridge key is the legacy CTL bearer. A separately configured
	// bridge credential remains valid after the migration deadline.
	return s.cfg.BridgeKey != s.cfg.CtlToken || s.legacyCtlTokenAllowed()
}

func (s *Server) mcpPrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.bridgeKeyMatches(r.URL.Query().Get("key")) {
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
	if !s.bridgeKeyMatches(r.URL.Query().Get("key")) {
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
	// The legacy bridge key is an ingress credential, not a scoped MCP client.
	// Treat bridge calls as read-only until the caller migrates to an OAuth
	// connection with an explicit access profile; otherwise this endpoint would
	// invoke the executor with a nil request and bypass policy gates.
	bridgeRequest := requestWithAuthClaims(r, map[string]any{
		"sub":         "legacy-bridge",
		"scope":       "gptadmin.read",
		"access_mode": accessModeReadonly,
	})
	if err := authorizeFacadeCall(bridgeRequest, tool, args); err != nil {
		s.auditToolDecision(bridgeRequest, "bridge", tool, args, "deny", err.Error(), nil, http.StatusForbidden)
		writeJSON(w, http.StatusForbidden, map[string]any{"status": "failed", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "result": s.appsSDKCallForRequest(bridgeRequest, tool, args)})
}

func (s *Server) appsSDKCall(name string, args map[string]any) any {
	switch name {
	case "secret_request", "secret_status":
		return s.secretToolForRequest(nil, name, args)
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
	case "pending", "list_pending_servers", "approve_pending_server":
		result, _ := s.callHubToolForRequest(nil, name, args)
		return result
	case "demo":
		result, _ := s.callHubTool(name, args)
		return result
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
			return withSchemaContractMetadata(map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": hubTools()}})
		}
		if strings.HasPrefix(target, "shell:") {
			return withSchemaContractMetadata(map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": shellTools()}})
		}
		jobID := s.enqueueRelay(target, "tools/list", map[string]any{})
		return withSchemaContractMetadata(s.waitRelay(jobID, s.cfg.DefaultTimeout))
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
	if name == "secret_request" || name == "secret_status" {
		return s.secretToolForRequest(r, name, args)
	}
	if name == "demo" {
		result, _ := s.callHubToolForRequest(r, name, args)
		return result
	}
	if name == "schema" || name == "list_mcp_tools" || name == "listMcpTools" {
		if err := authorizeFacadeCall(r, name, args); err != nil {
			return map[string]any{"server_id": firstString(args, "target", "server_id", "agent_id"), "status": "failed", "error": err.Error()}
		}
		requestedTarget := firstString(args, "target", "server_id", "agent_id")
		if requestAccessMode(r) == accessModeReadonly && requestedTarget != "hub" && !strings.HasPrefix(requestedTarget, "shell:") {
			return map[string]any{"server_id": requestedTarget, "status": "completed", "response": map[string]any{"tools": []map[string]any{}}}
		}
		return s.appsSDKSchemaForRequest(r, args)
	}
	if name == "inspect" || name == "inspect_system" || name == "inspectSystem" {
		if err := authorizeFacadeCall(r, name, args); err != nil {
			return map[string]any{"server_id": firstString(args, "target", "server_id", "agent_id"), "status": "failed", "error": err.Error()}
		}
		target := firstString(args, "target", "server_id", "agent_id")
		selectedTarget, status, detail := s.selectMCPRelayTarget(target)
		if status != http.StatusOK {
			return map[string]any{"server_id": target, "status": "failed", "error": map[string]any{"status_code": status, "message": detail}}
		}
		if !strings.HasPrefix(selectedTarget, "shell:") {
			return map[string]any{"server_id": selectedTarget, "status": "failed", "error": "system inspection requires a shell:* target"}
		}
		response, responseStatus := s.executeMCPTool(r, selectedTarget, "system_inspect", toolArgsFromTopLevel(args), false, s.cfg.DefaultTimeout, "")
		if responseStatus >= http.StatusBadRequest {
			return map[string]any{"server_id": selectedTarget, "status": "failed", "error": response}
		}
		return response
	}
	if requestAccessMode(r) == accessModeReadonly && (name == "schema" || name == "list_mcp_tools" || name == "listMcpTools") {
		target := firstString(args, "target", "server_id", "agent_id")
		if target != "hub" && !strings.HasPrefix(target, "shell:") {
			return map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": []map[string]any{}}}
		}
	}
	if name == "call_mcp_tool" || name == "callMcpTool" {
		return s.appsSDKCallMCP(r, name, args)
	}
	if name == "execute" {
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

func (s *Server) appsSDKSchemaForRequest(r *http.Request, args map[string]any) any {
	target := firstString(args, "target", "server_id", "agent_id")
	selectedTarget, status, detail := s.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		return map[string]any{"server_id": target, "status": "failed", "error": map[string]any{"status_code": status, "message": detail}}
	}
	target = selectedTarget
	var result map[string]any
	if target == "hub" {
		result = map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": hubTools()}}
	} else if strings.HasPrefix(target, "shell:") {
		result = map[string]any{"server_id": target, "status": "completed", "response": map[string]any{"tools": shellTools()}}
	} else {
		jobID := s.enqueueRelay(target, "tools/list", map[string]any{})
		result = s.waitRelay(jobID, s.cfg.DefaultTimeout)
	}
	if response, ok := result["response"].(map[string]any); ok {
		if raw, ok := response["tools"].([]map[string]any); ok {
			response["tools"] = toolsForRequest(r, target, raw)
		}
	}
	return withSchemaContractMetadata(result)
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
	if response, blocked := s.validateSchemaContract(r, selectedTarget, args); blocked {
		return response
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
	tools := []map[string]any{
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
			"name":            "demo",
			"title":           "Safe connection check",
			"description":     "Run a read-only connection check without shell execution, file access or credentials.",
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			"outputSchema":    map[string]any{"type": "object", "additionalProperties": true},
			"annotations":     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
			"securitySchemes": readSecurity,
			"_meta":           readMeta,
		},
		{
			"name":            "approve_pending_server",
			"title":           "Approve ShellMCP device",
			"description":     "Approve one ShellMCP device awaiting enrollment. Use the exact server_id returned by discover with detail=full or pending.",
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{"server_id": map[string]any{"type": "string"}}, "required": []string{"server_id"}, "additionalProperties": false},
			"outputSchema":    map[string]any{"type": "object", "additionalProperties": true},
			"annotations":     map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false},
			"securitySchemes": execSecurity,
			"_meta":           execMeta,
		},
		{
			"name":            "schema",
			"title":           "Schema",
			"description":     "List tools for one target selected by discover. Never use target=default.",
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string"}}, "required": []string{"target"}, "additionalProperties": false},
			"outputSchema":    map[string]any{"type": "object", "properties": map[string]any{"server_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "response": map[string]any{"type": "object", "description": "Includes schema_version and schema_digest_sha256 when listing tools.", "additionalProperties": true}}, "additionalProperties": true},
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
			"inputSchema":     map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string"}, "tool": map[string]any{"type": "string"}, "args": map[string]any{"type": "object", "additionalProperties": true}, "background": map[string]any{"type": "boolean"}, "idempotency_key": map[string]any{"type": "string", "minLength": 1, "maxLength": idempotencyKeyMax}, "schema_version": map[string]any{"type": "string"}, "schema_digest_sha256": map[string]any{"type": "string", "minLength": 64, "maxLength": 64, "pattern": "^[a-f0-9]{64}$"}}, "required": []string{"target", "tool"}, "additionalProperties": true},
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
	return append(tools, secretAppsTools()...)
}

const startupInstructionsResourceURI = "gptadmin://startup-instructions"

func (s *Server) appsSDKResourcesList() map[string]any {
	return map[string]any{"resources": []map[string]any{
		{"uri": startupInstructionsResourceURI, "name": "GPTAdmin startup instructions", "description": "Operational guidance for GPTAdmin system administration; permissions and approvals remain authoritative.", "mimeType": "text/markdown"},
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
	if uri == startupInstructionsResourceURI {
		return s.startupInstructionsResourceRead(r, uri)
	}
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

func (s *Server) startupInstructionsResourceRead(r *http.Request, uri string) map[string]any {
	return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "text/markdown", "text": s.startupInstructionsTextForRequest(r)}}}
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
	return s.verifyJWTForRequest(r, tok)
}

func (s *Server) verifyJWTForRequest(r *http.Request, token string) (map[string]any, error) {
	claims, err := s.verifyJWT(token)
	if err != nil {
		return nil, err
	}
	expected := strings.TrimRight(s.resource(r), "/")
	if expected == "" || !jwtAudienceMatches(claims["aud"], expected) {
		return nil, errors.New("token audience does not match this Hub")
	}
	resource, ok := claims["resource"].(string)
	if !ok || strings.TrimRight(resource, "/") != expected {
		return nil, errors.New("token resource does not match this Hub")
	}
	if scope, ok := claims["scope"].(string); !ok || !validJWTScopes(scope) {
		return nil, errors.New("token scope is invalid")
	}
	if sub, ok := claims["sub"].(string); !ok || strings.TrimSpace(sub) == "" {
		return nil, errors.New("token subject is required")
	}
	if iat := intFromAny(claims["iat"]); iat <= 0 || int64(iat) > time.Now().Unix()+60 {
		return nil, errors.New("token issued-at is invalid")
	}
	if kid, ok := claims["kid"].(string); !ok || strings.TrimSpace(kid) == "" || kid != s.jwtKeyID() {
		return nil, errors.New("token key id is invalid")
	}
	return claims, nil
}

func (s *Server) jwtKeyID() string {
	if strings.TrimSpace(s.cfg.OAuthKeyID) != "" {
		return strings.TrimSpace(s.cfg.OAuthKeyID)
	}
	return defaultJWTKeyID
}

func validJWTScopes(value string) bool {
	for _, scope := range strings.Fields(value) {
		if scope != "gptadmin.read" && scope != "gptadmin.inspect" && scope != "gptadmin.exec" {
			return false
		}
	}
	return strings.TrimSpace(value) != ""
}

func jwtAudienceMatches(value any, expected string) bool {
	switch audience := value.(type) {
	case string:
		return strings.TrimRight(audience, "/") == expected
	case []any:
		for _, item := range audience {
			if candidate, ok := item.(string); ok && strings.TrimRight(candidate, "/") == expected {
				return true
			}
		}
	}
	return false
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
			s.markLegacyCtlToken(w)
			if !s.legacyCtlTokenAllowed() {
				s.authAudit("mcp_auth_denied", r, map[string]any{"reason": "legacy ctl token migration deadline passed"})
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.origin(r)+`/.well-known/oauth-protected-resource", scope="gptadmin.read gptadmin.exec"`)
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "legacy token expired; use OAuth connection"})
				return false
			}
			s.authAudit("mcp_auth_ok", r, map[string]any{"auth_kind": "ctl_token"})
			return true
		}
		if claims, err := s.verifyJWTForRequest(r, tok); err == nil {
			s.authAudit("mcp_auth_ok", r, map[string]any{"auth_kind": "oauth_jwt", "jwt_claims": claims})
			*r = *requestWithAuthClaims(r, claims)
			*r = *s.applyAccessProfileContext(r, claims)
			return true
		} else {
			s.authAudit("mcp_auth_denied", r, map[string]any{"reason": err.Error(), "jwt_claims_unverified": decodeJWTClaimsUnverified(tok)})
		}
	} else if auth == "" {
		s.authAudit("mcp_auth_denied", r, map[string]any{"reason": "missing authorization header"})
	} else {
		s.authAudit("mcp_auth_denied", r, map[string]any{"reason": "unsupported authorization scheme"})
	}
	if s.authFailureRateLimited(w, r) {
		return false
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
	if secret == "" && s.legacyCtlTokenAllowed() {
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
	if s.sameOriginOAuthCallback(u) {
		return true
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

func (s *Server) sameOriginOAuthCallback(uri *url.URL) bool {
	if uri == nil || uri.Path != "/connect/callback" || uri.RawQuery != "" || uri.Fragment != "" {
		return false
	}
	origin, err := url.Parse(strings.TrimRight(s.cfg.PublicOrigin, "/"))
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return false
	}
	return uri.Scheme == origin.Scheme && strings.EqualFold(uri.Host, origin.Host)
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
		if known && record.ProfileID != "" {
			claims["profile_id"] = record.ProfileID
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
