package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityPresetRejectsExternalHTTPOrigin(t *testing.T) {
	s := New(Config{CtlToken: "ctl", PublicOrigin: "http://hub.example"})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/security/preset", strings.NewReader(`{"preset":"private_access"}`))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("status=%d body=%s, want HTTPS precondition", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "https") {
		t.Fatalf("response does not explain HTTPS requirement: %s", w.Body.String())
	}
}

func TestValidatePublicOriginAllowsHTTPSAndLoopbackOnly(t *testing.T) {
	tests := []struct {
		origin  string
		wantErr bool
	}{
		{origin: "", wantErr: false},
		{origin: "https://hub.example", wantErr: false},
		{origin: "http://127.0.0.1:9001", wantErr: false},
		{origin: "http://localhost:9001", wantErr: false},
		{origin: "http://hub.example", wantErr: true},
		{origin: "ftp://hub.example", wantErr: true},
		{origin: "https://user:pass@hub.example", wantErr: true},
	}
	for _, test := range tests {
		if err := validatePublicOriginForPreset(test.origin); (err != nil) != test.wantErr {
			t.Errorf("origin %q error=%v, wantErr=%v", test.origin, err, test.wantErr)
		}
	}
}
