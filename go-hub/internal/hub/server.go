package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	Addr            string
	ConfigDir       string
	PublicDir       string
	CtlToken        string
	RelayAgentToken string
	ShellToken      string
	DefaultTimeout  time.Duration
	PollMaxTimeout  time.Duration
	OutputDir       string
	PublicOrigin    string
}

func FromEnv() Config {
	port := env("GPTADMIN_HUB_PORT", env("HUB_PORT", env("PORT", "9001")))
	host := env("GPTADMIN_HUB_HOST", env("HUB_HOST", ""))
	root := env("GPTADMIN_ROOT", ".")
	cfgDir := env("GPTADMIN_CONFIG_DIR", filepath.Join(root, "config"))
	defTimeout := secondsEnv("MCP_RELAY_DEFAULT_TIMEOUT", 30)
	pollTimeout := secondsEnv("MCP_RELAY_POLL_MAX_TIMEOUT", 55)
	return Config{
		Addr:            host + ":" + port,
		ConfigDir:       cfgDir,
		PublicDir:       env("GPTADMIN_PUBLIC_DIR", filepath.Join(root, "public")),
		CtlToken:        env("CTL_TOKEN", env("GPTADMIN_CTL_TOKEN", "")),
		RelayAgentToken: env("MCP_RELAY_AGENT_TOKEN", env("GPTADMIN_MCP_RELAY_AGENT_TOKEN", "")),
		ShellToken:      env("SHELL_TOKEN", env("SHELLMCP_TOKEN", "")),
		DefaultTimeout:  time.Duration(defTimeout) * time.Second,
		PollMaxTimeout:  time.Duration(pollTimeout) * time.Second,
		OutputDir:       env("GPTADMIN_OUTPUT_DIR", filepath.Join(cfgDir, "outputs")),
		PublicOrigin:    strings.TrimRight(env("PUBLIC_ORIGIN", ""), "/"),
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

type Server struct {
	cfg Config

	mu          sync.Mutex
	cond        *sync.Cond
	agents      map[string]*Agent
	relayQueues map[string][]string
	relayJobs   map[string]*relayJob
	shellQueues map[string][]string
	shellJobs   map[string]*shellJob
	audit       []auditEvent
}

func New(cfg Config) *Server {
	s := &Server{
		cfg:         cfg,
		agents:      map[string]*Agent{},
		relayQueues: map[string][]string{},
		relayJobs:   map[string]*relayJob{},
		shellQueues: map[string][]string{},
		shellJobs:   map[string]*shellJob{},
		audit:       []auditEvent{},
	}
	s.cond = sync.NewCond(&s.mu)
	return s
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
	mux.HandleFunc("/heartbeat", s.heartbeat)
	mux.HandleFunc("/queue/", s.queue)
	mux.HandleFunc("/mcp-relay/register", s.mcpRelayRegister)
	mux.HandleFunc("/mcp-relay/poll/", s.mcpRelayPoll)
	mux.HandleFunc("/mcp-relay/result/", s.mcpRelayResult)
	mux.HandleFunc("/mcp-relay/agents", s.requireCtl(s.mcpRelayAgents))
	mux.HandleFunc("/mcp-relay/list_mcp_agents", s.requireCtl(s.mcpRelayAgents))
	mux.HandleFunc("/mcp-relay/list_mcp_tools", s.requireCtl(s.mcpRelayTools))
	mux.HandleFunc("/mcp-relay/tools", s.requireCtl(s.mcpRelayTools))
	mux.HandleFunc("/mcp-relay/call_mcp_tool", s.requireCtl(s.mcpRelayCall))
	mux.HandleFunc("/mcp-relay/call", s.requireCtl(s.mcpRelayCall))
	mux.HandleFunc("/mcp-relay/get_mcp_job/", s.requireCtl(s.mcpRelayJob))
	mux.HandleFunc("/mcp-relay/job/", s.requireCtl(s.mcpRelayJob))
	mux.HandleFunc("/admin/api/overview", s.requireCtl(s.adminOverview))
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
			next(w, r)
			return
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
	s.addAuditLocked("mcp_result", map[string]any{"agent_id": agentID, "job_id": res.ID, "status": job.Status})
	s.cond.Broadcast()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) mcpRelayAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	agents := make([]Agent, 0, len(s.agents)+1)
	agents = append(agents, s.hubAgentLocked())
	for _, a := range s.agents {
		cp := *a
		agents = append(agents, cp)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
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
	target := firstString(req, "target", "agent_id")
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing target"})
		return
	}
	if target == "hub" {
		writeJSON(w, http.StatusOK, map[string]any{"agent_id": target, "status": "completed", "response": map[string]any{"tools": hubTools()}})
		return
	}
	if strings.HasPrefix(target, "shell:") {
		writeJSON(w, http.StatusOK, map[string]any{"agent_id": target, "status": "completed", "response": map[string]any{"tools": shellTools()}})
		return
	}
	jobID := s.enqueueRelay(target, "tools/list", nil)
	if truthy(req["background"]) {
		writeJSON(w, http.StatusOK, map[string]any{"agent_id": target, "status": "running", "background": true, "job_id": jobID})
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
	target := firstString(req, "target", "agent_id")
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
		writeJSON(w, status, map[string]any{"agent_id": target, "status": "completed", "response": resp})
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
		writeJSON(w, http.StatusOK, map[string]any{"agent_id": target, "status": "running", "background": true, "job_id": jobID})
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
	s.addAuditLocked("mcp_enqueue", map[string]any{"agent_id": agentID, "job_id": id, "method": method})
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
			return map[string]any{"agent_id": job.AgentID, "status": "running", "background": true, "job_id": jobID, "message": "MCP relay job is still running"}
		}
		waitCond(s.cond, minDuration(remaining, 500*time.Millisecond))
	}
}

