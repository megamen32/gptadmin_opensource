package hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCloudOSUIServesInstalledDist(t *testing.T) {
	dir := t.TempDir()
	dist := filepath.Join(dir, "cloudos-ui")
	if err := os.MkdirAll(filepath.Join(dist, "app-icons", "finder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html><title>CloudOS</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "app-icons", "finder", "32.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := New(Config{PublicDir: dir})

	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/cloudos/", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("index status = %d, want 200", res.Code)
	}
	if body := res.Body.String(); body != "<!doctype html><title>CloudOS</title>" {
		t.Fatalf("unexpected index body: %q", body)
	}
	// The entry document must revalidate so rebuilt hashed assets are picked up.
	if cc := res.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("index cache-control = %q, want no-cache", cc)
	}

	res = httptest.NewRecorder()
	server.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/cloudos/app-icons/finder/32.png", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", res.Code)
	}

	// Path traversal must never escape the installed dist.
	res = httptest.NewRecorder()
	server.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/cloudos/..%2f..%2fetc%2fpasswd", nil))
	if res.Code == http.StatusOK {
		t.Fatalf("traversal returned 200")
	}

	// A missing installation fails closed instead of proxying anywhere.
	empty := New(Config{PublicDir: t.TempDir()})
	res = httptest.NewRecorder()
	empty.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/cloudos/", nil))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing dist status = %d, want 503", res.Code)
	}
}

func TestHubRootRedirectsToAdmin(t *testing.T) {
	dir := t.TempDir()
	dist := filepath.Join(dir, "cloudos-ui")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := New(Config{PublicDir: dir})

	// The hub root is the lightweight admin console. CloudOS is an optional
	// payload and must not be required for the primary entry point.
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusTemporaryRedirect || res.Header().Get("Location") != "/admin/" {
		t.Fatalf("root status = %d location = %q, want 307 /admin/", res.Code, res.Header().Get("Location"))
	}

	// Unknown paths keep returning 404 instead of joining the redirect.
	res = httptest.NewRecorder()
	server.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/no-such-path", nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", res.Code)
	}
}

// The CloudOS desktop exposes the user's files through its UI shell; the
// entry documents must require the admin password session the same way the
// /api/v1/cloud-os/ API does (regression for the anonymous desktop found
// on 2026-09-11).
func TestCloudOSUIRequiresAdminSession(t *testing.T) {
	dir := t.TempDir()
	dist := filepath.Join(dir, "cloudos-ui")
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html><title>CloudOS</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("js"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(Config{PublicDir: dir, AdminPassword: "admin-pass"})
	t.Cleanup(func() { s.Close() })
	handler := s.Handler()

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/cloudos/", nil))
	if res.Code != http.StatusFound {
		t.Fatalf("anonymous /cloudos/ status = %d, want 302", res.Code)
	}
	if loc := res.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/login?") || !strings.Contains(loc, "next=") {
		t.Fatalf("anonymous /cloudos/ location = %q, want /admin/login?next=…", loc)
	}
	if strings.Contains(res.Body.String(), "CloudOS</title>") {
		t.Fatalf("anonymous /cloudos/ leaked the desktop index")
	}

	req := httptest.NewRequest(http.MethodGet, "/cloudos/", nil)
	req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: "forged-session"})
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusFound {
		t.Fatalf("forged session status = %d, want 302", res.Code)
	}

	session := adminSessionCookieName + "=" + s.signAdminSession(time.Now().Add(time.Hour))
	req = httptest.NewRequest(http.MethodGet, "/cloudos/", nil)
	req.Header.Set("Cookie", session)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("authed /cloudos/ status = %d, want 200", res.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/cloudos/assets/app.js", nil)
	req.Header.Set("Cookie", session)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("authed asset status = %d, want 200", res.Code)
	}

	// After the password check the login must return to the desktop.
	form := url.Values{"password": {"admin-pass"}, "next": {"/cloudos/"}}
	req = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusFound || res.Header().Get("Location") != "/cloudos/" {
		t.Fatalf("login with next=/cloudos/ -> %d %q, want 302 /cloudos/", res.Code, res.Header().Get("Location"))
	}
}
