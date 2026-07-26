package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type proxyTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *proxyTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *proxyTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func testNetworkProxyPolicy() NetworkProxyPolicy {
	policy, err := networkProxyPolicyFromArgs(map[string]any{
		"scope":        "lan",
		"agent_id":     "shell:proxy-1",
		"mode":         "pull",
		"target_cidrs": []string{"10.20.0.0/24"},
		"target_ports": []int{443},
		"max_streams":  2,
		"max_bytes":    1 << 20,
		"lease":        time.Minute,
	})
	if err != nil {
		panic(err)
	}
	return policy
}

func newNetworkProxyTestServer(t *testing.T, clock *proxyTestClock) *Server {
	t.Helper()
	dir := t.TempDir()
	s := New(Config{
		ConfigDir:             dir,
		CtlToken:              "control-token",
		OAuthClientSecret:     "network-proxy-test-secret",
		PublicOrigin:          "https://hub.example",
		MCPResource:           "https://hub.example",
		Now:                   clock.Now,
		NetworkProxyStateFile: filepath.Join(dir, "network_proxy_state.json"),
	})
	s.mu.Lock()
	for _, profileID := range []string{"proxy-profile", "other-profile"} {
		s.accessProfiles[profileID] = AccessProfile{
			ID:             profileID,
			Name:           profileID,
			AccessMode:     accessModeFull,
			AllowedTargets: []string{"hub", "shell:proxy-1"},
			AllowedTools:   []string{"network_proxy_request", "network_proxy_approve", "network_proxy_issue", "network_proxy_status", "network_proxy_revoke"},
			Version:        1,
			UpdatedAt:      clock.Now(),
		}
	}
	s.agents["shell:proxy-1"] = &Agent{
		AgentID: "shell:proxy-1",
		Name:    "proxy-1",
		Status:  "stale",
		Meta:    map[string]any{"approved": true},
	}
	s.mu.Unlock()
	return s
}

func networkProxyMCPToken(t *testing.T, s *Server, profileID string) string {
	t.Helper()
	token, err := s.signJWT(map[string]any{
		"sub":        "network-proxy-test",
		"aud":        "https://hub.example",
		"resource":   "https://hub.example",
		"scope":      "gptadmin.exec",
		"profile_id": profileID,
		"exp":        time.Now().Add(time.Hour).Unix(),
		"iat":        time.Now().Unix(),
		"kid":        defaultJWTKeyID,
	})
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return token
}

func networkProxyMCPCall(t *testing.T, s *Server, profileID, toolName string, arguments map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"target": "hub", "tool_name": toolName, "arguments": arguments})
	if err != nil {
		t.Fatalf("marshal MCP call: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+networkProxyMCPToken(t, s, profileID))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func proxyControlRequest(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func requestProxyCapabilityHTTP(t *testing.T, s *Server, policy NetworkProxyPolicy) NetworkProxyCapability {
	t.Helper()
	rec := proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/request", map[string]any{
		"profile_id": "proxy-profile",
		"policy":     policy,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("request status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Capability NetworkProxyCapability `json:"capability"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode request response: %v", err)
	}
	return response.Capability
}

func approveProxyCapabilityHTTP(t *testing.T, s *Server, capabilityID string) NetworkProxyCapability {
	t.Helper()
	rec := proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/approve", map[string]any{"capability_id": capabilityID})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Capability NetworkProxyCapability `json:"capability"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	return response.Capability
}

