package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/audit"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/job"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/output"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/security"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/sshexec"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/system"
)

var BuildVersion = "3"
var GitCommit = "go-shellmcp"

type Config struct {
	Addr                 string
	Token                string
	LogLimit             int64
	ExecTimeout          int
	SpillDir             string
	Name                 string
	BaseURL              string
	HubURL               string
	IdentityDir          string
	HeartbeatEnabled     bool
	HeartbeatInterval    time.Duration
	QueueEnabled         bool
	QueueTimeout         int
	Mode                 string
	OutboxDir            string
	DefaultUser          string
	DefaultHome          string
	DefaultCwd           string
	HubPublicKeyFile     string
	HubPublicKey         string
	AuditLog             string
	NonceTTL             time.Duration
	PreserveFileMetadata bool
	PreserveMetadataMaxFiles int
	MCPConfig            string
	PollInterval         time.Duration
	SSHHost              string
	SSHPort              int
	SSHUser              string
	SSHPassword          string
	SSHKeyPath           string
}

func FromEnv() Config {
	port := env("SHELL_PORT", env("SHELLMCP_PORT", env("PORT", "25900")))
	host := env("SHELL_HOST", env("SHELLMCP_HOST", ""))
	limit, _ := strconv.ParseInt(env("LOG_LIMIT_B", strconv.FormatInt(output.DefaultInlineTailBytes, 10)), 10, 64)
	timeout, _ := strconv.Atoi(env("EXEC_TIMEOUT", "300"))
	spill := env("SHELL_SPOOL_DIR", env("SHELLMCP_SPOOL_DIR", filepath.Join(os.TempDir(), "shellmcp-go-spool")))
	name := env("SHELL_NAME", env("SHELLMCP_NAME", ""))
	baseURL := env("SHELL_URL", env("SHELLMCP_URL", "http://127.0.0.1:"+port))
	hbInt, _ := strconv.Atoi(env("HB_INTERVAL_S", "3600"))
	qTimeout, _ := strconv.Atoi(env("QUEUE_LONG_POLL_TIMEOUT_S", "55"))
	mode := env("SHELL_MODE", env("SHELLMCP_MODE", ""))
	if mode == "" {
		if truthy(env("SHELL_QUEUE", env("SHELLMCP_QUEUE", "0"))) {
			mode = "long_poll"
		} else {
			mode = "webhook"
		}
	}
	outbox := env("SHELL_OUTBOX_DIR", env("SHELLMCP_OUTBOX_DIR", filepath.Join(spill, "outbox")))
	defaultUser := env("SHELL_DEFAULT_USER", env("SHELLMCP_DEFAULT_USER", ""))
	defaultHome := env("SHELL_DEFAULT_HOME", env("SHELLMCP_DEFAULT_HOME", ""))
	defaultCwd := env("SHELL_DEFAULT_CWD", env("SHELLMCP_DEFAULT_CWD", defaultHome))
	auditLogPath := env("SHELLMCP_AUDIT_LOG", "")
	nonceTTL := parseSecondsEnvWithDefault("SHELLMCP_NONCE_TTL_S", 300)
	preserve := truthy(env("SHELLMCP_PRESERVE_FILE_METADATA", ""))
	preserveMax := parseIntEnv("SHELLMCP_PRESERVE_METADATA_MAX_FILES", 1000)
	mcpConfig := env("SHELLMCP_MCP_CONFIG", env("GPTADMIN_MCP_CONFIG", env("GPTADMIN_MCP_AGENTS_DIR", "")))
	pollInterval := parseSecondsEnvWithDefault("POLL_INTERVAL_S", 5)
	sshHost := env("SSH_HOST", "")
	sshPort := 22
	if raw := strings.TrimSpace(os.Getenv("SSH_PORT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			sshPort = n
		}
	}
	sshUser := env("SSH_USER", "")
	sshPassword := os.Getenv("SSH_PASSWORD")
	sshKeyPath := os.Getenv("SSH_KEY_PATH")
	if sshKeyPath == "" {
		sshKeyPath = os.Getenv("SSH_KEY")
	}
	return Config{Addr: host + ":" + port, Token: env("SHELL_TOKEN", env("SHELLMCP_TOKEN", "srv_secret")), LogLimit: limit, ExecTimeout: timeout, SpillDir: spill, Name: name, BaseURL: baseURL, HubURL: strings.TrimRight(env("HUB_URL", ""), "/"), IdentityDir: env("SHELL_IDENTITY_DIR", env("SHELLMCP_IDENTITY_DIR", "/etc/gptadmin")), HeartbeatEnabled: truthy(env("SHELL_HEARTBEAT", env("SHELLMCP_HEARTBEAT", "0"))), HeartbeatInterval: normalizeHeartbeatInterval(hbInt), QueueEnabled: truthy(env("SHELL_QUEUE", env("SHELLMCP_QUEUE", "0"))), QueueTimeout: qTimeout, Mode: mode, OutboxDir: outbox, DefaultUser: defaultUser, DefaultHome: defaultHome, DefaultCwd: defaultCwd, HubPublicKeyFile: env("HUB_PUBLIC_KEY_FILE", filepath.Join(env("SHELL_IDENTITY_DIR", env("SHELLMCP_IDENTITY_DIR", "/etc/gptadmin")), "hub_ed25519.pub")), HubPublicKey: env("HUB_PUBLIC_KEY", ""), AuditLog: auditLogPath, NonceTTL: nonceTTL, PreserveFileMetadata: preserve, PreserveMetadataMaxFiles: preserveMax, MCPConfig: mcpConfig, PollInterval: pollInterval, SSHHost: sshHost, SSHPort: sshPort, SSHUser: sshUser, SSHPassword: sshPassword, SSHKeyPath: sshKeyPath}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func normalizeHeartbeatInterval(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func truthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func parseBuildVersion(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func parseSecondsEnvWithDefault(name string, fallbackSec int) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return time.Duration(fallbackSec) * time.Second
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return time.Duration(fallbackSec) * time.Second
	}
	return time.Duration(n) * time.Second
}

func parseIntEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// firstToken returns the first whitespace-delimited token of s, or s
// itself when there is no whitespace. Used to redact arbitrary shell
// command strings to a searchable prefix in audit logs without leaking
// arguments that may contain secrets.
func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	return s
}

