package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserExtensionOAuthUsesSameOriginCallbackWithoutCredentialCopy(t *testing.T) {
	s := New(Config{
		CtlToken:          "ctl",
		AdminPassword:     "pw",
		OAuthClientSecret: "oauth-secret",
		PublicOrigin:      "https://hub.example",
		MCPResource:       "https://hub.example",
		DefaultTimeout:    time.Second,
		PollMaxTimeout:    time.Second,
	})

	register := httptest.NewRequest(http.MethodPost, "/register", bytes.NewBufferString(`{"redirect_uris":["https://hub.example/connect/callback"]}`))
	register.Header.Set("Content-Type", "application/json")
	registerRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(registerRecord, register)
	if registerRecord.Code != http.StatusCreated {
		t.Fatalf("registration status=%d body=%s", registerRecord.Code, registerRecord.Body.String())
	}
	var client map[string]any
	if err := json.Unmarshal(registerRecord.Body.Bytes(), &client); err != nil {
		t.Fatal(err)
	}
	clientID, _ := client["client_id"].(string)
	if clientID == "" {
		t.Fatalf("registration omitted client_id: %v", client)
	}

	authorizeQuery := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {"https://hub.example/connect/callback"},
		"resource":              {"https://hub.example"},
		"scope":                 {"gptadmin.read"},
		"code_challenge":        {pkceChallenge("browser-verifier")},
		"code_challenge_method": {"S256"},
		"state":                 {"browser-state"},
	}
	authorize := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+authorizeQuery.Encode(), nil)
	authorizeRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(authorizeRecord, authorize)
	if authorizeRecord.Code != http.StatusOK || !strings.Contains(authorizeRecord.Body.String(), "/oauth/authorize") {
		t.Fatalf("same-origin callback was not accepted: status=%d body=%s", authorizeRecord.Code, authorizeRecord.Body.String())
	}

	callback := httptest.NewRequest(http.MethodGet, "/connect/callback?code=opaque-code&state=browser-state", nil)
	callbackRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(callbackRecord, callback)
	if callbackRecord.Code != http.StatusOK || !strings.Contains(callbackRecord.Body.String(), "postMessage") {
		t.Fatalf("browser callback did not render handoff page: status=%d body=%s", callbackRecord.Code, callbackRecord.Body.String())
	}
	if strings.Contains(callbackRecord.Body.String(), "access_token") {
		t.Fatalf("browser callback rendered a credential: %s", callbackRecord.Body.String())
	}
}

func TestCustomGPTActionsOAuthAuthorizeAllowsNoPKCE(t *testing.T) {
	s := New(Config{
		CtlToken:          "ctl",
		AdminPassword:     "pw",
		OAuthClientSecret: "oauth-secret",
		PublicOrigin:      "https://hub.example",
		MCPResource:       "https://hub.example",
		DefaultTimeout:    time.Second,
		PollMaxTimeout:    time.Second,
	})
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {"chatgpt-actions"},
		"redirect_uri":  {"https://chat.openai.com/aip/g-123/oauth/callback"},
		"resource":      {"https://hub.example"},
		"scope":         {"gptadmin.read"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Custom GPT authorize without PKCE status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNoPKCEIsRestrictedToCustomGPTActionsCallback(t *testing.T) {
	for _, redirectURI := range []string{
		"http://localhost:3000/callback",
		"https://chatgpt.com/connector/oauth/callback",
		"https://example.invalid/oauth/callback",
		"https://chat.openai.com/aip/g-/oauth/callback",
		"https://chat.openai.com/aip/g-123/not-oauth",
	} {
		if validPKCEParameters("", "", redirectURI) {
			t.Fatalf("no-PKCE must be rejected for non-Custom-GPT redirect %q", redirectURI)
		}
	}
	if !validPKCEParameters("challenge", "S256", "http://localhost:3000/callback") {
		t.Fatal("S256 PKCE must remain valid for non-Custom-GPT clients")
	}
}

func TestRelaxAuthChecksAllowsOAuthWithoutPKCEVerifier(t *testing.T) {
	s := New(Config{
		CtlToken:                 "ctl",
		AdminPassword:            "pw",
		OAuthClientSecret:        "oauth-secret",
		PublicOrigin:             "https://hub.example",
		MCPResource:              "https://hub.example",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
		RelaxAuthChecks:          true,
	})
	form := url.Values{
		"client_id":    {"chatgpt"},
		"redirect_uri": {"https://chatgpt.com/connector/oauth/cb"},
		"resource":     {"https://hub.example"},
		"scope":        {"gptadmin.read"},
		"password":     {"pw"},
	}
	authorize := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	authorize.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	authorizeRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(authorizeRecord, authorize)
	if authorizeRecord.Code != http.StatusFound {
		t.Fatalf("relaxed authorize status=%d body=%s", authorizeRecord.Code, authorizeRecord.Body.String())
	}
	code := authorizeRecord.Header().Get("Location")
	if !strings.Contains(code, "code=") {
		t.Fatalf("relaxed authorize did not return a code: %q", code)
	}
	parsed, err := url.Parse(code)
	if err != nil {
		t.Fatal(err)
	}
	tokenForm := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {parsed.Query().Get("code")},
		"client_id":    {"chatgpt"},
		"redirect_uri": {"https://chatgpt.com/connector/oauth/cb"},
		"resource":     {"https://hub.example"},
	}
	tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenForm.Encode()))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(tokenRecord, tokenReq)
	if tokenRecord.Code != http.StatusOK {
		t.Fatalf("relaxed token status=%d body=%s", tokenRecord.Code, tokenRecord.Body.String())
	}
}
