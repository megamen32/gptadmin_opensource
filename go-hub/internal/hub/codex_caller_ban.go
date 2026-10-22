package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// CodexCallerBan is a temporary, file-driven rule that refuses Codex session
// starts for named callers. It is narrow on purpose: the caller's other
// access is untouched, only the listed tools on the listed targets are
// refused, and only when the request asks for the denied harness.
//
// The rule file is optional. Without it the hub behaves exactly as before, so
// turning the ban off is "remove the file" and nothing else.
type CodexCallerBan struct {
	Version           int      `json:"version"`
	Enabled           bool     `json:"enabled"`
	Clients           []string `json:"clients,omitempty"`
	UserAgentPrefixes []string `json:"user_agent_prefixes,omitempty"`
	UserAgentExcludes []string `json:"user_agent_excludes,omitempty"`
	Targets           []string `json:"targets,omitempty"`
	TargetSuffixes    []string `json:"target_suffixes,omitempty"`
	Tools             []string `json:"tools,omitempty"`
	DeniedHarness     string   `json:"denied_harness,omitempty"`
	Reason            string   `json:"reason,omitempty"`
}

const (
	defaultCodexCallerBanFile = "/etc/gptadmin/codex-caller-ban.json"
	codexCallerBanCacheTTL    = 2 * time.Second
)

type codexCallerBanCache struct {
	sync.Mutex
	loadedAt time.Time
	loaded   CodexCallerBan
}

var codexCallerBanState codexCallerBanCache

// resetCodexCallerBanCache drops the cached rule so a test or an operator
// action takes effect immediately.
func resetCodexCallerBanCache() {
	codexCallerBanState.Lock()
	codexCallerBanState.loadedAt = time.Time{}
	codexCallerBanState.Unlock()
}

func codexCallerBanPath() string {
	if path := strings.TrimSpace(os.Getenv("GPTADMIN_CODEX_CALLER_BAN_FILE")); path != "" {
		return path
	}
	return defaultCodexCallerBanFile
}

// loadCodexCallerBan reads the optional rule file. A missing or unreadable
// file means no ban: a broken rule file never breaks a hub call.
func loadCodexCallerBan() CodexCallerBan {
	codexCallerBanState.Lock()
	defer codexCallerBanState.Unlock()
	if time.Since(codexCallerBanState.loadedAt) < codexCallerBanCacheTTL {
		return codexCallerBanState.loaded
	}
	ban := CodexCallerBan{}
	if raw, err := os.ReadFile(codexCallerBanPath()); err == nil {
		_ = json.Unmarshal(raw, &ban)
	}
	codexCallerBanState.loadedAt = time.Now()
	codexCallerBanState.loaded = ban
	return ban
}

// codexCallerBanCallerMatches reports whether the authenticated caller is the
// one the rule names. The client id is the durable identity; the user agent
// is the fallback for connectors that share one client id.
func codexCallerBanCallerMatches(ban CodexCallerBan, r *http.Request) bool {
	if r == nil {
		return false
	}
	clientID := ""
	if claims, ok := r.Context().Value(authClaimsContextKey{}).(map[string]any); ok {
		clientID = firstString(claims, "client_id", "sub")
	}
	if clientID != "" && containsString(ban.Clients, clientID) {
		return true
	}
	if len(ban.UserAgentPrefixes) == 0 {
		return false
	}
	userAgent := r.Header.Get("User-Agent")
	if userAgent == "" {
		return false
	}
	for _, prefix := range ban.UserAgentPrefixes {
		if prefix == "" || !strings.HasPrefix(userAgent, prefix) {
			continue
		}
		for _, excluded := range ban.UserAgentExcludes {
			if excluded != "" && strings.HasSuffix(userAgent, excluded) {
				return false
			}
		}
		return true
	}
	return false
}

// codexCallerBanTargetMatches reports whether the call targets a listed
// target. A suffix match covers every node of one MCP agent family.
func codexCallerBanTargetMatches(ban CodexCallerBan, target string) bool {
	if target == "" {
		return false
	}
	if containsString(ban.Targets, target) {
		return true
	}
	for _, suffix := range ban.TargetSuffixes {
		if suffix != "" && strings.HasSuffix(target, suffix) {
			return true
		}
	}
	return false
}

// authorizeCodexCallerBan refuses a Codex session start for a banned caller.
// The refusal is explicit and explains itself, so the caller can act on it.
func authorizeCodexCallerBan(r *http.Request, target, tool string, args map[string]any) error {
	ban := loadCodexCallerBan()
	if !ban.Enabled || ban.DeniedHarness == "" {
		return nil
	}
	if len(ban.Tools) > 0 && !containsString(ban.Tools, tool) {
		return nil
	}
	if !codexCallerBanTargetMatches(ban, target) {
		return nil
	}
	harness, _ := args["harness"].(string)
	if harness != ban.DeniedHarness {
		return nil
	}
	if !codexCallerBanCallerMatches(ban, r) {
		return nil
	}
	reason := ban.Reason
	if reason == "" {
		reason = "this caller may not start " + ban.DeniedHarness + " sessions"
	}
	return errors.New(reason)
}
