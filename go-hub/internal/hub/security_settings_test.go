package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityPresetAndTOTPFailClosedUntilEnrollment(t *testing.T) {
	configDir := t.TempDir()
	s := New(Config{ConfigDir: configDir, CtlToken: "ctl", AdminPassword: "pw", OAuthClientSecret: "oauth-secret", PublicOrigin: "https://hub.example", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	var cookie string

	request := func(server *Server, method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		return w
	}

	initial := request(s, http.MethodGet, "/admin/api/security/preset", "ctl", "")
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"preset":"working_default"`) {
		t.Fatalf("initial preset status=%d body=%s", initial.Code, initial.Body.String())
	}
	if strings.Contains(initial.Body.String(), "secret") {
		t.Fatalf("preset endpoint exposed secret material: %s", initial.Body.String())
	}
	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("password=pw"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusFound || len(loginResponse.Result().Cookies()) == 0 {
		t.Fatalf("admin login status=%d cookies=%v body=%s", loginResponse.Code, loginResponse.Result().Cookies(), loginResponse.Body.String())
	}
	adminCookie := loginResponse.Result().Cookies()[0]
	cookie = adminCookie.Name + "=" + adminCookie.Value
	reauth := request(s, http.MethodPost, "/admin/api/security/reauth", "ctl", `{"password":"pw"}`)
	if reauth.Code != http.StatusOK || len(reauth.Result().Cookies()) == 0 {
		t.Fatalf("admin reauth status=%d body=%s", reauth.Code, reauth.Body.String())
	}
	reauthCookie := reauth.Result().Cookies()[0]
	cookie += "; " + reauthCookie.Name + "=" + reauthCookie.Value

	private := request(s, http.MethodPut, "/admin/api/security/preset", "ctl", `{"preset":"private_access"}`)
	if private.Code != http.StatusOK || !strings.Contains(private.Body.String(), `"preset":"private_access"`) {
		t.Fatalf("private preset status=%d body=%s", private.Code, private.Body.String())
	}
	locked := request(s, http.MethodPut, "/admin/api/security/preset", "ctl", `{"preset":"locked_down"}`)
	if locked.Code != http.StatusPreconditionFailed || !strings.Contains(locked.Body.String(), "MFA") {
		t.Fatalf("locked preset bypassed MFA gate: status=%d body=%s", locked.Code, locked.Body.String())
	}

	enroll := request(s, http.MethodPost, "/admin/api/security/mfa/totp/enroll", "ctl", "{}")
	if enroll.Code != http.StatusOK {
		t.Fatalf("TOTP enrollment status=%d body=%s", enroll.Code, enroll.Body.String())
	}
	var enrollment struct {
		Secret        string   `json:"secret"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(enroll.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	if enrollment.Secret == "" || len(enrollment.RecoveryCodes) != 8 || !strings.Contains(enroll.Body.String(), "otpauth://") {
		t.Fatalf("enrollment response missing one-time setup data: %s", enroll.Body.String())
	}
	verify := request(s, http.MethodPost, "/admin/api/security/mfa/totp/verify", "ctl", `{"code":"000000"}`)
	if verify.Code != http.StatusUnauthorized {
		t.Fatalf("invalid TOTP code status=%d body=%s", verify.Code, verify.Body.String())
	}
	verify = request(s, http.MethodPost, "/admin/api/security/mfa/totp/verify", "ctl", `{"code":"`+totpCode(enrollment.Secret, s.now())+`"}`)
	if verify.Code != http.StatusOK || !strings.Contains(verify.Body.String(), `"mfa_enrolled":true`) {
		t.Fatalf("valid TOTP code status=%d body=%s", verify.Code, verify.Body.String())
	}
	recoveryVerify := request(s, http.MethodPost, "/admin/api/security/mfa/totp/verify", "ctl", `{"code":"`+enrollment.RecoveryCodes[0]+`"}`)
	if recoveryVerify.Code != http.StatusOK {
		t.Fatalf("recovery code verification status=%d body=%s", recoveryVerify.Code, recoveryVerify.Body.String())
	}
	if reused := request(s, http.MethodPost, "/admin/api/security/mfa/totp/verify", "ctl", `{"code":"`+enrollment.RecoveryCodes[0]+`"}`); reused.Code != http.StatusUnauthorized {
		t.Fatalf("recovery code was reusable: status=%d body=%s", reused.Code, reused.Body.String())
	}
	if repeated := request(s, http.MethodPost, "/admin/api/security/mfa/totp/enroll", "ctl", "{}"); repeated.Code != http.StatusConflict {
		t.Fatalf("TOTP enrollment was reusable: status=%d body=%s", repeated.Code, repeated.Body.String())
	}
	info, err := os.Stat(filepath.Join(configDir, securityStateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("security state mode=%o, want 0600", info.Mode().Perm())
	}
	stateBytes, err := os.ReadFile(filepath.Join(configDir, securityStateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateBytes), enrollment.Secret) || !strings.Contains(string(stateBytes), "totp_secret_ciphertext") {
		t.Fatalf("security state did not encrypt TOTP secret: %s", stateBytes)
	}
	locked = request(s, http.MethodPut, "/admin/api/security/preset", "ctl", `{"preset":"locked_down"}`)
	if locked.Code != http.StatusOK {
		t.Fatalf("locked preset with MFA status=%d body=%s", locked.Code, locked.Body.String())
	}

	restarted := New(Config{ConfigDir: configDir, CtlToken: "ctl", AdminPassword: "pw", OAuthClientSecret: "oauth-secret", PublicOrigin: "https://hub.example", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	state := request(restarted, http.MethodGet, "/admin/api/security/preset", "ctl", "")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), `"preset":"locked_down"`) || !strings.Contains(state.Body.String(), `"mfa_enrolled":true`) {
		t.Fatalf("security state did not survive restart: status=%d body=%s", state.Code, state.Body.String())
	}

	login = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("password=pw"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse = httptest.NewRecorder()
	restarted.Handler().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || loginResponse.Result().Cookies() != nil && len(loginResponse.Result().Cookies()) != 0 {
		t.Fatalf("locked login without MFA was accepted: status=%d cookies=%v body=%s", loginResponse.Code, loginResponse.Result().Cookies(), loginResponse.Body.String())
	}

	login = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("password=pw&mfa_code="+enrollment.RecoveryCodes[1]))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse = httptest.NewRecorder()
	restarted.Handler().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusFound || len(loginResponse.Result().Cookies()) == 0 {
		t.Fatalf("locked login with MFA failed: status=%d cookies=%v body=%s", loginResponse.Code, loginResponse.Result().Cookies(), loginResponse.Body.String())
	}
}

func TestSensitiveSecurityMutationRequiresFreshAdminReauth(t *testing.T) {
	s := New(Config{CtlToken: "ctl", AdminPassword: "pw", OAuthClientSecret: "oauth-secret", PublicOrigin: "https://hub.example"})
	request := func(method, path, token, cookie, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		return response
	}
	if denied := request(http.MethodPut, "/admin/api/security/preset", "ctl", "", `{"preset":"private_access"}`); denied.Code != http.StatusPreconditionRequired {
		t.Fatalf("security mutation without reauth status=%d body=%s", denied.Code, denied.Body.String())
	}

	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("password=pw"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(loginResponse, login)
	cookies := loginResponse.Result().Cookies()
	if loginResponse.Code != http.StatusFound || len(cookies) == 0 {
		t.Fatalf("admin login status=%d cookies=%v body=%s", loginResponse.Code, cookies, loginResponse.Body.String())
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	reauth := request(http.MethodPost, "/admin/api/security/reauth", "", cookie, `{"password":"pw"}`)
	if reauth.Code != http.StatusOK || strings.Contains(reauth.Body.String(), "pw") {
		t.Fatalf("password reauth status=%d body=%s", reauth.Code, reauth.Body.String())
	}
	if reauthCookies := reauth.Result().Cookies(); len(reauthCookies) == 0 {
		t.Fatalf("password reauth did not set a reauth cookie: %s", reauth.Body.String())
	} else {
		cookie += "; " + reauthCookies[0].Name + "=" + reauthCookies[0].Value
	}
	if changed := request(http.MethodPut, "/admin/api/security/preset", "", cookie, `{"preset":"private_access"}`); changed.Code != http.StatusOK {
		t.Fatalf("mutation after password reauth status=%d body=%s", changed.Code, changed.Body.String())
	}

	enroll := request(http.MethodPost, "/admin/api/security/mfa/totp/enroll", "", cookie, `{}`)
	if enroll.Code != http.StatusOK {
		t.Fatalf("TOTP enrollment status=%d body=%s", enroll.Code, enroll.Body.String())
	}
	var enrollment struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(enroll.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	code := totpCode(enrollment.Secret, s.now())
	if verified := request(http.MethodPost, "/admin/api/security/mfa/totp/verify", "", cookie, `{"code":"`+code+`"}`); verified.Code != http.StatusOK {
		t.Fatalf("TOTP verification status=%d body=%s", verified.Code, verified.Body.String())
	}
	if missingMFA := request(http.MethodPost, "/admin/api/security/reauth", "", cookie, `{"password":"pw"}`); missingMFA.Code != http.StatusUnauthorized {
		t.Fatalf("reauth without fresh MFA status=%d body=%s", missingMFA.Code, missingMFA.Body.String())
	}
	fresh := request(http.MethodPost, "/admin/api/security/reauth", "", cookie, `{"password":"pw","code":"`+code+`"}`)
	if fresh.Code != http.StatusOK {
		t.Fatalf("reauth with fresh MFA status=%d body=%s", fresh.Code, fresh.Body.String())
	}
	if freshCookies := fresh.Result().Cookies(); len(freshCookies) > 0 {
		cookie += "; " + freshCookies[0].Name + "=" + freshCookies[0].Value
	}
	if locked := request(http.MethodPut, "/admin/api/security/preset", "", cookie, `{"preset":"locked_down"}`); locked.Code != http.StatusOK {
		t.Fatalf("locked preset after fresh MFA status=%d body=%s", locked.Code, locked.Body.String())
	}
}
