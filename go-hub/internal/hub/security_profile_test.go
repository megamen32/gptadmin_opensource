package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProcessSecurityProfileIsExplicitAndPersistent(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl"})
	request := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/admin/api/security/profile", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer ctl")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}

	initial := request(http.MethodGet, "")
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"process_profile":{"mode":"normal"`) || !strings.Contains(initial.Body.String(), `"bearer_profile":{"mode":"normal"`) {
		t.Fatalf("unexpected default profile: %d %s", initial.Code, initial.Body.String())
	}

	maximum := request(http.MethodPut, `{"mode":"maximum","no_new_privileges":true,"private_tmp":true,"protect_system":true,"protect_home":true,"allow_privileged_execution":false}`)
	if maximum.Code != http.StatusOK || !strings.Contains(maximum.Body.String(), `"mode":"maximum"`) {
		t.Fatalf("maximum profile rejected: %d %s", maximum.Code, maximum.Body.String())
	}

	invalid := request(http.MethodPut, `{"mode":"normal","no_new_privileges":true}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid normal profile accepted: %d %s", invalid.Code, invalid.Body.String())
	}

	restarted := New(Config{ConfigDir: s.cfg.ConfigDir, CtlToken: "ctl"})
	if restarted.securitySnapshot().Process.Mode != processSecurityMaximum {
		t.Fatalf("profile did not persist: %#v", restarted.securitySnapshot().Process)
	}

	customBearer := `{"mode":"custom","no_new_privileges":false,"private_tmp":false,"protect_system":false,"protect_home":false,"allow_privileged_execution":true,"bearer_profile":{"mode":"custom","require_issuer":false,"require_audience":true,"require_resource":true,"require_scope":false,"require_subject":false,"require_issued_at":false,"require_expiry":true,"require_pkce":false,"enforce_token_lifecycle":true,"enforce_redirect_allowlist":true,"enforce_resource_allowlist":true}}`
	if changed := request(http.MethodPut, customBearer); changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"bearer_profile":{"mode":"custom"`) {
		t.Fatalf("custom bearer profile rejected: %d %s", changed.Code, changed.Body.String())
	}
	badMaximumBearer := `{"mode":"normal","no_new_privileges":false,"private_tmp":false,"protect_system":false,"protect_home":false,"allow_privileged_execution":true,"bearer_profile":{"mode":"maximum"}}`
	if rejected := request(http.MethodPut, badMaximumBearer); rejected.Code != http.StatusBadRequest {
		t.Fatalf("incomplete maximum bearer profile accepted: %d %s", rejected.Code, rejected.Body.String())
	}
}

func TestBearerProfileControlsClaimsButNeverSignature(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", OAuthClientSecret: "secret", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example"})
	claims := map[string]any{
		"aud": s.cfg.MCPResource, "resource": s.cfg.MCPResource, "scope": "gptadmin.read",
		"sub": "client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "kid": s.jwtKeyID(),
	}
	token, err := s.signJWT(claims)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://hub.example/mcp", nil)
	if _, err := s.verifyJWTForRequest(req, token); err != nil {
		t.Fatalf("normal profile rejected a legacy token without issuer: %v", err)
	}
	custom := `{"mode":"normal","no_new_privileges":false,"private_tmp":false,"protect_system":false,"protect_home":false,"allow_privileged_execution":true,"bearer_profile":{"mode":"custom","require_issuer":false,"require_audience":true,"require_resource":true,"require_scope":true,"require_subject":true,"require_issued_at":true,"require_expiry":true,"require_pkce":false,"enforce_token_lifecycle":true,"enforce_redirect_allowlist":true,"enforce_resource_allowlist":true}}`
	change := httptest.NewRequest(http.MethodPut, "/admin/api/security/profile", strings.NewReader(custom))
	change.Header.Set("Authorization", "Bearer ctl")
	change.Header.Set("Content-Type", "application/json")
	changed := httptest.NewRecorder()
	s.Handler().ServeHTTP(changed, change)
	if changed.Code != http.StatusOK {
		t.Fatalf("custom bearer profile rejected: %d %s", changed.Code, changed.Body.String())
	}
	if _, err := s.verifyJWTForRequest(req, token); err != nil {
		t.Fatalf("custom profile did not relax issuer claim: %v", err)
	}
	tamperedSuffix := "x"
	if token[len(token)-1] == 'x' {
		tamperedSuffix = "y"
	}
	tampered := token[:len(token)-1] + tamperedSuffix
	if _, err := s.verifyJWTForRequest(req, tampered); err == nil {
		t.Fatal("custom profile disabled cryptographic signature verification")
	}
}
