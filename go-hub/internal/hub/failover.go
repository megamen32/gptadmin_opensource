package hub

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FailoverNode struct {
	ServerID     string `json:"server_id"`
	Rank         int    `json:"rank"`
	Enabled      bool   `json:"enabled"`
	CheckURL     string `json:"check_url,omitempty"`
	HubURL       string `json:"hub_url,omitempty"`
	LocalHubPort int    `json:"local_hub_port,omitempty"`
	Notes        string `json:"notes,omitempty"`
}

type FailoverConfig struct {
	Enabled                  bool           `json:"enabled"`
	PrimaryPublicURL         string         `json:"primary_public_url,omitempty"`
	PrimaryHealthURL         string         `json:"primary_health_url,omitempty"`
	PrimaryReclaimURL        string         `json:"primary_reclaim_url,omitempty"`
	PrimaryReclaimAcceptURL  string         `json:"primary_reclaim_accept_url,omitempty"`
	StateURL                 string         `json:"state_url,omitempty"`
	ReclaimMaxAgeSec         int            `json:"reclaim_max_age_sec,omitempty"`
	CheckIntervalSec         int            `json:"check_interval_sec"`
	PublicConfirmTimeoutSec  int            `json:"public_confirm_timeout_sec"`
	FailCountBase            int            `json:"fail_count_base"`
	PromotionCooldownSec     int            `json:"promotion_cooldown_sec"`
	DeterministicRankBackoff bool           `json:"deterministic_rank_backoff"`
	Nodes                    []FailoverNode `json:"nodes"`
	UpdatedAt                string         `json:"updated_at,omitempty"`
}

func (s *Server) failoverConfigPath() string {
	if s.cfg.FailoverConfigFile != "" {
		return s.cfg.FailoverConfigFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "failover_config.json")
}

func (s *Server) failoverStatePath() string {
	if s.cfg.FailoverStateFile != "" {
		return s.cfg.FailoverStateFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "failover_state.json")
}

func (s *Server) failoverReclaimCommandPath() string {
	if s.cfg.FailoverReclaimCommandFile != "" {
		return s.cfg.FailoverReclaimCommandFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "failover_reclaim_command.json")
}

func (s *Server) defaultFailoverConfig() FailoverConfig {
	publicURL := firstNonEmpty(os.Getenv("HUB_PUBLIC_URL"), s.cfg.PublicOrigin, s.cfg.MCPResource)
	return FailoverConfig{
		Enabled:                  false,
		PrimaryPublicURL:         strings.TrimRight(publicURL, "/"),
		PrimaryHealthURL:         firstNonEmpty(os.Getenv("GPTADMIN_PRIMARY_HEALTH_URL"), "http://127.0.0.1:9001/healthz"),
		PrimaryReclaimURL:        firstNonEmpty(os.Getenv("GPTADMIN_PRIMARY_RECLAIM_URL"), strings.TrimRight(publicURL, "/")+"/admin/api/failover/reclaim"),
		PrimaryReclaimAcceptURL:  firstNonEmpty(os.Getenv("GPTADMIN_PRIMARY_RECLAIM_ACCEPT_URL"), strings.TrimRight(publicURL, "/")+"/admin/api/failover/reclaim/accept"),
		StateURL:                 strings.TrimRight(publicURL, "/") + "/admin/api/failover/state?secrets=1",
		ReclaimMaxAgeSec:         120,
		CheckIntervalSec:         15,
		PublicConfirmTimeoutSec:  3,
		FailCountBase:            3,
		PromotionCooldownSec:     300,
		DeterministicRankBackoff: true,
		Nodes:                    []FailoverNode{},
	}
}

