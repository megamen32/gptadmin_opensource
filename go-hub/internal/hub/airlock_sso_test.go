package hub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fast unit, expected<1s/max5s: accept a real delegated principal instead of
// a shared owner cookie; reject forged/expired/request-mismatched assertions.
func TestAirlockUIAdminAdmissionIsPrincipalBound(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "airlock.key")
	if err := os.WriteFile(keyFile, []byte("fixture-key-for-local-logic-testing-only"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GPTADMIN_AIRLOCK_UI_KEY_FILE", keyFile)
	s := New(Config{AdminPassword: "existing-admin-password"})
	defer s.Close()
	claims := map[string]any{"app_id": "178f37c3-283d-48b9-82ef-03775e772438", "user_id": "98daa037-a87d-49b1-ba07-cd9e6f451864", "email": "cyberteaborg@gmail.com", "role": "admin", "host": "gptadmin.airlock.bezrabotnyi.com", "method": "GET", "uri": "/admin/", "expires": time.Now().Add(30 * time.Second).Unix()}
	sign := func() string {
		raw, _ := json.Marshal(claims)
		payload := base64.RawURLEncoding.EncodeToString(raw)
		mac := hmac.New(sha256.New, []byte("fixture-key-for-local-logic-testing-only"))
		mac.Write([]byte(payload))
		return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	r := httptest.NewRequest("GET", "https://gptadmin.airlock.bezrabotnyi.com/admin/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: s.signAdminSession(time.Now().Add(time.Hour))})
	r.Header.Set("X-GPTAdmin-Airlock-Identity", sign())
	if !s.adminSessionValid(r) {
		t.Fatal("trusted native Airlock admin rejected")
	}
	if actor := s.actorForRequest(r); actor != "airlock:98daa037-a87d-49b1-ba07-cd9e6f451864" {
		t.Fatalf("signed Oleg overwritten by legacy owner cookie: %q", actor)
	}
	r.Header.Set("X-GPTAdmin-Airlock-Identity", "forgery")
	if s.adminSessionValid(r) {
		t.Fatal("forged assertion fell back to owner cookie")
	}
	claims["expires"] = time.Now().Add(-time.Minute).Unix()
	r.Header.Set("X-GPTAdmin-Airlock-Identity", sign())
	if s.adminSessionValid(r) {
		t.Fatal("expired delegation accepted")
	}
	claims["expires"] = time.Now().Add(30 * time.Second).Unix()
	claims["uri"] = "/admin/"
	for _, remote := range []string{"", "malformed", "localhost:1234", "192.0.2.1:1234"} {
		r.RemoteAddr = remote
		r.Header.Set("X-GPTAdmin-Airlock-Identity", sign())
		if s.adminSessionValid(r) {
			t.Fatalf("non-loopback private sender accepted: %q", remote)
		}
	}
	r.RemoteAddr = "127.0.0.1:1234"
	claims["uri"] = "/other"
	r.Header.Set("X-GPTAdmin-Airlock-Identity", sign())
	if s.adminSessionValid(r) {
		t.Fatal("delegation replayed onto a different route")
	}
}
