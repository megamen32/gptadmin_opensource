package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/system"
)

type Config struct {
	Addr        string
	Token       string
	LogLimit    int64
	ExecTimeout int
}

func FromEnv() Config {
	port := env("SHELL_PORT", env("ROOTD_PORT", env("PORT", "25900")))
	limit, _ := strconv.ParseInt(env("LOG_LIMIT_B", "8192"), 10, 64)
	timeout, _ := strconv.Atoi(env("EXEC_TIMEOUT", "300"))
	return Config{Addr: ":" + port, Token: env("SHELL_TOKEN", env("ROOTD_TOKEN", "srv_secret")), LogLimit: limit, ExecTimeout: timeout}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type Server struct{ cfg Config }

func New(cfg Config) *Server { return &Server{cfg: cfg} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", s.version)
	mux.HandleFunc("/system/info", s.authed(s.systemInfo))
	mux.HandleFunc("/system/health", s.authed(s.health))
	mux.HandleFunc("/exec", s.authed(s.exec))
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
	writeJSON(w, 200, map[string]any{"component": "rootd-go", "build_version": 1, "status": "prototype"})
}

func (s *Server) systemInfo(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, system.Get()) }
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "time": time.Now().Unix()})
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var req shell.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if req.Timeout == 0 {
		req.Timeout = s.cfg.ExecTimeout
	}
	res := shell.Run(context.Background(), req, s.cfg.LogLimit)
	status := 200
	if res.Error != "" && res.ReturnCode == -1 {
		status = 500
	}
	writeJSON(w, status, res)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