func (s *Server) normalizeFailoverConfig(cfg FailoverConfig) FailoverConfig {
	def := s.defaultFailoverConfig()
	if cfg.PrimaryPublicURL == "" {
		cfg.PrimaryPublicURL = def.PrimaryPublicURL
	}
	cfg.PrimaryPublicURL = strings.TrimRight(cfg.PrimaryPublicURL, "/")
	if cfg.PrimaryHealthURL == "" {
		cfg.PrimaryHealthURL = def.PrimaryHealthURL
	}
	if cfg.StateURL == "" && cfg.PrimaryPublicURL != "" {
		cfg.StateURL = cfg.PrimaryPublicURL + "/admin/api/failover/state?secrets=1"
	}
	if cfg.PrimaryReclaimURL == "" && cfg.PrimaryPublicURL != "" {
		cfg.PrimaryReclaimURL = cfg.PrimaryPublicURL + "/admin/api/failover/reclaim"
	}
	if cfg.PrimaryReclaimAcceptURL == "" && cfg.PrimaryPublicURL != "" {
		cfg.PrimaryReclaimAcceptURL = cfg.PrimaryPublicURL + "/admin/api/failover/reclaim/accept"
	}
	if cfg.ReclaimMaxAgeSec <= 0 {
		cfg.ReclaimMaxAgeSec = def.ReclaimMaxAgeSec
	}
	if cfg.CheckIntervalSec <= 0 {
		cfg.CheckIntervalSec = def.CheckIntervalSec
	}
	if cfg.PublicConfirmTimeoutSec <= 0 {
		cfg.PublicConfirmTimeoutSec = def.PublicConfirmTimeoutSec
	}
	if cfg.FailCountBase <= 0 {
		cfg.FailCountBase = def.FailCountBase
	}
	if cfg.PromotionCooldownSec <= 0 {
		cfg.PromotionCooldownSec = def.PromotionCooldownSec
	}
	if !cfg.DeterministicRankBackoff {
		cfg.DeterministicRankBackoff = true
	}
	for i := range cfg.Nodes {
		cfg.Nodes[i].ServerID = strings.TrimSpace(cfg.Nodes[i].ServerID)
		if cfg.Nodes[i].Rank <= 0 {
			cfg.Nodes[i].Rank = i + 1
		}
		if cfg.Nodes[i].LocalHubPort <= 0 {
			cfg.Nodes[i].LocalHubPort = 9001
		}
	}
	cfg.UpdatedAt = time.Now().Format(time.RFC3339)
	return cfg
}

func (s *Server) loadFailoverConfig() FailoverConfig {
	cfg := s.defaultFailoverConfig()
	path := s.failoverConfigPath()
	if path == "" {
		return cfg
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("failover config load failed path=%s err=%v", path, err)
		}
		return cfg
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Printf("failover config parse failed path=%s err=%v", path, err)
		return s.defaultFailoverConfig()
	}
	return s.normalizeFailoverConfig(cfg)
}

func (s *Server) saveFailoverConfigLocked() error {
	path := s.failoverConfigPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.failover, "", "  ")
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

func (s *Server) failoverStateBundleLocked(includeSecrets bool) map[string]any {
	agents := make(map[string]Agent, len(s.agents))
	for id, agent := range s.agents {
		if agent == nil {
			continue
		}
		cp := *agent
		cp.Meta = sanitizeFailoverMeta(cp.Meta)
		agents[id] = cp
	}
	frp := map[string]any{
		"enabled":       truthyString(os.Getenv("FRP_ENABLE")),
		"domain":        os.Getenv("FRP_DOMAIN"),
		"subdomain":     os.Getenv("FRP_SUBDOMAIN"),
		"server_addr":   os.Getenv("FRP_SERVER_ADDR"),
		"server_port":   os.Getenv("FRP_SERVER_PORT"),
		"endpoints":     splitCSV(os.Getenv("FRP_SERVER_ENDPOINTS")),
		"local_ip":      "127.0.0.1",
		"local_port":    firstNonEmpty(os.Getenv("HUB_PORT"), os.Getenv("GPTADMIN_HUB_PORT"), "9001"),
		"public_url":    firstNonEmpty(os.Getenv("HUB_PUBLIC_URL"), s.cfg.PublicOrigin),
		"tunnel_mode":   os.Getenv("TUNNEL_MODE"),
		"token_present": os.Getenv("FRP_TOKEN") != "",
	}
	secrets := map[string]any{}
	if includeSecrets {
		frp["token"] = os.Getenv("FRP_TOKEN")
		secrets = map[string]any{
			"ctl_token":         os.Getenv("CTL_TOKEN"),
			"relay_agent_token": os.Getenv("MCP_RELAY_AGENT_TOKEN"),
			"shellmcp_token":    firstNonEmpty(os.Getenv("SHELLMCP_TOKEN"), os.Getenv("SHELL_TOKEN")),
			"oauth_secret":      os.Getenv("OAUTH_CLIENT_SECRET"),
			"admin_password":    os.Getenv("ADMIN_PASSWORD"),
			"bridge_key":        os.Getenv("MCP_BRIDGE_KEY"),
		}
	}
	return map[string]any{
		"ok":              true,
		"kind":            "gptadmin_failover_state",
		"saved_at":        nowFloat(),
		"saved_at_fmt":    time.Now().Format(time.RFC3339),
		"build":           map[string]any{"name": "gptadmin-go-hub", "build_version": BuildVersion, "git_commit": GitCommit},
		"public_origin":   firstNonEmpty(s.cfg.PublicOrigin, os.Getenv("PUBLIC_ORIGIN")),
		"mcp_resource":    firstNonEmpty(s.cfg.MCPResource, os.Getenv("MCP_RESOURCE")),
		"hub_public_url":  firstNonEmpty(os.Getenv("HUB_PUBLIC_URL"), s.cfg.PublicOrigin),
		"hub_url":         os.Getenv("HUB_URL"),
		"registry_state":  s.registryStatePath(),
		"failover_config": s.failover,
		"agents":          agents,
		"tunnel":          map[string]any{"frp": frp},
		"secrets":         secrets,
	}
}