type Server struct {
	cfg         Config
	jobs        *job.Manager
	identity    *security.Identity
	hub         *hub.Client
	auditLog    *audit.Logger
	nonces      *security.NonceCache
	supervisor  *supervisor.Manager
	preserveMeta bool
	preserveMax  int
	sshClient   *sshexec.Client
}

func New(cfg Config) *Server {
	var ident *security.Identity
	if cfg.IdentityDir != "" {
		if id, err := security.LoadIdentity(cfg.IdentityDir, cfg.Name); err == nil {
			ident = id
		} else {
			log.Printf("identity disabled: %v", err)
		}
	}
	if cfg.Name == "" {
		if ident != nil && ident.Name != "" {
			cfg.Name = ident.Name
		} else if h, err := os.Hostname(); err == nil {
			cfg.Name = h
		}
	}
	var hc *hub.Client
	if cfg.HubURL != "" {
		hc = hub.New(cfg.HubURL, ident)
	}

	auditLog, auditErr := audit.New(cfg.AuditLog)
	if auditErr != nil {
		log.Printf("audit logger disabled: %v", auditErr)
	}

	nonces := security.NewNonceCache(cfg.NonceTTL)

	var agents []supervisor.Agent
	if cfg.MCPConfig != "" {
		var loadErr error
		agents, loadErr = supervisor.LoadAgents(cfg.MCPConfig)
		if loadErr != nil {
			log.Printf("supervisor: load agents failed: %v", loadErr)
		}
	}
	mgr := supervisor.New(agents)

	maxFiles := cfg.PreserveMetadataMaxFiles
	if maxFiles <= 0 {
		maxFiles = 1000
	}

	var sshClient *sshexec.Client
	if strings.TrimSpace(cfg.SSHHost) != "" && strings.TrimSpace(cfg.SSHUser) != "" {
		sshCfg := sshexec.Config{
			Host:     cfg.SSHHost,
			Port:     cfg.SSHPort,
			User:     cfg.SSHUser,
			Password: cfg.SSHPassword,
			KeyPath:  cfg.SSHKeyPath,
			Timeout:  parseSecondsEnvWithDefault("SSH_TIMEOUT_S", 300),
		}
		if sshCfg.Port == 0 {
			sshCfg.Port = 22
		}
		client, sshErr := sshexec.New(sshCfg)
		if sshErr != nil {
			log.Printf("ssh client disabled: %v", sshErr)
		} else {
			sshClient = client
			log.Printf("ssh client connected host=%s:%d user=%s", cfg.SSHHost, sshCfg.Port, cfg.SSHUser)
		}
	}

	return &Server{
		cfg:          cfg,
		jobs:         job.New(cfg.LogLimit),
		identity:     ident,
		hub:          hc,
		auditLog:     auditLog,
		nonces:       nonces,
		supervisor:   mgr,
		preserveMeta: cfg.PreserveFileMetadata,
		preserveMax:  maxFiles,
		sshClient:    sshClient,
	}
}