func TestNetworkProxyScopesAreExplicitAndIsolateDestinations(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	controller, err := NewNetworkProxyController("", clock.Now, nil)
	if err != nil {
		t.Fatalf("NewNetworkProxyController: %v", err)
	}

	missingScope := testNetworkProxyPolicy()
	missingScopeJSON, err := json.Marshal(missingScope)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	var missingScopeArgs map[string]any
	if err := json.Unmarshal(missingScopeJSON, &missingScopeArgs); err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	delete(missingScopeArgs, "scope")
	missingScope, err = networkProxyPolicyFromArgs(missingScopeArgs)
	if err != nil {
		t.Fatalf("decode missing-scope policy: %v", err)
	}
	if _, err := controller.Request("proxy-profile", missingScope); err != ErrNetworkProxyInvalid {
		t.Fatalf("missing scope error=%v, want %v", err, ErrNetworkProxyInvalid)
	}

	lanPublic, err := networkProxyPolicyFromArgs(map[string]any{
		"scope": "lan", "agent_id": "shell:proxy-1", "mode": "pull",
		"target_cidrs": []string{"8.8.8.0/24"}, "target_ports": []int{443},
		"max_streams": 2, "max_bytes": 1 << 20, "lease": time.Minute,
	})
	if err != nil {
		t.Fatalf("decode LAN policy: %v", err)
	}
	if _, err := controller.Request("proxy-profile", lanPublic); err != ErrNetworkProxyInvalid {
		t.Fatalf("public LAN CIDR error=%v, want %v", err, ErrNetworkProxyInvalid)
	}

	lan, err := controller.Request("proxy-profile", testNetworkProxyPolicy())
	if err != nil {
		t.Fatalf("request LAN capability: %v", err)
	}
	if _, err := controller.Approve(lan.CapabilityID); err != nil {
		t.Fatalf("approve LAN capability: %v", err)
	}
	if _, _, err := controller.IssueStreamGrants(lan.CapabilityID, "10.20.0.255:443"); err != ErrNetworkProxyTargetDenied {
		t.Fatalf("LAN broadcast error=%v, want %v", err, ErrNetworkProxyTargetDenied)
	}

	internetPolicy, err := networkProxyPolicyFromArgs(map[string]any{
		"scope": "internet_egress", "agent_id": "shell:proxy-1", "mode": "pull",
		"target_cidrs": []string{"0.0.0.0/0", "::/0"}, "target_ports": []int{443},
		"max_streams": 20, "max_bytes": 1 << 20, "lease": time.Minute,
	})
	if err != nil {
		t.Fatalf("decode internet policy: %v", err)
	}
	internet, err := controller.Request("proxy-profile", internetPolicy)
	if err != nil {
		t.Fatalf("request internet capability: %v", err)
	}
	if _, err := controller.Approve(internet.CapabilityID); err != nil {
		t.Fatalf("approve internet capability: %v", err)
	}
	for _, target := range []string{
		"10.0.0.1:443",
		"127.0.0.1:443",
		"169.254.169.254:443",
		"100.100.100.200:443",
		"100.64.0.1:443",
		"192.0.2.1:443",
		"198.18.0.1:443",
		"198.51.100.1:443",
		"203.0.113.1:443",
		"240.0.0.1:443",
		"[::ffff:100.64.0.1]:443",
		"[::ffff:192.0.2.1]:443",
		"[::ffff:198.51.100.1]:443",
		"[2001:db8::1]:443",
		"224.0.0.1:443",
		"255.255.255.255:443",
	} {
		if _, _, err := controller.IssueStreamGrants(internet.CapabilityID, target); err != ErrNetworkProxyTargetDenied {
			t.Errorf("internet target %s error=%v, want %v", target, err, ErrNetworkProxyTargetDenied)
		}
	}
	if _, _, err := controller.IssueStreamGrants(internet.CapabilityID, "8.8.8.8:443"); err != nil {
		t.Fatalf("public internet target denied: %v", err)
	}
}

