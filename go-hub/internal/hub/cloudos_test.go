package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudOSListsServer100(t *testing.T) {
	server := New(Config{CtlToken: "owner-control-token"})
	// The desktop API is never public: unauthenticated calls are rejected.
	anon := httptest.NewRequest(http.MethodGet, "/api/v1/cloud-os/computers", nil)
	anonRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(anonRes, anon)
	if anonRes.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want %d", anonRes.Code, http.StatusUnauthorized)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cloud-os/computers", nil)
	req.Header.Set("Authorization", "Bearer owner-control-token")
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}

	var body struct {
		Computers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"computers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Computers) != 1 || body.Computers[0].ID != "server-100" || body.Computers[0].Name != "server-100" || body.Computers[0].Status != "online" {
		t.Fatalf("unexpected computers: %#v", body.Computers)
	}
}
