package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
)

func TestWebAuthnRegistrationBeginReturnsPublicKeyChallenge(t *testing.T) {
	s := New(Config{
		ConfigDir:         t.TempDir(),
		CtlToken:          "ctl",
		AdminPassword:     "pw",
		PublicOrigin:      "https://hub.example",
		MCPResource:       "https://hub.example",
		OAuthClientSecret: "oauth-secret",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/security/mfa/webauthn/register/begin", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("registration begin status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	publicKey, ok := payload["publicKey"].(map[string]any)
	if !ok {
		t.Fatalf("registration begin missing publicKey options: %s", rec.Body.String())
	}
	if challenge, _ := publicKey["challenge"].(string); challenge == "" {
		t.Fatalf("registration begin missing challenge: %s", rec.Body.String())
	}
	rp, ok := publicKey["rp"].(map[string]any)
	if !ok || rp["id"] != "hub.example" {
		t.Fatalf("registration begin has unexpected rp: %v", publicKey["rp"])
	}
	user, ok := publicKey["user"].(map[string]any)
	if !ok || user["name"] != "admin" {
		t.Fatalf("registration begin has unexpected user: %v", publicKey["user"])
	}
	if strings.Contains(rec.Body.String(), "privateKey") || strings.Contains(rec.Body.String(), "AdminPassword") {
		t.Fatalf("registration begin exposed sensitive material: %s", rec.Body.String())
	}
}

func TestLockedDownAcceptsEnrolledWebAuthnCredential(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", PublicOrigin: "https://hub.example", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	s.mu.Lock()
	s.webauthnState = webAuthnState{Credentials: []webauthnlib.Credential{{ID: []byte{1}, PublicKey: []byte{2}}}, EnrolledAt: s.now()}
	s.mu.Unlock()

	get := httptest.NewRequest(http.MethodGet, "/admin/api/security/preset", nil)
	get.Header.Set("Authorization", "Bearer ctl")
	getResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `"mfa_method":"webauthn"`) {
		t.Fatalf("WebAuthn MFA snapshot status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}

	put := httptest.NewRequest(http.MethodPut, "/admin/api/security/preset", strings.NewReader(`{"preset":"locked_down"}`))
	put.Header.Set("Authorization", "Bearer ctl")
	put.Header.Set("Content-Type", "application/json")
	putResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(putResponse, put)
	if putResponse.Code != http.StatusOK || !strings.Contains(putResponse.Body.String(), `"preset":"locked_down"`) {
		t.Fatalf("locked-down WebAuthn preset status=%d body=%s", putResponse.Code, putResponse.Body.String())
	}
}

func TestWebAuthnLoginBeginFailsClosedWithoutCredential(t *testing.T) {
	s := New(Config{PublicOrigin: "https://hub.example", CtlToken: "ctl"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/security/mfa/webauthn/login/begin", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "no WebAuthn credential") {
		t.Fatalf("unenrolled WebAuthn login status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebAuthnRegistrationFinishAcceptsZeroExpiryCeremony(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", PublicOrigin: "https://hub.example"})
	begin := httptest.NewRequest(http.MethodPost, "/admin/api/security/mfa/webauthn/register/begin", strings.NewReader("{}"))
	begin.Header.Set("Authorization", "Bearer ctl")
	begin.Header.Set("Content-Type", "application/json")
	beginResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(beginResponse, begin)
	if beginResponse.Code != http.StatusOK {
		t.Fatalf("registration begin status=%d body=%s", beginResponse.Code, beginResponse.Body.String())
	}
	cookies := beginResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != webAuthnSessionCookie {
		t.Fatalf("registration begin session cookie=%v", cookies)
	}

	finish := httptest.NewRequest(http.MethodPost, "/admin/api/security/mfa/webauthn/register/finish", strings.NewReader("{}"))
	finish.Header.Set("Authorization", "Bearer ctl")
	finish.Header.Set("Content-Type", "application/json")
	finish.AddCookie(cookies[0])
	finishResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(finishResponse, finish)
	if finishResponse.Code == http.StatusBadRequest && strings.Contains(finishResponse.Body.String(), "missing or expired") {
		t.Fatalf("live registration ceremony was rejected before credential validation: %s", finishResponse.Body.String())
	}
}

func TestLockedDownAdminLoginRequiresWebAuthnProofCookie(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", AdminPassword: "pw", PublicOrigin: "https://hub.example", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	s.mu.Lock()
	s.webauthnState = webAuthnState{Credentials: []webauthnlib.Credential{{ID: []byte{1}, PublicKey: []byte{2}}}, EnrolledAt: s.now()}
	s.security.Preset = securityPresetLockedDown
	s.security.MFAEnrolledAt = s.now()
	s.mu.Unlock()

	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("password=pw"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	withoutProof := httptest.NewRecorder()
	s.Handler().ServeHTTP(withoutProof, login)
	if withoutProof.Code != http.StatusOK || len(withoutProof.Result().Cookies()) != 0 {
		t.Fatalf("locked login without WebAuthn proof status=%d cookies=%v", withoutProof.Code, withoutProof.Result().Cookies())
	}

	login = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("password=pw"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.AddCookie(&http.Cookie{Name: webAuthnMFACookie, Value: s.signAdminProof("webauthn-mfa", time.Now().Add(time.Minute))})
	withProof := httptest.NewRecorder()
	s.Handler().ServeHTTP(withProof, login)
	if withProof.Code != http.StatusFound || len(withProof.Result().Cookies()) == 0 {
		t.Fatalf("locked login with WebAuthn proof status=%d cookies=%v body=%s", withProof.Code, withProof.Result().Cookies(), withProof.Body.String())
	}
}

func TestAdminLoginRendersWebAuthnBrowserFlow(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), AdminPassword: "pw", PublicOrigin: "https://hub.example", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	s.mu.Lock()
	s.webauthnState = webAuthnState{Credentials: []webauthnlib.Credential{{ID: []byte{1}, PublicKey: []byte{2}}}, EnrolledAt: s.now()}
	s.security.Preset = securityPresetLockedDown
	s.security.MFAEnrolledAt = s.now()
	s.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{
		"webauthn-login",
		"/admin/api/security/mfa/webauthn/login/begin",
		"/admin/api/security/mfa/webauthn/login/finish",
		"navigator.credentials.get",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("admin login missing WebAuthn browser marker %q: %s", marker, body)
		}
	}
	if strings.Contains(body, "privateKey") {
		t.Fatalf("admin login exposed private credential material: %s", body)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; connect-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'" {
		t.Fatalf("admin login CSP=%q", got)
	}
}

func TestWebAuthnLoginBrowserRequiresSameOrigin(t *testing.T) {
	s := New(Config{PublicOrigin: "https://hub.example", CtlToken: "ctl"})
	for _, test := range []struct {
		name       string
		origin     string
		wantStatus int
	}{
		{name: "same origin", origin: "https://hub.example", wantStatus: http.StatusPreconditionFailed},
		{name: "cross origin", origin: "https://attacker.example", wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/api/security/mfa/webauthn/login/begin", strings.NewReader("{}"))
			req.Header.Set("Origin", test.origin)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("browser origin %q status=%d body=%s", test.origin, rec.Code, rec.Body.String())
			}
		})
	}
}