func (s *Server) saveFailoverStateBundleLocked() error {
	path := s.failoverStatePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.failoverStateBundleLocked(true), "", "  ")
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

func (s *Server) adminFailoverReclaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "node_id is required"})
		return
	}
	secret := firstNonEmpty(os.Getenv("MCP_BRIDGE_KEY"), os.Getenv("CTL_TOKEN"))
	if secret == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failover reclaim signing key is not configured"})
		return
	}
	s.mu.Lock()
	cfg := s.failover
	s.addAuditLocked("failover_reclaim_issued", map[string]any{"node_id": nodeID})
	s.mu.Unlock()
	issuedAt := time.Now().Unix()
	expiresAt := issuedAt + int64(cfg.ReclaimMaxAgeSec)
	if expiresAt <= issuedAt {
		expiresAt = issuedAt + 120
	}
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	action := "demote"
	primaryHealthURL := cfg.PrimaryHealthURL
	sigInput := failoverReclaimSigInput(action, nodeID, nonce, issuedAt, expiresAt, primaryHealthURL)
	sig := signFailoverReclaim(secret, sigInput)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"action":             action,
		"node_id":            nodeID,
		"issued_at":          issuedAt,
		"expires_at":         expiresAt,
		"nonce":              nonce,
		"primary_health_url": primaryHealthURL,
		"alg":                "hmac-sha256",
		"signature":          sig,
	})
}

func failoverReclaimSigInput(action, nodeID, nonce string, issuedAt, expiresAt int64, primaryHealthURL string) string {
	return fmt.Sprintf("%s\n%s\n%s\n%d\n%d\n%s", action, nodeID, nonce, issuedAt, expiresAt, primaryHealthURL)
}

func signFailoverReclaim(secret, input string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(input))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) adminFailoverReclaimAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var payload map[string]any
	if err := readJSON(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	action := strings.TrimSpace(fmt.Sprint(payload["action"]))
	nodeID := strings.TrimSpace(fmt.Sprint(payload["node_id"]))
	nonce := strings.TrimSpace(fmt.Sprint(payload["nonce"]))
	primaryHealthURL := strings.TrimSpace(fmt.Sprint(payload["primary_health_url"]))
	sig := strings.TrimSpace(fmt.Sprint(payload["signature"]))
	issuedAt, ok1 := int64FromAny(payload["issued_at"])
	expiresAt, ok2 := int64FromAny(payload["expires_at"])
	if action != "demote" || nodeID == "" || nonce == "" || sig == "" || !ok1 || !ok2 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "detail": "invalid reclaim payload"})
		return
	}
	now := time.Now().Unix()
	if issuedAt > now+30 || expiresAt < now || expiresAt-issuedAt > 600 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "detail": "expired or invalid reclaim window"})
		return
	}
	secret := firstNonEmpty(os.Getenv("MCP_BRIDGE_KEY"), os.Getenv("CTL_TOKEN"))
	if secret == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "detail": "failover reclaim signing key is not configured"})
		return
	}
	expected := signFailoverReclaim(secret, failoverReclaimSigInput(action, nodeID, nonce, issuedAt, expiresAt, primaryHealthURL))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "detail": "bad signature"})
		return
	}
	configuredNodeID := strings.TrimSpace(os.Getenv("GPTADMIN_FAILOVER_NODE_ID"))
	if configuredNodeID == "" || configuredNodeID != nodeID {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": false, "reason": "not_this_node", "configured_node_id": configuredNodeID, "node_id": nodeID})
		return
	}
	cmdPath := s.failoverReclaimCommandPath()
	if cmdPath == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "detail": "reclaim command path is not configured"})
		return
	}
	payload["received_at"] = now
	payload["accepted_by"] = configuredNodeID
	if err := writeJSONFile0600(cmdPath, payload); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "detail": err.Error()})
		return
	}
	s.mu.Lock()
	s.addAuditLocked("failover_reclaim_accepted", map[string]any{"node_id": nodeID, "nonce": nonce})
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": true, "node_id": nodeID, "command_file": cmdPath})
}