func TestNetworkProxyMCPDeniesCrossProfileCapabilityOperations(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	policy := testNetworkProxyPolicy()

	t.Run("request", func(t *testing.T) {
		rec := networkProxyMCPCall(t, s, "proxy-profile", "network_proxy_request", map[string]any{
			"profile_id": "other-profile",
			"policy":     policy,
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-profile request status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	for _, toolName := range []string{"network_proxy_approve", "network_proxy_status", "network_proxy_revoke"} {
		t.Run(toolName, func(t *testing.T) {
			capability, err := s.requestNetworkProxyCapability("proxy-profile", policy)
			if err != nil {
				t.Fatalf("request capability: %v", err)
			}
			rec := networkProxyMCPCall(t, s, "other-profile", toolName, map[string]any{"capability_id": capability.CapabilityID})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("cross-profile %s status=%d body=%s", toolName, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestNetworkProxyApprovalRevalidatesProfileAndAgent(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}

	t.Run("profile permissions", func(t *testing.T) {
		s := newNetworkProxyTestServer(t, clock)
		capability, err := s.requestNetworkProxyCapability("proxy-profile", testNetworkProxyPolicy())
		if err != nil {
			t.Fatalf("request capability: %v", err)
		}
		s.mu.Lock()
		profile := s.accessProfiles["proxy-profile"]
		profile.AllowedTargets = []string{"hub"}
		s.accessProfiles["proxy-profile"] = profile
		s.mu.Unlock()
		if _, err := s.approveNetworkProxyCapability(capability.CapabilityID); err != ErrNetworkProxyUnauthorized {
			t.Fatalf("approve after profile restriction error=%v, want %v", err, ErrNetworkProxyUnauthorized)
		}
	})

	t.Run("approved agent", func(t *testing.T) {
		s := newNetworkProxyTestServer(t, clock)
		capability, err := s.requestNetworkProxyCapability("proxy-profile", testNetworkProxyPolicy())
		if err != nil {
			t.Fatalf("request capability: %v", err)
		}
		s.mu.Lock()
		s.agents["shell:proxy-1"].Meta["approved"] = false
		s.mu.Unlock()
		if _, err := s.approveNetworkProxyCapability(capability.CapabilityID); err != ErrNetworkProxyUnauthorized {
			t.Fatalf("approve after agent removal error=%v, want %v", err, ErrNetworkProxyUnauthorized)
		}
	})
}

func TestNetworkProxyIssueOffersAuthenticatedRoleBoundOneTimeGrants(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	capability := requestProxyCapabilityHTTP(t, s, testNetworkProxyPolicy())
	approveProxyCapabilityHTTP(t, s, capability.CapabilityID)

	rec := networkProxyMCPCall(t, s, "proxy-profile", "network_proxy_issue", map[string]any{
		"capability_id": capability.CapabilityID,
		"target":        "10.20.0.8:443",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Response struct {
			ClientGrant ProxyStreamGrant `json:"client_grant"`
			AgentGrant  ProxyStreamGrant `json:"agent_grant"`
		} `json:"response"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	clientGrant := response.Response.ClientGrant
	agentGrant := response.Response.AgentGrant
	if clientGrant.Token == "" || agentGrant.Token == "" || clientGrant.Role != "client" || agentGrant.Role != "agent" {
		t.Fatalf("invalid role-bound grants: client=%+v agent=%+v", clientGrant, agentGrant)
	}
	if clientGrant.StreamID == "" || clientGrant.StreamID != agentGrant.StreamID || clientGrant.Target != "10.20.0.8:443" || agentGrant.Target != clientGrant.Target {
		t.Fatalf("grants lost stream/target binding: client=%+v agent=%+v", clientGrant, agentGrant)
	}

	for _, grant := range []ProxyStreamGrant{clientGrant, agentGrant} {
		if _, err := s.networkProxy.Open(grant.Token, grant.Role); err != nil {
			t.Fatalf("first %s open: %v", grant.Role, err)
		}
		if _, err := s.networkProxy.Open(grant.Token, grant.Role); err != ErrNetworkProxyGrantUsed {
			t.Fatalf("replayed %s grant error=%v, want %v", grant.Role, err, ErrNetworkProxyGrantUsed)
		}
	}

	other := networkProxyMCPCall(t, s, "other-profile", "network_proxy_issue", map[string]any{
		"capability_id": capability.CapabilityID,
		"target":        "10.20.0.9:443",
	})
	if other.Code != http.StatusForbidden {
		t.Fatalf("cross-profile issue status=%d body=%s", other.Code, other.Body.String())
	}

	s.mu.Lock()
	auditJSON, err := json.Marshal(s.audit)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("marshal audit: %v", err)
	}
	auditText := string(auditJSON)
	for _, secret := range []string{clientGrant.Token, agentGrant.Token, clientGrant.Target} {
		if strings.Contains(auditText, secret) {
			t.Fatalf("audit leaked issued grant material %q: %s", secret, auditText)
		}
	}
}

func TestNetworkProxyOpenAPIDocumentsControlContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "public", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read public/openapi.yaml: %v", err)
	}
	doc := string(b)
	for _, required := range []string{
		"/proxy-control/v1/request:",
		"/proxy-control/v1/approve:",
		"/proxy-control/v1/issue:",
		"/proxy-control/v1/open:",
		"/proxy-control/v1/status:",
		"/proxy-control/v1/revoke:",
		"NetworkProxyPolicy:",
		"ProxyStreamGrant:",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("public/openapi.yaml missing %q", required)
		}
	}
}

func TestNetworkProxyRequestDeniesUnauthorizedProfileHTTP(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	s.mu.Lock()
	profile := s.accessProfiles["proxy-profile"]
	profile.AllowedTools = []string{"status"}
	s.accessProfiles[profile.ID] = profile
	s.mu.Unlock()

	rec := proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/request", map[string]any{
		"profile_id": "proxy-profile",
		"policy":     testNetworkProxyPolicy(),
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNetworkProxyRequestDeniesUnapprovedAgentHTTP(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	s.mu.Lock()
	s.agents["shell:proxy-1"].Meta["approved"] = false
	s.mu.Unlock()

	rec := proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/request", map[string]any{
		"profile_id": "proxy-profile",
		"policy":     testNetworkProxyPolicy(),
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNetworkProxyExpiredCapabilityCannotIssueGrants(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	capability := requestProxyCapabilityHTTP(t, s, testNetworkProxyPolicy())
	approveProxyCapabilityHTTP(t, s, capability.CapabilityID)
	clock.Advance(2 * time.Minute)

	_, _, err := s.networkProxy.IssueStreamGrants(capability.CapabilityID, "10.20.0.8:443")
	if err != ErrNetworkProxyExpired {
		t.Fatalf("IssueStreamGrants error=%v, want %v", err, ErrNetworkProxyExpired)
	}

	rec := proxyControlRequest(t, s, http.MethodGet, "/proxy-control/v1/status?capability_id="+capability.CapabilityID, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"expired"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNetworkProxyRejectsTargetOutsideCIDRAndPort(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	controller, err := NewNetworkProxyController("", clock.Now, nil)
	if err != nil {
		t.Fatalf("NewNetworkProxyController: %v", err)
	}
	capability, err := controller.Request("proxy-profile", testNetworkProxyPolicy())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := controller.Approve(capability.CapabilityID); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if _, _, err := controller.IssueStreamGrants(capability.CapabilityID, "10.21.0.8:443"); err != ErrNetworkProxyTargetDenied {
		t.Fatalf("outside CIDR error=%v, want %v", err, ErrNetworkProxyTargetDenied)
	}
	if _, _, err := controller.IssueStreamGrants(capability.CapabilityID, "10.20.0.8:80"); err != ErrNetworkProxyTargetDenied {
		t.Fatalf("outside port error=%v, want %v", err, ErrNetworkProxyTargetDenied)
	}
}

func TestNetworkProxyOpenRejectsWrongRoleAndReusedJTIHTTP(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	capability := requestProxyCapabilityHTTP(t, s, testNetworkProxyPolicy())
	approveProxyCapabilityHTTP(t, s, capability.CapabilityID)
	clientGrant, _, err := s.networkProxy.IssueStreamGrants(capability.CapabilityID, "10.20.0.8:443")
	if err != nil {
		t.Fatalf("IssueStreamGrants: %v", err)
	}

	wrongRole := proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/open", map[string]any{
		"token": clientGrant.Token,
		"role":  "agent",
	})
	if wrongRole.Code != http.StatusForbidden {
		t.Fatalf("wrong role status=%d body=%s", wrongRole.Code, wrongRole.Body.String())
	}

	opened := proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/open", map[string]any{
		"token": clientGrant.Token,
		"role":  "client",
	})
	if opened.Code != http.StatusOK {
		t.Fatalf("open status=%d body=%s", opened.Code, opened.Body.String())
	}
	if strings.Contains(opened.Body.String(), clientGrant.Token) || strings.Contains(opened.Body.String(), "10.20.0.8:443") {
		t.Fatalf("open response leaked grant material: %s", opened.Body.String())
	}

	reused := proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/open", map[string]any{
		"token": clientGrant.Token,
		"role":  "client",
	})
	if reused.Code != http.StatusConflict {
		t.Fatalf("reused status=%d body=%s", reused.Code, reused.Body.String())
	}
}

func TestNetworkProxyRevokeIsIdempotentAndSignalsOnce(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	var mu sync.Mutex
	var signals []string
	controller, err := NewNetworkProxyController("", clock.Now, func(capabilityID string) {
		mu.Lock()
		signals = append(signals, capabilityID)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewNetworkProxyController: %v", err)
	}
	capability, err := controller.Request("proxy-profile", testNetworkProxyPolicy())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := controller.Approve(capability.CapabilityID); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	first, err := controller.Revoke(capability.CapabilityID)
	if err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	second, err := controller.Revoke(capability.CapabilityID)
	if err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	if first.State != "draining" || second.State != "draining" {
		t.Fatalf("states=%q,%q, want draining", first.State, second.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(signals) != 1 || signals[0] != capability.CapabilityID {
		t.Fatalf("signals=%v, want one capability signal", signals)
	}
}

func TestNetworkProxyRestartDoesNotResurrectActiveCapability(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	statePath := filepath.Join(t.TempDir(), "network_proxy_state.json")
	controller, err := NewNetworkProxyController(statePath, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewNetworkProxyController: %v", err)
	}
	capability, err := controller.Request("proxy-profile", testNetworkProxyPolicy())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := controller.Approve(capability.CapabilityID); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	restarted, err := NewNetworkProxyController(statePath, clock.Now, nil)
	if err != nil {
		t.Fatalf("restart NewNetworkProxyController: %v", err)
	}
	status, err := restarted.Status(capability.CapabilityID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != "expired" {
		t.Fatalf("restart state=%q, want expired", status.State)
	}
	if _, _, err := restarted.IssueStreamGrants(capability.CapabilityID, "10.20.0.8:443"); err != ErrNetworkProxyExpired {
		t.Fatalf("restart IssueStreamGrants error=%v, want %v", err, ErrNetworkProxyExpired)
	}
}

func TestNetworkProxyAuditOmitsTokensAndTargets(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	capability := requestProxyCapabilityHTTP(t, s, testNetworkProxyPolicy())
	approveProxyCapabilityHTTP(t, s, capability.CapabilityID)
	clientGrant, _, err := s.networkProxy.IssueStreamGrants(capability.CapabilityID, "10.20.0.8:443")
	if err != nil {
		t.Fatalf("IssueStreamGrants: %v", err)
	}
	rec := proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/open", map[string]any{
		"token": clientGrant.Token,
		"role":  clientGrant.Role,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("open status=%d body=%s", rec.Code, rec.Body.String())
	}

	s.mu.Lock()
	auditJSON, err := json.Marshal(s.audit)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("marshal audit: %v", err)
	}
	auditText := string(auditJSON)
	if strings.Contains(auditText, clientGrant.Token) || strings.Contains(auditText, "10.20.0.8:443") {
		t.Fatalf("audit leaked token or target: %s", auditText)
	}
}

func TestNetworkProxyControlDoesNotTouchCommandQueuesOrHeartbeat(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	capability := requestProxyCapabilityHTTP(t, s, testNetworkProxyPolicy())
	approveProxyCapabilityHTTP(t, s, capability.CapabilityID)
	proxyControlRequest(t, s, http.MethodPost, "/proxy-control/v1/revoke", map[string]any{"capability_id": capability.CapabilityID})

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.relayJobs) != 0 || len(s.relayQueues) != 0 || len(s.shellJobs) != 0 || len(s.shellQueues) != 0 {
		t.Fatalf("proxy control touched command state: relay_jobs=%d relay_queues=%d shell_jobs=%d shell_queues=%d", len(s.relayJobs), len(s.relayQueues), len(s.shellJobs), len(s.shellQueues))
	}
	if got := s.agents["shell:proxy-1"].LastSeen; got != 0 {
		t.Fatalf("proxy control reused heartbeat liveness: last_seen=%v", got)
	}
}

func TestNetworkProxyHubToolSchemasAreRegistered(t *testing.T) {
	want := map[string]bool{
		"network_proxy_request": false,
		"network_proxy_approve": false,
		"network_proxy_open":    false,
		"network_proxy_status":  false,
		"network_proxy_revoke":  false,
	}
	for _, tool := range hubTools() {
		name, _ := tool["name"].(string)
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("Hub tool %q is not registered", name)
		}
	}
}

func allowNetworkAccessTools(t *testing.T, s *Server, profileID string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	profile := s.accessProfiles[profileID]
	profile.AllowedTools = append(profile.AllowedTools,
		"network_access_plan",
		"network_access_enable",
		"network_access_status",
		"network_access_disable",
	)
	s.accessProfiles[profileID] = profile
}

func networkProxyMCPResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("MCP status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Response map[string]any `json:"response"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode MCP response: %v", err)
	}
	return response.Response
}

func decodeNetworkProxyCapability(t *testing.T, raw any) NetworkProxyCapability {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal capability: %v", err)
	}
	var capability NetworkProxyCapability
	if err := json.Unmarshal(b, &capability); err != nil {
		t.Fatalf("decode capability: %v", err)
	}
	return capability
}

func TestNetworkAccessAliasesAreRegisteredWithSafeSchemas(t *testing.T) {
	want := map[string]bool{
		"network_access_plan":    false,
		"network_access_enable":  false,
		"network_access_status":  false,
		"network_access_disable": false,
	}
	for _, tool := range hubTools() {
		name, _ := tool["name"].(string)
		if _, ok := want[name]; !ok {
			continue
		}
		want[name] = true
		description, _ := tool["description"].(string)
		lower := strings.ToLower(description)
		for _, phrase := range []string{"lan", "internet_egress", "bounded tcp", "no udp"} {
			if !strings.Contains(lower, phrase) {
				t.Errorf("tool %q description missing %q: %q", name, phrase, description)
			}
		}
		schema := mapValue(tool["inputSchema"])
		required := map[string]bool{}
		switch requiredValues := schema["required"].(type) {
		case []string:
			for _, value := range requiredValues {
				required[value] = true
			}
		case []any:
			for _, item := range requiredValues {
				if value, ok := item.(string); ok {
					required[value] = true
				}
			}
		}
		switch name {
		case "network_access_plan":
			if !required["policy"] {
				t.Errorf("tool %q must require policy", name)
			}
		case "network_access_enable", "network_access_disable":
			if !required["explicit_confirm"] {
				t.Errorf("tool %q must require explicit_confirm", name)
			}
			if !strings.Contains(lower, "explicit_confirm") {
				t.Errorf("tool %q description must explain explicit_confirm", name)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("Hub tool %q is not registered", name)
		}
	}
}

func TestNetworkAccessAliasesUseTheExistingCapabilityFlow(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	allowNetworkAccessTools(t, s, "proxy-profile")

	planned := networkProxyMCPCall(t, s, "proxy-profile", "network_access_plan", map[string]any{
		"policy": testNetworkProxyPolicy(),
	})
	if planned.Code != http.StatusCreated {
		t.Fatalf("plan status=%d body=%s", planned.Code, planned.Body.String())
	}
	plannedResponse := networkProxyMCPResponse(t, planned)
	capability := decodeNetworkProxyCapability(t, plannedResponse["capability"])
	if capability.State != "pending" {
		t.Fatalf("plan state=%q, want pending; plan must not auto-enable", capability.State)
	}

	enabled := networkProxyMCPCall(t, s, "proxy-profile", "network_access_enable", map[string]any{
		"capability_id":    capability.CapabilityID,
		"explicit_confirm": true,
	})
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", enabled.Code, enabled.Body.String())
	}
	enabledCapability := decodeNetworkProxyCapability(t, networkProxyMCPResponse(t, enabled)["capability"])
	if enabledCapability.State != "active" {
		t.Fatalf("enable state=%q, want active", enabledCapability.State)
	}

	status := networkProxyMCPCall(t, s, "proxy-profile", "network_access_status", map[string]any{
		"capability_id": capability.CapabilityID,
	})
	if status.Code != http.StatusOK {
		t.Fatalf("status status=%d body=%s", status.Code, status.Body.String())
	}
	if got := decodeNetworkProxyCapability(t, networkProxyMCPResponse(t, status)["capability"]); got.State != "active" {
		t.Fatalf("status state=%q, want active", got.State)
	}

	disabled := networkProxyMCPCall(t, s, "proxy-profile", "network_access_disable", map[string]any{
		"capability_id":    capability.CapabilityID,
		"explicit_confirm": true,
	})
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	if got := decodeNetworkProxyCapability(t, networkProxyMCPResponse(t, disabled)["capability"]); got.State != "draining" {
		t.Fatalf("disable state=%q, want draining", got.State)
	}
}

func TestNetworkAccessEnableAndDisableRequireExplicitConfirmation(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	allowNetworkAccessTools(t, s, "proxy-profile")
	capability := requestProxyCapabilityHTTP(t, s, testNetworkProxyPolicy())

	withoutEnableConfirmation := networkProxyMCPCall(t, s, "proxy-profile", "network_access_enable", map[string]any{
		"capability_id": capability.CapabilityID,
	})
	if withoutEnableConfirmation.Code != http.StatusBadRequest {
		t.Fatalf("enable without confirmation status=%d body=%s", withoutEnableConfirmation.Code, withoutEnableConfirmation.Body.String())
	}
	if current, err := s.networkProxy.Status(capability.CapabilityID); err != nil || current.State != "pending" {
		t.Fatalf("enable without confirmation mutated capability: state=%q err=%v", current.State, err)
	}

	confirmed := networkProxyMCPCall(t, s, "proxy-profile", "network_access_enable", map[string]any{
		"capability_id": capability.CapabilityID, "explicit_confirm": true,
	})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	withoutDisableConfirmation := networkProxyMCPCall(t, s, "proxy-profile", "network_access_disable", map[string]any{
		"capability_id": capability.CapabilityID, "explicit_confirm": false,
	})
	if withoutDisableConfirmation.Code != http.StatusBadRequest {
		t.Fatalf("disable without confirmation status=%d body=%s", withoutDisableConfirmation.Code, withoutDisableConfirmation.Body.String())
	}
	if current, err := s.networkProxy.Status(capability.CapabilityID); err != nil || current.State != "active" {
		t.Fatalf("disable without confirmation mutated capability: state=%q err=%v", current.State, err)
	}
}

func TestNetworkAccessAliasesDenyCrossProfileOperations(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	allowNetworkAccessTools(t, s, "proxy-profile")
	allowNetworkAccessTools(t, s, "other-profile")
	capability := requestProxyCapabilityHTTP(t, s, testNetworkProxyPolicy())

	for _, testCase := range []struct {
		name string
		args map[string]any
	}{
		{name: "enable", args: map[string]any{"capability_id": capability.CapabilityID, "explicit_confirm": true}},
		{name: "status", args: map[string]any{"capability_id": capability.CapabilityID}},
		{name: "disable", args: map[string]any{"capability_id": capability.CapabilityID, "explicit_confirm": true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rec := networkProxyMCPCall(t, s, "other-profile", "network_access_"+testCase.name, testCase.args)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("cross-profile %s status=%d body=%s", testCase.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestNetworkAccessAliasesDoNotTouchCommandQueuesOrHeartbeat(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	allowNetworkAccessTools(t, s, "proxy-profile")
	planned := networkProxyMCPCall(t, s, "proxy-profile", "network_access_plan", map[string]any{"policy": testNetworkProxyPolicy()})
	capability := decodeNetworkProxyCapability(t, networkProxyMCPResponse(t, planned)["capability"])
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{name: "network_access_enable", args: map[string]any{"capability_id": capability.CapabilityID, "explicit_confirm": true}},
		{name: "network_access_status", args: map[string]any{"capability_id": capability.CapabilityID}},
		{name: "network_access_disable", args: map[string]any{"capability_id": capability.CapabilityID, "explicit_confirm": true}},
	} {
		rec := networkProxyMCPCall(t, s, "proxy-profile", call.name, call.args)
		if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
			t.Fatalf("%s status=%d body=%s", call.name, rec.Code, rec.Body.String())
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.relayJobs) != 0 || len(s.relayQueues) != 0 || len(s.shellJobs) != 0 || len(s.shellQueues) != 0 {
		t.Fatalf("network access aliases touched command state: relay_jobs=%d relay_queues=%d shell_jobs=%d shell_queues=%d", len(s.relayJobs), len(s.relayQueues), len(s.shellJobs), len(s.shellQueues))
	}
	if got := s.agents["shell:proxy-1"].LastSeen; got != 0 {
		t.Fatalf("network access aliases reused heartbeat liveness: last_seen=%v", got)
	}
}

func TestNetworkProxyDeniedPolicyAuditIncludesCallerAndDecision(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := newNetworkProxyTestServer(t, clock)
	s.mu.Lock()
	s.accessProfiles["denied-profile"] = AccessProfile{
		ID:             "denied-profile",
		AccessMode:     accessModeFull,
		AllowedTargets: []string{"hub"},
		AllowedTools:   []string{"network_proxy_request"},
		Version:        1,
	}
	s.mu.Unlock()
	_, status := s.callNetworkProxyTool("denied-profile", "network_proxy_request", map[string]any{"policy": testNetworkProxyPolicy()})
	if status != http.StatusForbidden {
		t.Fatalf("denied proxy request status = %d, want 403", status)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.audit) - 1; i >= 0; i-- {
		if s.audit[i].Name != "hub_tool_denied" {
			continue
		}
		if s.audit[i].Fields["profile_id"] != "denied-profile" || s.audit[i].Fields["decision"] != "deny" {
			t.Fatalf("denial audit fields = %#v", s.audit[i].Fields)
		}
		if s.audit[i].Fields["reason"] == "" {
			t.Fatal("denial audit omitted reason")
		}
		return
	}
	t.Fatal("no hub_tool_denied audit event recorded")
}