// Close releases the audit logger and best-effort stops every supervisor
// agent. Safe to call multiple times and idempotent w.r.t. nil resources.
func (s *Server) Close() error {
	if s.auditLog != nil {
		_ = s.auditLog.Close()
	}
	if s.supervisor != nil {
		_ = s.supervisor.KillAll()
	}
	if s.sshClient != nil {
		_ = s.sshClient.Close()
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", s.version)
	mux.HandleFunc("/system/info", s.authed(s.systemInfo))
	mux.HandleFunc("/system/health", s.authed(s.health))
	mux.HandleFunc("/capabilities", s.authed(s.capabilities))
	mux.HandleFunc("/mcp", s.authed(s.mcpHTTP))
	mux.HandleFunc("/exec", s.authed(s.exec))
	mux.HandleFunc("/exec/live", s.authed(s.execLive))
	mux.HandleFunc("/exec/stream", s.authed(s.execStream))
	mux.HandleFunc("/exec/callback", s.authed(s.execCallback))
	mux.HandleFunc("/jobs", s.authed(s.jobsList))
	mux.HandleFunc("/jobs/", s.authed(s.jobGet))
	mux.HandleFunc("/file", s.authed(s.fileGet))
	mux.HandleFunc("/capabilities/mcp/", s.authed(s.supervisorHandler))
	return mux
}

func (s *Server) ListenAndServe() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if s.cfg.HeartbeatEnabled {
		go s.heartbeatLoop(ctx)
	}
	if s.cfg.QueueEnabled {
		go s.queueLoop(ctx)
	}
	s.startUpdateLoop(ctx)
	s.startAutoStartAgents()
	srv := &http.Server{Addr: s.cfg.Addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("shellmcp-go listening addr=%s name=%s heartbeat=%v queue=%v", s.cfg.Addr, s.cfg.Name, s.cfg.HeartbeatEnabled, s.cfg.QueueEnabled)
	return srv.ListenAndServe()
}

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
		r.Body = io.NopCloser(bytes.NewReader(body))
		if s.authorized(r, body) {
			next(w, r)
			return
		}
		s.auditLog.Event(audit.AuthFail, map[string]any{
			"path":   r.URL.Path,
			"method": r.Method,
			"reason": "auth_reject",
		})
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
	}
}

