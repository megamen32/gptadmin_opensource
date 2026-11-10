package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const browserGrepMeshPrefix = "/api/browser/grepmesh/"
const browserGrepMeshBodyLimit = 512 << 10

type browserGrepMeshConfig struct {
	ssoURL   string
	origin   string
	host     string
	bindings map[string]string
}

type browserGrepMeshError struct {
	status int
	code   string
	detail string
}

func (e *browserGrepMeshError) Error() string { return e.detail }

func browserGrepMeshDenied() *browserGrepMeshError {
	return &browserGrepMeshError{http.StatusForbidden, "browser_access_denied", "browser identity has no current GrepMesh access"}
}
func browserGrepMeshUnavailable() *browserGrepMeshError {
	return &browserGrepMeshError{http.StatusServiceUnavailable, "browser_auth_unavailable", "browser authorization is unavailable"}
}
func browserGrepMeshLoginRequired() *browserGrepMeshError {
	return &browserGrepMeshError{http.StatusUnauthorized, "browser_login_required", "a valid browser login is required"}
}

// This is a credential-free binding to the canonical managed-client store.
// There is no default subject, wildcard mapping, token file, or new ACL store.
func loadBrowserGrepMeshConfig() (browserGrepMeshConfig, error) {
	var cfg browserGrepMeshConfig
	sso, err := url.Parse(strings.TrimSpace(os.Getenv("GREPMESH_BROWSER_SSO_URL")))
	if err != nil || sso.Scheme != "http" || sso.User != nil || sso.RawQuery != "" || sso.Fragment != "" ||
		sso.Path != "/check" || sso.RawPath != "" || net.ParseIP(sso.Hostname()) == nil || !net.ParseIP(sso.Hostname()).IsLoopback() {
		return cfg, browserGrepMeshUnavailable()
	}
	origin, err := url.Parse(strings.TrimSpace(os.Getenv("GREPMESH_BROWSER_ORIGIN")))
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" ||
		(origin.Path != "" && origin.Path != "/") || origin.RawPath != "" ||
		(origin.Scheme != "https" && !(origin.Scheme == "http" && net.ParseIP(origin.Hostname()) != nil && net.ParseIP(origin.Hostname()).IsLoopback())) {
		return cfg, browserGrepMeshUnavailable()
	}
	raw := os.Getenv("GREPMESH_BROWSER_TOKEN_BINDINGS")
	if len(raw) == 0 || len(raw) > 64<<10 {
		return cfg, browserGrepMeshUnavailable()
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return cfg, browserGrepMeshUnavailable()
	}
	bindings, tokens := map[string]string{}, map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		subject, ok := key.(string)
		var tokenID string
		if err != nil || !ok || decoder.Decode(&tokenID) != nil || !validBrowserGrepMeshSubject(subject) ||
			len(tokenID) == 0 || len(tokenID) > 256 || strings.TrimSpace(tokenID) != tokenID ||
			bindings[subject] != "" || tokens[tokenID] || len(bindings) >= 256 {
			return cfg, browserGrepMeshUnavailable()
		}
		bindings[subject], tokens[tokenID] = tokenID, true
	}
	if _, err = decoder.Token(); err != nil {
		return cfg, browserGrepMeshUnavailable()
	}
	if decoder.Decode(&struct{}{}) != io.EOF || len(bindings) == 0 {
		return cfg, browserGrepMeshUnavailable()
	}
	cfg.ssoURL, cfg.origin, cfg.host, cfg.bindings = sso.String(), origin.Scheme+"://"+origin.Host, origin.Host, bindings
	return cfg, nil
}

