package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleLogRedactionAndParsing(t *testing.T) {
	raw := []byte("{\"MESSAGE\":\"Bearer top-secret token=abc password=foo api_key:bar https://example.test/?token=secret123\",\"PRIORITY\":\"4\",\"__REALTIME_TIMESTAMP\":\"1793450000000000\"}\n" +
		"{\"MESSAGE\":\"ordinary event\",\"PRIORITY\":\"6\",\"__REALTIME_TIMESTAMP\":\"1793450000000001\"}\n")
	entries := parseConsoleEntries(raw)
	if len(entries) != 2 || entries[0].Level != "warn" || entries[1].Level != "info" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	for _, secret := range []string{"top-secret", "abc", "foo", "bar", "secret123"} {
		if strings.Contains(entries[0].Message, secret) {
			t.Fatalf("console leaked %q: %s", secret, entries[0].Message)
		}
	}
	if entries[1].Message != "ordinary event" || entries[0].Time == "" {
		t.Fatalf("ordinary console fields lost: %+v", entries)
	}
}

func TestConsoleRequiresCtlAndRejectsInvalidRequests(t *testing.T) {
	s := New(Config{CtlToken: "private-test-token", ConfigDir: t.TempDir()})
	defer s.Close()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/api/console", nil))
	if w.Code < 400 {
		t.Fatalf("unprivileged console access succeeded: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.adminConsole(w, httptest.NewRequest(http.MethodGet, "/admin/api/console?limit=10000", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("limit should be checked before journal execution: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.adminConsole(w, httptest.NewRequest(http.MethodPost, "/admin/api/console", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("console must be read-only: %d", w.Code)
	}
}
