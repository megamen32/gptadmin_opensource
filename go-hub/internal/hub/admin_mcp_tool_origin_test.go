package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminMCPToolRejectsCrossOriginAdminSession(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := New(Config{CtlToken: "fixture", AdminPassword: "fixture-password", ConfigDir: t.TempDir(), Now: func() time.Time { return now }})
	defer s.Close()
	request := httptest.NewRequest(http.MethodPost, "https://hub.example/admin/api/mcp/tool", strings.NewReader(`{"target":"hub","method":"tools/list"}`))
	request.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: s.signAdminSession(now.Add(time.Hour))})
	request.Header.Set("Origin", "https://other.example")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin cookie accepted: %d %s", rec.Code, rec.Body.String())
	}
}
