package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestExplicitOAuthRedirectAllowlistIsExactAndValidated(t *testing.T) {
	good := "https://legacy.example/oauth/callback?tenant=one"
	redirects := parseExplicitOAuthRedirects(`["https://legacy.example/oauth/callback?tenant=one"]`)
	s := New(Config{OAuthAllowedRedirects: redirects, PublicOrigin: "https://hub.example", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	if !s.allowedRedirect(good) {
		t.Fatal("explicit registered redirect rejected")
	}
	for _, bad := range []string{"https://legacy.example/oauth/callback?tenant=two", "https://legacy.example/oauth/callback/extra", "https://evil.example/oauth/callback?tenant=one", "http://legacy.example/oauth/callback?tenant=one"} {
		if s.allowedRedirect(bad) {
			t.Fatalf("non-matching redirect accepted: %s", bad)
		}
	}
	for _, raw := range []string{`["http://evil.example/cb"]`, `["https://user:pass@legacy.example/cb"]`, `["https://legacy.example/cb#fragment"]`, `["https://legacy.example/cb#"]`, `["https://legacy.example:99999/cb"]`, `["https://legacy.example/cb",`, `"https://legacy.example/cb"`} {
		if len(parseExplicitOAuthRedirects(raw)) != 0 {
			t.Fatal("invalid configuration must fail closed")
		}
	}
}

func TestExplicitOAuthRedirectKeepsClientBinding(t *testing.T) {
	good := "https://legacy.example/callback?tenant=one"
	other := "https://legacy.example/callback?tenant=two"
	s := New(Config{OAuthAllowedRedirects: []string{good, other}, PublicOrigin: "https://hub.example", MCPResource: "https://hub.example", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	register := httptest.NewRequest(http.MethodPost, "/register", bytes.NewBufferString(`{"redirect_uris":["https://legacy.example/callback?tenant=one"]}`))
	register.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, register)
	if w.Code != http.StatusCreated {
		t.Fatalf("registration: %d", w.Code)
	}
	var client struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &client); err != nil || client.ClientID == "" {
		t.Fatal("registration did not produce client id")
	}
	for _, tc := range []struct {
		redirect string
		want     int
	}{{good, http.StatusOK}, {other, http.StatusBadRequest}} {
		q := url.Values{"response_type": {"code"}, "client_id": {client.ClientID}, "redirect_uri": {tc.redirect}, "resource": {"https://hub.example"}, "code_challenge": {"challenge"}, "code_challenge_method": {"S256"}}
		req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("authorize status %d, want %d", rec.Code, tc.want)
		}
	}
}

func TestExplicitOAuthRedirectsFromEnvironment(t *testing.T) {
	t.Setenv("GPTADMIN_OAUTH_ALLOWED_REDIRECTS", `["https://legacy.example/cb"]`)
	cfg := FromEnv()
	if len(cfg.OAuthAllowedRedirects) != 1 || cfg.OAuthAllowedRedirects[0] != "https://legacy.example/cb" {
		t.Fatal("exact callback was not loaded from the protected environment")
	}
}
