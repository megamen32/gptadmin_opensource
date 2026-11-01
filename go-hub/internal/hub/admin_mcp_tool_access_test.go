package hub

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminMCPToolRejectsUnauthenticatedAndInvalidRequests(t *testing.T) {
	s := New(Config{CtlToken: "fixture", ConfigDir: t.TempDir()})
	defer s.Close()
	for _, tc := range []struct {
		name, token, body string
		want              int
	}{
		{"unauthenticated", "", "{\"method\":\"tools/list\",\"target\":\"hub\"}", http.StatusUnauthorized},
		{"unknown target", "fixture", "{\"method\":\"tools/list\",\"target\":\"shell:unknown\"}", http.StatusNotFound},
		{"no implicit target", "fixture", "{\"method\":\"tools/list\",\"target\":\"default\"}", http.StatusBadRequest},
		{"no tasks mutation", "fixture", "{\"method\":\"tasks/cancel\",\"target\":\"hub\"}", http.StatusBadRequest},
		{"invalid name", "fixture", "{\"method\":\"tools/call\",\"target\":\"hub\"}", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/tool", bytes.NewBufferString(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			recorder := httptest.NewRecorder()
			s.Handler().ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}
