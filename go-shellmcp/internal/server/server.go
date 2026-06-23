package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/job"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/system"
)

type Config struct {
	Addr        string
	Token       string
	LogLimit    int64
	ExecTimeout int
	SpillDir    string
}

func FromEnv() Config {
	port := env("SHELL_PORT", env("ROOTD_PORT", env("PORT", "25900")))
	limit, _ := strconv.ParseInt(env("LOG_LIMIT_B", "8192"), 10, 64)
	timeout, _ := strconv.Atoi(env("EXEC_TIMEOUT", "300"))
	spill := env("SHELL_SPOOL_DIR", env("ROOTD_SPOOL_DIR", filepath.Join(os.TempDir(), "rootd-go-spool")))
	return Config{Addr: ":" + port, Token: env("SHELL_TOKEN", env("ROOTD_TOKEN", "srv_secret")), LogLimit: limit, ExecTimeout: timeout, SpillDir: spill}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type Server struct {
	cfg  Config
	jobs *job.Manager
}

func New(cfg Config) *Server { return &Server{cfg: cfg, jobs: job.New(cfg.LogLimit)} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", s.version)
	mux.HandleFunc("/system/info", s.authed(s.systemInfo))
	mux.HandleFunc("/system/health", s.authed(s.health))
	mux.HandleFunc("/exec", s.authed(s.exec))
	mux.HandleFunc("/exec/live", s.authed(s.execLive))
	mux.HandleFunc("/jobs", s.authed(s.jobsList))
	mux.HandleFunc("/jobs/", s.authed(s.jobGet))
	return mux
}

func (s *Server) ListenAndServe() error {
	srv := &http.Server{Addr: s.cfg.Addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("rootd-go listening addr=%s", s.cfg.Addr)
	return srv.ListenAndServe()
}

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			if r.Header.Get("Authorization") != "Bearer "+s.cfg.Token {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"component": "rootd-go", "build_version": 2, "status": "prototype"})
}

func (s *Server) systemInfo(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, system.Get()) }
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "time": time.Now().Unix()})
}

func (s *Server) decodeExec(w http.ResponseWriter, r *http.Request) (shell.Request, bool) {
	var req shell.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return req, false
	}
	if req.Timeout == 0 {
		req.Timeout = s.cfg.ExecTimeout
	}
	if req.SpillDir == "" {
		req.SpillDir = s.cfg.SpillDir
	}
	return req, true
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
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

func (s *Server) execLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	req, ok := s.decodeExec(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
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
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	writeJSON(w, 200, map[string]any{"jobs": s.jobs.List()})
}

func (s *Server) jobGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
