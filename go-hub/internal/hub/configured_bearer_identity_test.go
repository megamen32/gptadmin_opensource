package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Fast units: reproduce configured/durable shadowing without real credentials.
// Expected <5s; maximum 30s. Live identity acceptance is recorded separately.
func configuredDurableFixture(t *testing.T) (*Server, string, managedMCPToken) {
	t.Helper()
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "fixture-owner", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example", OAuthClientSecret: "fixture-signing"})
	t.Cleanup(func() { s.Close() })
	token, record, err := s.issueManagedMCPToken("zcode", 7, s.cfg.PublicOrigin, s.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.ExistingMCPBearers = map[string]string{"GPTADMIN_ZCODE_MCP_BEARER": token}
	if err := s.reconcileExistingMCPBearers(); err != nil {
		t.Fatal(err)
	}
	return s, token, record
}

func configuredBearerRequest(s *Server, token string) (*http.Request, *httptest.ResponseRecorder, bool) {
	r := httptest.NewRequest(http.MethodPost, "https://hub.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	return r, w, s.mcpAuth(w, r)
}

func TestConfiguredBearerPreservesDurableIdentity(t *testing.T) {
	s, token, original := configuredDurableFixture(t)
	r, _, admitted := configuredBearerRequest(s, token)
	if !admitted {
		t.Fatal("original durable caller rejected")
	}
	identity, status := s.callAccessClientTool(r, "access_clients", map[string]any{"action": "whoami"})
	if status != 200 || identity["client_id"] != "zcode" || identity["token_id"] != original.ID || identity["access_mode"] != "full" || identity["admin"] != false {
		t.Fatalf("migration alias replaced durable identity: %#v", identity)
	}
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	if claims["scope"] != original.Scope || AccessProfileIDFromRequest(r) != "" {
		t.Fatal("original scope/profile changed")
	}
	if len(s.managedMCP) != 2 {
		t.Fatal("existing migration evidence was removed")
	}
}

func TestConfiguredBearerCannotResurrectDurableLifecycle(t *testing.T) {
	for _, cause := range []string{"revoked", "expired"} {
		t.Run(cause, func(t *testing.T) {
			s, token, record := configuredDurableFixture(t)
			if cause == "revoked" {
				record.RevokedAt = time.Now().Unix()
			} else {
				record.ExpiresAt = time.Now().Unix() - 1
			}
			s.managedMCP[record.ID] = record
			if err := s.saveManagedMCPStateLocked(); err != nil {
				t.Fatal(err)
			}
			_, w, admitted := configuredBearerRequest(s, token)
			if admitted || w.Code != http.StatusUnauthorized {
				t.Fatal("configured alias bypassed original durable lifecycle")
			}
		})
	}
}

func TestConfiguredBearerPreservesDurableProfile(t *testing.T) {
	s, token, record := configuredDurableFixture(t)
	_, status := accessToolTest(t, s, "fixture-owner", "access_profiles", map[string]any{"action": "create", "id": "original-reader", "profile": map[string]any{"access_mode": "readonly", "approval_mode": "read_only", "allowed_targets": []string{"hub"}, "allowed_tools": []string{"*"}}})
	if status != 200 {
		t.Fatal("fixture profile creation failed")
	}
	_, status = accessToolTest(t, s, "fixture-owner", "access_clients", map[string]any{"action": "bind", "id": record.ID, "profile_id": "original-reader"})
	if status != 200 {
		t.Fatal("fixture profile binding failed")
	}
	r, _, admitted := configuredBearerRequest(s, token)
	if !admitted || AccessProfileIDFromRequest(r) != "original-reader" || requestAccessMode(r) != "readonly" || s.requestHasAdminRole(r) {
		t.Fatal("configured alias bypassed original effective policy")
	}
}

func TestConfiguredOnlyBearerStillAdmitted(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), PublicOrigin: "https://hub.example", ExistingMCPBearers: map[string]string{"GPTADMIN_LEGACY_MCP_BEARER": "fixture-opaque-only"}})
	t.Cleanup(func() { s.Close() })
	r, _, admitted := configuredBearerRequest(s, "fixture-opaque-only")
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	if !admitted || claims["client_id"] != "GPTADMIN_LEGACY_MCP_BEARER" {
		t.Fatal("unrelated configured-only migration lane changed")
	}
}
