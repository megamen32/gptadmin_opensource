package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The CloudOS desktop API carries shell execution, file read/write and
// process control; the whole /api/v1/cloud-os/ surface must reject
// unauthenticated callers. Regression for the public-origin exposure
// found on 2026-09-10.

func cloudOSAuthTestHub(t *testing.T) *Server {
	t.Helper()
	s := New(Config{CtlToken: "owner-control-token", AdminPassword: "admin-pass"})
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCloudOSAuthGateRejectsAnonymousEverywhere(t *testing.T) {
	s := cloudOSAuthTestHub(t)
	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/cloud-os/shell/computers"},
		{http.MethodPost, "/api/v1/cloud-os/shell/exec"},
		{http.MethodPost, "/api/v1/cloud-os/files/read"},
		{http.MethodPost, "/api/v1/cloud-os/files/write"},
		{http.MethodPost, "/api/v1/cloud-os/processes/exec"},
		{http.MethodPost, "/api/v1/cloud-os/processes/kill"},
		{http.MethodPost, "/api/v1/cloud-os/network/connect"},
	}
	for _, probe := range paths {
		r := httptest.NewRequest(probe.method, probe.path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s anonymous = %d, want 401", probe.method, probe.path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "admin_login") {
			t.Fatalf("%s %s 401 body must point to the admin login", probe.method, probe.path)
		}
	}
}

func TestCloudOSAuthGateAdmitsBearerAndAdminSession(t *testing.T) {
	s := cloudOSAuthTestHub(t)
	handler := s.Handler()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/cloud-os/shell/computers", nil)
	r.Header.Set("Authorization", "Bearer owner-control-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("owner bearer rejected: %d", w.Code)
	}

	// The admin session cookie comes from /admin/login; only the cookie
	// issued for the configured password admits the desktop.
	r = httptest.NewRequest(http.MethodGet, "/api/v1/cloud-os/shell/computers", nil)
	r.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: "forged-session"})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("forged session accepted: %d", w.Code)
	}
}

func TestCloudOSAuthGateLeavesOtherSurfacesAlone(t *testing.T) {
	s := cloudOSAuthTestHub(t)
	handler := s.Handler()
	for _, path := range []string{"/healthz", "/version", "/connect"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == http.StatusUnauthorized {
			t.Fatalf("%s must stay public, got 401", path)
		}
	}
}