func int64FromAny(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int64:
		return t, true
	case float64:
		return int64(t), true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case string:
		var n int64
		_, err := fmt.Sscanf(t, "%d", &n)
		return n, err == nil
	default:
		return 0, false
	}
}

func writeJSONFile0600(path string, data any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
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

func (s *Server) adminFailover(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		cfg := s.failover
		state := s.failoverStateBundleLocked(false)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": cfg, "state": state, "state_files": map[string]any{"config": s.failoverConfigPath(), "state": s.failoverStatePath(), "reclaim_command": s.failoverReclaimCommandPath()}})
	case http.MethodPost:
		var raw map[string]any
		if err := readJSON(r, &raw); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		payload := raw
		if nested, ok := raw["config"].(map[string]any); ok {
			payload = nested
		}
		b, _ := json.Marshal(payload)
		var cfg FailoverConfig
		if err := json.Unmarshal(b, &cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		cfg = s.normalizeFailoverConfig(cfg)
		s.mu.Lock()
		s.failover = cfg
		s.addAuditLocked("failover_config_saved", map[string]any{"enabled": cfg.Enabled, "nodes": len(cfg.Nodes), "fail_count_base": cfg.FailCountBase})
		err1 := s.saveFailoverConfigLocked()
		err2 := s.saveFailoverStateBundleLocked()
		s.mu.Unlock()
		if err1 != nil || err2 != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "config_error": errString(err1), "state_error": errString(err2)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": cfg, "state_files": map[string]any{"config": s.failoverConfigPath(), "state": s.failoverStatePath(), "reclaim_command": s.failoverReclaimCommandPath()}})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (s *Server) adminFailoverState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	includeSecrets := r.URL.Query().Get("secrets") == "1" || strings.EqualFold(r.URL.Query().Get("secrets"), "true")
	s.mu.Lock()
	state := s.failoverStateBundleLocked(includeSecrets)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, state)
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return strings.TrimSpace(item)
		}
	}
	return ""
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func sanitizeFailoverMeta(meta map[string]any) map[string]any {
	out := make(map[string]any, len(meta))
	for k, v := range meta {
		out[k] = sanitizeFailoverValue(k, v)
	}
	return out
}

func sanitizeFailoverValue(key string, value any) any {
	lk := strings.ToLower(key)
	if strings.Contains(lk, "token") || strings.Contains(lk, "secret") || strings.Contains(lk, "password") || strings.Contains(lk, "authorization") || strings.Contains(lk, "bearer") || strings.Contains(lk, "key") {
		if value == nil || value == "" {
			return value
		}
		return "<MASKED>"
	}
	switch v := value.(type) {
	case string:
		if strings.Contains(strings.ToLower(v), "bearer ") || strings.Contains(strings.ToLower(v), "authorization:") {
			return maskBearerString(v)
		}
		return v
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeFailoverValue(key, item)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = maskBearerString(item)
		}
		return out
	case map[string]any:
		return sanitizeFailoverMeta(v)
	default:
		return v
	}
}

func maskBearerString(v string) string {
	parts := strings.Fields(v)
	for i := 0; i < len(parts); i++ {
		if strings.EqualFold(parts[i], "Bearer") && i+1 < len(parts) {
			parts[i+1] = "<MASKED>"
		}
	}
	masked := strings.Join(parts, " ")
	if strings.Contains(strings.ToLower(masked), "authorization:") && !strings.Contains(masked, "<MASKED>") {
		return "Authorization: Bearer <MASKED>"
	}
	return masked
}
