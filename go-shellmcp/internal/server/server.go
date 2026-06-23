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

	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/job"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/security"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/system"
)

const BuildVersion = 3

type Config struct {
	Addr              string
	Token             string
	LogLimit          int64
	ExecTimeout       int
	SpillDir          string
	Name              string
	BaseURL           string
	HubURL            string
	IdentityDir       string
	HeartbeatEnabled  bool
	HeartbeatInterval time.Duration
	QueueEnabled      bool
	QueueTimeout      int
	Mode              string
	OutboxDir         string
	DefaultUser       string
	DefaultHome       string
	DefaultCwd        string
	HubPublicKeyFile  string
	HubPublicKey      string
}

func FromEnv() Config {
	port := env("SHELL_PORT", env("ROOTD_PORT", env("PORT", "25900")))
	host := env("SHELL_HOST", env("ROOTD_HOST", ""))
	limit, _ := strconv.ParseInt(env("LOG_LIMIT_B", "8192"), 10, 64)
	timeout, _ := strconv.Atoi(env("EXEC_TIMEOUT", "300"))
	spill := env("SHELL_SPOOL_DIR", env("ROOTD_SPOOL_DIR", filepath.Join(os.TempDir(), "rootd-go-spool")))
	name := env("SHELL_NAME", env("ROOTD_NAME", ""))
	baseURL := env("SHELL_URL", env("ROOTD_URL", "http://127.0.0.1:"+port))
	hbInt, _ := strconv.Atoi(env("HB_INTERVAL_S", "60"))
	qTimeout, _ := strconv.Atoi(env("QUEUE_LONG_POLL_TIMEOUT_S", "55"))
	mode := env("SHELL_MODE", env("ROOTD_MODE", ""))
	if mode == "" {
		if truthy(env("SHELL_QUEUE", env("ROOTD_QUEUE", "0"))) {
			mode = "long_poll"
		} else {
			mode = "webhook"
		}
	}
	outbox := env("SHELL_OUTBOX_DIR", env("ROOTD_OUTBOX_DIR", filepath.Join(spill, "outbox")))
	defaultUser := env("SHELL_DEFAULT_USER", env("ROOTD_DEFAULT_USER", ""))
	defaultHome := env("SHELL_DEFAULT_HOME", env("ROOTD_DEFAULT_HOME", ""))
	defaultCwd := env("SHELL_DEFAULT_CWD", env("ROOTD_DEFAULT_CWD", defaultHome))
	return Config{Addr: host + ":" + port, Token: env("SHELL_TOKEN", env("ROOTD_TOKEN", "srv_secret")), LogLimit: limit, ExecTimeout: timeout, SpillDir: spill, Name: name, BaseURL: baseURL, HubURL: strings.TrimRight(env("HUB_URL", ""), "/"), IdentityDir: env("SHELL_IDENTITY_DIR", env("ROOTD_IDENTITY_DIR", "/etc/gptadmin")), HeartbeatEnabled: truthy(env("SHELL_HEARTBEAT", env("ROOTD_HEARTBEAT", "0"))), HeartbeatInterval: time.Duration(hbInt) * time.Second, QueueEnabled: truthy(env("SHELL_QUEUE", env("ROOTD_QUEUE", "0"))), QueueTimeout: qTimeout, Mode: mode, OutboxDir: outbox, DefaultUser: defaultUser, DefaultHome: defaultHome, DefaultCwd: defaultCwd, HubPublicKeyFile: env("HUB_PUBLIC_KEY_FILE", filepath.Join(env("SHELL_IDENTITY_DIR", env("ROOTD_IDENTITY_DIR", "/etc/gptadmin")), "hub_ed25519.pub")), HubPublicKey: env("HUB_PUBLIC_KEY", "")}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func truthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

type Server struct {
	cfg      Config
	jobs     *job.Manager
	identity *security.Identity
	hub      *hub.Client
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
	return &Server{cfg: cfg, jobs: job.New(cfg.LogLimit), identity: ident, hub: hc}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", s.version)
	mux.HandleFunc("/system/info", s.authed(s.systemInfo))
	mux.HandleFunc("/system/health", s.authed(s.health))
	mux.HandleFunc("/capabilities", s.authed(s.capabilities))
	mux.HandleFunc("/exec", s.authed(s.exec))
	mux.HandleFunc("/exec/live", s.authed(s.execLive))
	mux.HandleFunc("/exec/callback", s.authed(s.execCallback))
	mux.HandleFunc("/jobs", s.authed(s.jobsList))
	mux.HandleFunc("/jobs/", s.authed(s.jobGet))
	mux.HandleFunc("/file", s.authed(s.fileGet))
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
	srv := &http.Server{Addr: s.cfg.Addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("rootd-go listening addr=%s name=%s heartbeat=%v queue=%v", s.cfg.Addr, s.cfg.Name, s.cfg.HeartbeatEnabled, s.cfg.QueueEnabled)
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
	return security.Verify(pub, r.Method, r.URL.Path, r.Header.Get("X-GPTAdmin-Timestamp"), r.Header.Get("X-GPTAdmin-Nonce"), body, r.Header.Get("X-GPTAdmin-Signature"), 5*time.Minute) == nil
}
func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"component": "rootd-go", "build_version": BuildVersion, "status": "prototype", "features": []string{"exec", "exec_live", "jobs", "file", "heartbeat", "queue"}})
}
func (s *Server) systemInfo(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, system.Get()) }
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "time": time.Now().Unix(), "jobs": len(s.jobs.List()), "name": s.cfg.Name, "heartbeat": s.cfg.HeartbeatEnabled, "queue": s.cfg.QueueEnabled, "mode": s.cfg.Mode, "default_user": s.cfg.DefaultUser, "default_home": s.cfg.DefaultHome, "default_cwd": s.cfg.DefaultCwd})
}
func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"shell": true, "system": true, "tasks": true, "logs": true, "go_shellmcp": true, "build_version": BuildVersion})
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

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	req, ok := s.decodeExec(w, r)
	if !ok {
		return
	}
	if req.Background {
		j := s.jobs.Start(req)
		writeJSON(w, 202, map[string]any{"ok": true, "status": "running", "job_id": j.ID})
		return
	}
	res := shell.Run(context.Background(), req, s.cfg.LogLimit)
	status := 200
	if res.Error != "" && res.ReturnCode == -1 {
		status = 500
	}
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
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emit := func(e shell.Event) {
		b, _ := json.Marshal(e)
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	shell.RunLive(r.Context(), req, s.cfg.LogLimit, emit)
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
	beat := hub.NewBeat(s.identity, s.cfg.BaseURL, s.cfg.Mode, BuildVersion)
	beat.DefaultUser = s.cfg.DefaultUser
	beat.DefaultHome = s.cfg.DefaultHome
	beat.DefaultCwd = s.cfg.DefaultCwd
	resp, body, err := s.hub.Heartbeat(ctx, beat)
	if err != nil {
		log.Printf("heartbeat failed: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("heartbeat HTTP %d: %s", resp.StatusCode, string(body))
	}
}
func (s *Server) queueLoop(ctx context.Context) {
	if s.hub == nil {
		return
	}
	for {
		s.flushOutbox(ctx)
		q, ok, err := s.hub.PollQueue(ctx, s.cfg.Name, s.cfg.QueueTimeout)
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
	res := shell.Run(context.Background(), req, s.cfg.LogLimit)
	if s.hub == nil {
		return
	}
	payload := hub.TaskResult{ID: jobID, Result: res}
	if err := s.hub.PostResult(context.Background(), s.cfg.Name, payload); err != nil {
		log.Printf("callback result failed job=%s err=%v", jobID, err)
		s.spoolOutbox(jobID, payload, err)
	}
}

func (s *Server) spoolOutbox(jobID string, payload hub.TaskResult, cause error) {
	if s.cfg.OutboxDir == "" {
		return
	}
	_ = os.MkdirAll(s.cfg.OutboxDir, 0o700)
	path := filepath.Join(s.cfg.OutboxDir, jobID+".json")
	entry := map[string]any{"job_id": jobID, "payload": payload, "created_at": time.Now().Unix(), "last_error": cause.Error()}
	b, _ := json.Marshal(entry)
	_ = os.WriteFile(path, b, 0o600)
}

func (s *Server) flushOutbox(ctx context.Context) {
	if s.hub == nil || s.cfg.OutboxDir == "" {
		return
	}
	entries, err := os.ReadDir(s.cfg.OutboxDir)
	if err != nil {
		return
	}
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
			Payload hub.TaskResult `json:"payload"`
		}
		if json.Unmarshal(b, &entry) != nil || entry.Payload.ID == "" {
			continue
		}
		if err := s.hub.PostResult(ctx, s.cfg.Name, entry.Payload); err == nil {
			_ = os.Remove(path)
		} else {
			log.Printf("outbox retry failed file=%s err=%v", path, err)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