func validBrowserGrepMeshSubject(subject string) bool {
	if subject == "" || len(subject) > 256 || !utf8.ValidString(subject) || strings.TrimSpace(subject) != subject {
		return false
	}
	for _, char := range subject {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

var browserGrepMeshSSOClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		Proxy: nil, MaxIdleConns: 16, MaxIdleConnsPerHost: 8,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 3 * time.Second,
	},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

func browserGrepMeshSSOSubject(ctx context.Context, cfg browserGrepMeshConfig, cookie string) (string, error) {
	if cookie == "" || len(cookie) > 16<<10 {
		return "", browserGrepMeshLoginRequired()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.ssoURL, nil)
	if err != nil {
		return "", browserGrepMeshUnavailable()
	}
	request.Host = cfg.host
	request.Header.Set("Cookie", cookie)
	request.Header.Set("X-Forwarded-Host", cfg.host)
	request.Header.Set("X-Original-Host", cfg.host)
	request.Header.Set("X-Original-URI", "/")
	request.Header.Set("X-Original-URL", cfg.origin+"/")
	response, err := browserGrepMeshSSOClient.Do(request)
	if err != nil {
		return "", browserGrepMeshUnavailable()
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == http.StatusUnauthorized {
		return "", browserGrepMeshLoginRequired()
	}
	if response.StatusCode == http.StatusForbidden {
		return "", browserGrepMeshDenied()
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return "", browserGrepMeshUnavailable()
	}
	// Both spellings are responses from the fixed verifier, never request input.
	subjects, legacy := response.Header.Values("X-GPTAdmin-User"), response.Header.Values("X-Auth-User")
	if len(subjects) > 1 || len(legacy) > 1 {
		return "", browserGrepMeshLoginRequired()
	}
	subject := response.Header.Get("X-GPTAdmin-User")
	if subject == "" {
		subject = response.Header.Get("X-Auth-User")
	}
	if !validBrowserGrepMeshSubject(subject) || (len(legacy) == 1 && legacy[0] != subject) {
		return "", browserGrepMeshLoginRequired()
	}
	return subject, nil
}

type browserGrepMeshSession struct {
	Principal        string   `json:"principal"`
	ProfileID        string   `json:"profile_id"`
	ProfileVersion   int64    `json:"profile_version"`
	RootsDigest      string   `json:"roots_digest"`
	AuthzFingerprint string   `json:"authz_fingerprint"`
	ReadRoots        []string `json:"read_roots"`
	AllowedTargets   []string `json:"allowed_targets"`
}

type browserGrepMeshGuard struct {
	subject     string
	cookie      string
	fingerprint string
}
type browserGrepMeshGuardKey struct{}

// mcpAuth remains the only credential/profile authority. The bridge supplies
// the already stored credential after verifying an explicitly bound SSO user.
func (s *Server) authenticateBrowserGrepMesh(r *http.Request) (*http.Request, browserGrepMeshSession, error) {
	var session browserGrepMeshSession
	cfg, err := loadBrowserGrepMeshConfig()
	if err != nil {
		return nil, session, err
	}
	cookie := strings.Join(r.Header.Values("Cookie"), "; ")
	subject, err := browserGrepMeshSSOSubject(r.Context(), cfg, cookie)
	if err != nil {
		return nil, session, err
	}
	tokenID := cfg.bindings[subject]
	if tokenID == "" {
		return nil, session, browserGrepMeshDenied()
	}
	if err := s.refreshAccessState(); err != nil {
		return nil, session, browserGrepMeshUnavailable()
	}
	s.mu.Lock()
	record, found := s.managedMCP[tokenID]
	s.mu.Unlock()
	if !found || record.ID != tokenID || record.ClientID == "" || record.TokenKind != "durable" ||
		record.TokenValue == "" || record.ProfileID == "" || record.RevokedAt != 0 ||
		(record.ExpiresAt > 0 && !s.now().Before(time.Unix(record.ExpiresAt, 0))) {
		return nil, session, browserGrepMeshDenied()
	}
	delegated := r.Clone(r.Context())
	delegated.Header = make(http.Header)
	for _, name := range []string{"Accept", "Content-Type", "MCP-Protocol-Version", "Mcp-Method", "Mcp-Name"} {
		if values := r.Header.Values(name); len(values) != 0 {
			delegated.Header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	delegated.Header.Set("Authorization", "Bearer "+record.TokenValue)
	authResponse := &accessToolResponse{header: make(http.Header)}
	if !s.mcpAuth(authResponse, delegated) {
		if authResponse.status == http.StatusServiceUnavailable {
			return nil, session, browserGrepMeshUnavailable()
		}
		return nil, session, browserGrepMeshDenied()
	}
	claims, _ := delegated.Context().Value(authClaimsContextKey{}).(map[string]any)
	profile, bound := AccessProfileFromRequest(delegated)
	if !bound || profile.ID != record.ProfileID || profile.Version < 1 || profile.ReadRoots == nil || len(*profile.ReadRoots) == 0 ||
		firstString(claims, "jti") != tokenID || firstString(claims, "client_id") != record.ClientID {
		return nil, session, browserGrepMeshDenied()
	}
	targets := []string{}
	s.mu.Lock()
	agents := s.publicAgentsLocked(nil)
	s.mu.Unlock()
	for _, agent := range agents {
		if !isChildMCPAgent(agent) || firstString(agent.Meta, "child_ref") != "GrepMesh" ||
			!agentOwnerProfileAllowed(delegated, agent) || !agentOwnerConnectionAllowed(delegated, agent) {
			continue
		}
		if authorizeToolCall(delegated, agent.AgentID, "search") != nil &&
			authorizeToolCall(delegated, agent.AgentID, "search_text") != nil &&
			authorizeToolCall(delegated, agent.AgentID, "read_text") != nil {
			continue
		}
		targets = append(targets, agent.AgentID)
	}
	if len(targets) > 256 || len(*profile.ReadRoots) > 256 {
		return nil, session, browserGrepMeshUnavailable()
	}
	sort.Strings(targets)
	binding, err := json.Marshal(struct {
		Subject     string
		TokenID     string
		ClientID    string
		TokenDigest string
		SSOURL      string
		Origin      string
		Profile     AccessProfile
		Targets     []string
	}{subject, tokenID, record.ClientID, record.TokenDigest, cfg.ssoURL, cfg.origin, profile, targets})
	if err != nil || len(binding) > 256<<10 {
		return nil, session, browserGrepMeshUnavailable()
	}
	digest := sha256.Sum256(binding)
	session = browserGrepMeshSession{
		Principal: subject, ProfileID: profile.ID, ProfileVersion: profile.Version,
		RootsDigest: filesystemACLRootsDigest(profile), AuthzFingerprint: hex.EncodeToString(digest[:]),
		ReadRoots: append([]string(nil), (*profile.ReadRoots)...), AllowedTargets: targets,
	}
	guard := browserGrepMeshGuard{subject: subject, cookie: cookie, fingerprint: session.AuthzFingerprint}
	// Admission just fully verified this authority; seed the stale-limit window.
	browserGrepMeshVerified.Store(guard.fingerprint, time.Now())
	delegated = delegated.WithContext(context.WithValue(delegated.Context(), browserGrepMeshGuardKey{}, guard))
	return delegated, session, nil
}

// Native Tasks invokes this before each receipt, including every SSE update.
// An open subscription cannot outlive its SSO login, token, binding or profile:
// every receipt re-verifies, and a definitive rejection (revoked login, token,
// binding, profile, or a changed fingerprint) fails the receipt immediately.
// Only a Hub that is itself too busy to answer — commonly this search's own
// task fan-out — may defer a re-verification, never beyond the stale limit
// counted from the last successful verification (seeded at admission).
var browserGrepMeshVerified sync.Map // fingerprint -> time.Time of last full verification

const browserGrepMeshReceiptStaleLimit = 120 * time.Second

func (s *Server) revalidateBrowserGrepMeshReceipt(r *http.Request) error {
	if r == nil {
		return nil
	}
	guard, browser := r.Context().Value(browserGrepMeshGuardKey{}).(browserGrepMeshGuard)
	if !browser {
		return nil
	}
	now := time.Now()
	request := r.Clone(r.Context())
	request.Header = make(http.Header)
	request.Header.Set("Cookie", guard.cookie)
	_, session, err := s.authenticateBrowserGrepMesh(request)
	if err != nil {
		var failure *browserGrepMeshError
		if errors.As(err, &failure) && failure.status == http.StatusServiceUnavailable {
			// Keep the healthy task alive across a transient Hub stall: the
			// delegated call was fully verified at admission, and the stale
			// limit below bounds how long verification can be deferred.
			if verified, ok := browserGrepMeshVerified.Load(guard.fingerprint); ok &&
				now.Sub(verified.(time.Time)) < browserGrepMeshReceiptStaleLimit {
				return nil
			}
			return errors.New("browser authorization could not be re-verified")
		}
		return errors.New("browser authorization changed")
	}
	if session.AuthzFingerprint != guard.fingerprint {
		return errors.New("browser authorization changed")
	}
	browserGrepMeshVerified.Store(guard.fingerprint, now)
	return nil
}

func browserGrepMeshWriteError(w http.ResponseWriter, err error) {
	var failure *browserGrepMeshError
	if !errors.As(err, &failure) {
		failure = browserGrepMeshUnavailable()
	}
	writeJSON(w, failure.status, map[string]any{"error": failure.code, "detail": failure.detail})
}

func (s *Server) browserGrepMeshEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
	w.Header().Add("Vary", "Origin")
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		browserGrepMeshWriteError(w, browserGrepMeshDenied())
		return
	}
	cfg, err := loadBrowserGrepMeshConfig()
	if err != nil {
		browserGrepMeshWriteError(w, err)
		return
	}
	if origin := r.Header.Get("Origin"); (r.Method == http.MethodPost && origin != cfg.origin) || (origin != "" && origin != cfg.origin) {
		browserGrepMeshWriteError(w, browserGrepMeshDenied())
		return
	}
	path := strings.TrimPrefix(r.URL.Path, browserGrepMeshPrefix)
	if r.URL.RawQuery != "" || (path == "session" && r.Method != http.MethodGet) || (path != "session" && r.Method != http.MethodPost) {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "invalid_browser_request", "detail": "unsupported browser request"})
		return
	}
	delegated, session, err := s.authenticateBrowserGrepMesh(r)
	if err != nil {
		browserGrepMeshWriteError(w, err)
		return
	}
	if path == "session" {
		data, err := json.Marshal(session)
		if err != nil || len(data) > 128<<10 {
			browserGrepMeshWriteError(w, browserGrepMeshUnavailable())
			return
		}
		writeJSON(w, http.StatusOK, session)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, browserGrepMeshBodyLimit))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "invalid_browser_request", "detail": "browser request exceeds limit"})
		return
	}
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) != io.EOF || firstString(body, "jsonrpc") != "2.0" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_browser_request", "detail": "one JSON-RPC object is required"})
		return
	}
	method, params := firstString(body, "method"), mapValue(body["params"])
	// Only the native Tasks branch carries the caller and filesystem policy.
	// Use its capability helper so browser calls cannot fall back to legacy
	// synchronous dispatch, even if a client omits or changes protocol metadata.
	if method == "tools/call" && (!mcpTasksOptedIn(params) ||
		strings.TrimSpace(delegated.Header.Get("MCP-Protocol-Version")) != mcpProtocolVersion ||
		firstString(mapValue(params["_meta"]), "io.modelcontextprotocol/protocolVersion") != mcpProtocolVersion) {
		browserGrepMeshWriteError(w, browserGrepMeshDenied())
		return
	}
	// Transport authority comes only from the authenticated native queue.
	// Reject the presence of client-supplied policy, including explicit null.
	if _, supplied := mapValue(params["_meta"])["io.gptadmin/filesystem-acl"]; supplied {
		browserGrepMeshWriteError(w, browserGrepMeshDenied())
		return
	}
	for _, key := range []string{"filesystem_acl", "_meta", "origin_host", "hop_count", "request_id"} {
		if _, supplied := mapValue(params["arguments"])[key]; supplied {
			browserGrepMeshWriteError(w, browserGrepMeshDenied())
			return
		}
	}
	allowed := false
	if path == "mcp" {
		switch method {
		case "tasks/get", "tasks/cancel":
			id := firstString(params, "taskId")
			allowed = id != "" && len(id) <= 256
		case "subscriptions/listen":
			filter := mapValue(params["notifications"])
			ids := sliceValue(filter["taskIds"])
			allowed = len(filter) == 1 && len(ids) > 0 && len(ids) <= 64
			for _, value := range ids {
				id, valid := value.(string)
				allowed = allowed && valid && id != "" && len(id) <= 256
			}
		}
	} else if strings.HasPrefix(path, "server/") && strings.HasSuffix(path, "/mcp") {
		slug := strings.TrimSuffix(strings.TrimPrefix(path, "server/"), "/mcp")
		if slug != "" && len(slug) <= 256 && !strings.ContainsAny(slug, "/\\") {
			agent, found := s.resolveExposedAgent(slug)
			name := firstString(params, "name")
			allowed = found && slug == agentSlug(agent.AgentID) && isChildMCPAgent(agent) &&
				firstString(agent.Meta, "child_ref") == "GrepMesh" && containsString(session.AllowedTargets, agent.AgentID) &&
				method == "tools/call" && (name == "search" || name == "search_text" || name == "read_text")
			if allowed {
				parent := firstString(agent.Meta, "parent_server_id")
				s.mu.Lock()
				local := s.localExecutors[parent] != nil
				_, routed := s.nodePeerRouteLocked(parent)
				remote := routed || s.nodeConns.get(parent) != nil
				s.mu.Unlock()
				if !local && remote {
					// Native Tasks retain browser ownership in this Hub's ledger.
					// A standalone peer polls its own queue, not this one. Plain
					// HTTP forwarding cannot carry the verified browser guard.
					sharedQueue, queueErr := s.quorumExecutorUsesLeaderQueue(r.Context(), parent)
					if queueErr != nil || !sharedQueue {
						browserGrepMeshWriteError(w, &browserGrepMeshError{http.StatusServiceUnavailable, "browser_task_owner_unavailable", "remote browser Tasks require an owner-local browser endpoint or a verified quorum leader queue"})
						return
					}
				}
			}
		}
	}
	if !allowed {
		browserGrepMeshWriteError(w, browserGrepMeshDenied())
		return
	}
	delegated.URL = new(url.URL)
	*delegated.URL = *r.URL
	delegated.URL.Path = "/" + path
	delegated.URL.RawPath = ""
	delegated.RequestURI = delegated.URL.RequestURI()
	delegated.Body = io.NopCloser(bytes.NewReader(data))
	delegated.ContentLength = int64(len(data))
	if path == "mcp" {
		s.mcpEndpoint(w, delegated)
	} else {
		s.serverMCPEndpoint(w, delegated)
	}
}