func (s *Server) authorized(r *http.Request, body []byte) bool {
	if s.cfg.Token != "" && r.Header.Get("Authorization") == "Bearer "+s.cfg.Token {
		return true
	}
	if r.Header.Get("X-GPTAdmin-Signature") == "" {
		return false
	}
	pub := s.cfg.HubPublicKey
	if pub == "" && s.cfg.HubPublicKeyFile != "" {
		if loaded, err := security.LoadPublicKey(s.cfg.HubPublicKeyFile); err == nil {
			pub = loaded
		}
	}
	if err := security.Verify(pub, r.Method, r.URL.Path, r.Header.Get("X-GPTAdmin-Timestamp"), r.Header.Get("X-GPTAdmin-Nonce"), body, r.Header.Get("X-GPTAdmin-Signature"), 5*time.Minute); err != nil {
		return false
	}
	// Signature is valid: additionally enforce nonce replay protection.
	nonce := r.Header.Get("X-GPTAdmin-Nonce")
	if nonce != "" {
		if s.nonces == nil || !s.nonces.CheckAndRemember(nonce) {
			s.auditLog.Event(audit.AuthFail, map[string]any{
				"path":   r.URL.Path,
				"method": r.Method,
				"reason": "nonce_replay",
			})
			return false
		}
	}
	s.auditLog.Event(audit.AuthOK, map[string]any{
		"path":   r.URL.Path,
		"method": r.Method,
	})
	return true
}
func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"component": "shellmcp-go", "build_version": parseBuildVersion(BuildVersion), "git_commit": GitCommit, "status": "prototype", "features": []string{"exec", "exec_live", "jobs", "file", "file_backup", "heartbeat", "queue", "real_mcp", "mcp_transport_http", "mcp_transport_stdio"}})
}
func (s *Server) systemInfo(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, system.Get()) }
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "time": time.Now().Unix(), "jobs": len(s.jobs.List()), "name": s.cfg.Name, "heartbeat": s.cfg.HeartbeatEnabled, "queue": s.cfg.QueueEnabled, "mode": s.cfg.Mode, "default_user": s.cfg.DefaultUser, "default_home": s.cfg.DefaultHome, "default_cwd": s.cfg.DefaultCwd})
}
func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"shell":           true,
		"system":          true,
		"tasks":           true,
		"logs":            true,
		"file_backup":     true,
		"go_shellmcp":     true,
		"real_mcp":        true,
		"mcp_transports":  []string{"stdio", "streamable-http-poll"},
		"build_version":   parseBuildVersion(BuildVersion),
		"git_commit":      GitCommit,
		"mcp_agents":      s.mcpAgentsForCapabilities(),
	})
}

func (s *Server) decodeExec(w http.ResponseWriter, r *http.Request) (shell.Request, bool) {
	var req shell.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return req, false
	}
	s.applyDefaults(&req)
	return req, true
}
func (s *Server) applyDefaults(req *shell.Request) {
	if req.Timeout == 0 {
		req.Timeout = s.cfg.ExecTimeout
	}
	if req.SpillDir == "" {
		req.SpillDir = s.cfg.SpillDir
	}
	if req.DefaultUser == "" {
		req.DefaultUser = s.cfg.DefaultUser
	}
	if req.DefaultCwd == "" {
		req.DefaultCwd = s.cfg.DefaultCwd
	}
}

// runShell dispatches a synchronous exec to the configured backend.
// When SSH is configured (s.sshClient != nil) the request is composed
// via sshexec.ComposeCmd and run on the remote host; otherwise the
// existing local shell.Run path is used. Both branches produce the
// same shell.Result so the JSON response shape is unchanged.
func (s *Server) runShell(ctx context.Context, req shell.Request) shell.Result {
	if s.sshClient == nil {
		return shell.Run(ctx, req, s.cfg.LogLimit)
	}
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	composed := sshexec.ComposeCmd(req.Cmd, req.Cwd, req.Env)
	sshRes, _ := s.sshClient.Run(runCtx, composed, timeout)
	return sshexecResultToShell(sshRes, req, timeout)
}

