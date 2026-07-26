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