func relayJobResponse(job *relayJob) map[string]any {
	if job.Status == "failed" {
		return map[string]any{"agent_id": job.AgentID, "status": "failed", "job_id": job.ID, "error": job.Error}
	}
	return map[string]any{"agent_id": job.AgentID, "status": job.Status, "job_id": job.ID, "response": spillFriendly(job.Result)}
}

func shellJobResponse(job *shellJob) map[string]any {
	out := map[string]any{"agent_id": "shell:" + job.Server, "status": job.Status, "job_id": job.ID, "task_id": job.ID}
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
	case "listMcpAgents", "list_mcp_agents":
		agents := make([]Agent, 0, len(s.agents)+1)
		agents = append(agents, s.hubAgentLocked())
		for _, a := range s.agents {
			agents = append(agents, *a)
		}
		return map[string]any{"agents": agents}, http.StatusOK
	case "list_pending_servers":
		return map[string]any{"pending": []any{}, "count": 0}, http.StatusOK
	case "hub_status", "status":
		return map[string]any{"ok": true, "agents": len(s.agents), "relay_jobs": len(s.relayJobs), "shell_jobs": len(s.shellJobs)}, http.StatusOK
	default:
		return map[string]any{"error": "unsupported hub tool", "tool": name, "arguments": args}, http.StatusBadRequest
	}
}

func (s *Server) callShellTool(target, toolName string, args map[string]any, background bool, timeout time.Duration) map[string]any {
	server := strings.TrimPrefix(target, "shell:")
	if toolName != "shell_exec" {
		return map[string]any{"agent_id": target, "status": "failed", "error": "unsupported shell tool: " + toolName}
	}
	cmd := firstString(args, "cmd", "command")
	if cmd == "" {
		return map[string]any{"agent_id": target, "status": "failed", "error": "missing cmd"}
	}
	job := &shellJob{ID: newID(), Server: server, Cmd: cmd, Cwd: firstString(args, "cwd"), Timeout: intFromAny(args["timeout"]), Env: mapValue(args["env"]), CreatedAt: nowFloat(), Status: "queued"}
	s.mu.Lock()
	s.shellJobs[job.ID] = job
	s.shellQueues[server] = append(s.shellQueues[server], job.ID)
	s.addAuditLocked("shell_enqueue", map[string]any{"server": server, "job_id": job.ID})
	s.cond.Broadcast()
	s.mu.Unlock()
	if background {
		return map[string]any{"agent_id": target, "status": "running", "background": true, "job_id": job.ID, "task_id": job.ID, "message": "shell job queued"}
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
			return map[string]any{"agent_id": target, "status": "running", "background": true, "job_id": job.ID, "task_id": job.ID, "message": "shell job is still running"}
		}
		waitCond(s.cond, minDuration(remaining, 500*time.Millisecond))
	}
}

func hubTools() []map[string]any {
	return []map[string]any{
		{"name": "list_mcp_agents", "description": "List registered GPTAdmin agents, including the internal hub", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "list_pending_servers", "description": "List pending shell server approvals", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "hub_status", "description": "Return Go hub runtime status", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	}
}

func shellTools() []map[string]any {
	return []map[string]any{{"name": "shell_exec", "description": "Execute a shell command through a polling shellmcp agent", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}, "cwd": map[string]any{"type": []string{"string", "null"}}, "timeout": map[string]any{"type": []string{"integer", "null"}}}, "required": []string{"cmd"}}}}
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	agents := make([]Agent, 0, len(s.agents)+1)
	agents = append(agents, s.hubAgentLocked())
	for _, a := range s.agents {
		agents = append(agents, *a)
	}
	jobs := len(s.relayJobs) + len(s.shellJobs)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agents": agents, "jobs_count": jobs, "build_version": BuildVersion, "git_commit": GitCommit})
}

func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	items := make([]any, 0, len(s.relayJobs)+len(s.shellJobs))
	for _, j := range s.relayJobs {
		items = append(items, j)
	}
	for _, j := range s.shellJobs {
		items = append(items, j)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "jobs": items})
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	items := append([]auditEvent(nil), s.audit...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "events": items})
}

func (s *Server) adminClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "clients": []any{}})
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
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