// runShellStream is the streaming analogue of runShell. The emit
// callback receives shell.Event values with the same shape the local
// shell.RunLive path emits (stdout/stderr "chunk" events + final
// "exit") so the SSE / NDJSON response shape is unchanged.
func (s *Server) runShellStream(ctx context.Context, req shell.Request, emit func(shell.Event)) shell.Result {
	if s.sshClient == nil {
		return shell.RunLive(ctx, req, s.cfg.LogLimit, emit)
	}
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	composed := sshexec.ComposeCmd(req.Cmd, req.Cwd, req.Env)
	var last shell.Result
	s.sshClient.RunStream(runCtx, composed, timeout, func(e map[string]any) {
		t, _ := e["type"].(string)
		switch t {
		case "stdout":
			if data, ok := e["data"].(string); ok {
				emit(shell.Event{Type: "chunk", Stream: "stdout", Data: data})
			}
		case "stderr":
			if data, ok := e["data"].(string); ok {
				emit(shell.Event{Type: "chunk", Stream: "stderr", Data: data})
			}
		case "exit":
			rc, _ := e["code"].(int)
			errStr, _ := e["error"].(string)
			timedOut, _ := e["timed_out"].(bool)
			last = shell.Result{
				ReturnCode: rc,
				Error:      errStr,
				TimedOut:   timedOut,
			}
		}
	})
	return last
}

// sshexecResultToShell maps the SSH result into a shell.Result so the
// JSON wire format is identical to the local-exec path. Fields that
// have no meaningful remote equivalent (Spilled / StdoutPath / Files)
// stay empty.
func sshexecResultToShell(r sshexec.Result, req shell.Request, timeout time.Duration) shell.Result {
	cwd := req.Cwd
	if cwd == "" {
		cwd = req.DefaultCwd
	}
	_ = timeout
	return shell.Result{
		ReturnCode: r.ReturnCode,
		Stdout:     r.Stdout,
		Stderr:     r.Stderr,
		Error:      r.Error,
		TimedOut:   r.TimedOut,
		Cwd:        cwd,
		RunAsUser:  req.RunAsUser,
	}
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	req, ok := s.decodeExec(w, r)
	if !ok {
		return
	}
	cmdField := firstToken(req.Cmd)
	startFields := map[string]any{
		"cmd":        cmdField,
		"user":       req.RunAsUser,
		"background": req.Background,
	}
	s.auditLog.Event(audit.ExecStart, startFields)
	if req.Background {
		j := s.jobs.Start(req)
		writeJSON(w, 202, map[string]any{"ok": true, "status": "running", "job_id": j.ID})
		return
	}

	// Best-effort metadata snapshot for sync (non-background) runs when the
	// caller enabled the feature and supplied a cwd.
	var snapshot *shell.Snapshot
	if s.preserveMeta && req.Cwd != "" {
		snap, err := shell.SnapshotDir(req.Cwd, s.preserveMax)
		if err == nil && snap != nil {
			snapshot = snap
		}
	}
	restore := func() (restored, failed int) {
		if snapshot == nil {
			return 0, 0
		}
		return snapshot.Restore()
	}

	startTime := time.Now()
	res := s.runShell(context.Background(), req)
	restored, failed := restore()
	elapsedMS := time.Since(startTime).Milliseconds()
	status := 200
	if res.Error != "" && res.ReturnCode == -1 {
		status = 500
	}
	endFields := map[string]any{
		"cmd":          cmdField,
		"user":         req.RunAsUser,
		"background":   false,
		"return_code":  res.ReturnCode,
		"elapsed_ms":   elapsedMS,
		"restored":     restored,
		"failed_files": failed,
	}
	if res.Error != "" {
		endFields["error"] = res.Error
	}
	s.auditLog.Event(audit.ExecEnd, endFields)
	writeJSON(w, status, res)
}
func (s *Server) execCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		shell.Request
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	req := body.Request
	s.applyDefaults(&req)
	jobID := body.JobID
	if jobID == "" {
		j := s.jobs.Start(req)
		writeJSON(w, 202, map[string]any{"ok": true, "status": "running", "job_id": j.ID, "delivery": "local_job"})
		return
	}
	go s.runCallbackJob(jobID, req)
	writeJSON(w, 202, map[string]any{"ok": true, "status": "running", "job_id": jobID, "delivery": "hub_queue_result"})
}
func (s *Server) execLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	req, ok := s.decodeExec(w, r)
	if !ok {
		return
	}
	cmdField := firstToken(req.Cmd)
	s.auditLog.Event(audit.ExecStart, map[string]any{
		"cmd":             cmdField,
		"user":            req.RunAsUser,
		"background":      req.Background,
		"transport":       "ndjson",
	})
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	startTime := time.Now()
	emit := func(e shell.Event) {
		b, _ := json.Marshal(e)
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	res := s.runShellStream(r.Context(), req, emit)
	elapsedMS := time.Since(startTime).Milliseconds()
	endFields := map[string]any{
		"cmd":             cmdField,
		"user":            req.RunAsUser,
		"background":      req.Background,
		"return_code":     res.ReturnCode,
		"elapsed_ms":      elapsedMS,
		"transport":       "ndjson",
	}
	if res.Error != "" {
		endFields["error"] = res.Error
	}
	s.auditLog.Event(audit.ExecEnd, endFields)
}
func (s *Server) jobsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	writeJSON(w, 200, map[string]any{"jobs": s.jobs.List()})
}
func (s *Server) jobGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	id := filepath.Base(r.URL.Path)
	j, ok := s.jobs.Get(id)
	if !ok {
		writeJSON(w, 404, map[string]any{"error": fmt.Sprintf("job %s not found", id)})
		return
	}
	writeJSON(w, 200, j)
}
func (s *Server) fileGet(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, 400, map[string]any{"error": "missing path"})
		return
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	root, _ := filepath.Abs(s.cfg.SpillDir)
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		writeJSON(w, 403, map[string]any{"error": "path outside spool dir"})
		return
	}
	http.ServeFile(w, r, abs)
}

func (s *Server) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		s.sendHeartbeat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) sendHeartbeat(ctx context.Context) {
	if s.hub == nil {
		return
	}
	beat := s.newBeat()
	beatCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, body, err := s.hub.Heartbeat(beatCtx, beat)
	if err != nil {
		log.Printf("heartbeat best-effort failed: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("heartbeat best-effort HTTP %d: %s", resp.StatusCode, string(body))
	}
}

func (s *Server) newBeat() hub.Beat {
	beat := hub.NewBeat(s.identity, s.cfg.BaseURL, s.cfg.Mode, parseBuildVersion(BuildVersion))
	beat.GitCommit = GitCommit
	beat.DefaultUser = s.cfg.DefaultUser
	beat.DefaultHome = s.cfg.DefaultHome
	beat.DefaultCwd = s.cfg.DefaultCwd
	return beat
}

func (s *Server) queueLoop(ctx context.Context) {
	if s.hub == nil {
		return
	}
	for {
		s.flushOutbox(ctx)
		q, ok, err := s.hub.PollQueue(ctx, s.newBeat(), s.cfg.QueueTimeout)
		if err != nil {
			log.Printf("queue poll failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if ok {
			req := shell.Request{Cmd: q.Cmd, Cwd: q.Cwd, Timeout: q.Timeout, Env: q.Env, SpillDir: s.cfg.SpillDir}
			s.applyDefaults(&req)
			go s.runCallbackJob(q.ID, req)
		}
	}
}
func (s *Server) runCallbackJob(jobID string, req shell.Request) {
	res := s.runShell(context.Background(), req)
	if s.hub == nil {
		return
	}
	payload := hub.TaskResult{ID: jobID, Result: res}
	if err := s.hub.PostResult(context.Background(), s.cfg.Name, payload); err != nil {
		log.Printf("callback result failed job=%s err=%v", jobID, err)
		s.spoolOutbox(jobID, payload, err)
	}
}

// Outbox retry tuning. Mirrors the Python shellmcp exponential-backoff
// hint so a flapping hub cannot cause a tight retry loop.
const (
	outboxBackoffBase = 5 * time.Second
	outboxBackoffCap  = 10 * time.Minute
)

func (s *Server) spoolOutbox(jobID string, payload hub.TaskResult, cause error) {
	if s.cfg.OutboxDir == "" {
		return
	}
	_ = os.MkdirAll(s.cfg.OutboxDir, 0o700)
	path := filepath.Join(s.cfg.OutboxDir, jobID+".json")
	now := time.Now()
	entry := map[string]any{
		"job_id":          jobID,
		"payload":         payload,
		"created_at":      now.Unix(),
		"last_error":      cause.Error(),
		"attempts":        0,
		"next_attempt_at": 0,
	}
	b, _ := json.Marshal(entry)
	_ = os.WriteFile(path, b, 0o600)
}

// computeOutboxBackoff returns the wait time for the given attempt number
// using a 5s base doubling each retry, capped at 10m, for parity with the
// Python shellmcp outbox behavior.
func computeOutboxBackoff(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	// Cap attempts to avoid overflow when shifting.
	const maxAttempts = 30
	if attempts > maxAttempts {
		attempts = maxAttempts
	}
	wait := outboxBackoffBase * (1 << attempts)
	if wait > outboxBackoffCap || wait < 0 {
		wait = outboxBackoffCap
	}
	return wait
}

func (s *Server) flushOutbox(ctx context.Context) {
	if s.hub == nil || s.cfg.OutboxDir == "" {
		return
	}
	entries, err := os.ReadDir(s.cfg.OutboxDir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.cfg.OutboxDir, ent.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry struct {
			JobID          string      `json:"job_id"`
			Payload        hub.TaskResult `json:"payload"`
			Attempts       int         `json:"attempts"`
			NextAttemptAt  int64       `json:"next_attempt_at"`
		}
		if json.Unmarshal(b, &entry) != nil || entry.Payload.ID == "" {
			continue
		}
		// Skip entries whose next_attempt_at is in the future.
		if entry.NextAttemptAt > 0 && now.Unix() < entry.NextAttemptAt {
			continue
		}
		if s.hub == nil {
			continue
		}
		postErr := s.hub.PostResult(ctx, s.cfg.Name, entry.Payload)
		if postErr == nil {
			_ = os.Remove(path)
			continue
		}
		// Bump attempt counter and set next_attempt_at to the backoff window.
		newAttempts := entry.Attempts + 1
		nextAt := now.Add(computeOutboxBackoff(newAttempts)).Unix()
		updated := map[string]any{
			"job_id":          entry.JobID,
			"payload":         entry.Payload,
			"created_at":      now.Unix(),
			"last_error":      postErr.Error(),
			"attempts":        newAttempts,
			"next_attempt_at": nextAt,
		}
		raw, mErr := json.Marshal(updated)
		if mErr != nil {
			log.Printf("outbox retry marshal failed file=%s err=%v", path, mErr)
			continue
		}
		if wErr := os.WriteFile(path, raw, 0o600); wErr != nil {
			log.Printf("outbox retry persist failed file=%s err=%v", path, wErr)
			continue
		}
		log.Printf("outbox retry failed file=%s err=%v attempts=%d next_attempt_in=%s", path, err, newAttempts, computeOutboxBackoff(newAttempts))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
