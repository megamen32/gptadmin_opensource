package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyOAuthResourceMetadataMatchesConnection(t *testing.T) {
	const canonical = "https://mcp.example"
	const legacy = "https://u-old.example"
	s := New(Config{PublicOrigin: canonical, MCPResource: canonical, LegacyPublicOrigins: []string{legacy}, OAuthClientSecret: "test-secret"})
	defer s.Close()
	for _, tc := range []struct{ name, incoming, forwarded, want string }{
		{"canonical", canonical, "", canonical},
		{"legacy", legacy, "", legacy},
		{"proxy legacy", "http://internal.invalid", "u-old.example", legacy},
		{"unlisted host", "https://attacker.example", "", canonical},
		{"unlisted forwarded host", canonical, "attacker.example", canonical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := func(path string) *http.Request {
				r := httptest.NewRequest(http.MethodGet, tc.incoming+path, nil)
				if tc.forwarded != "" {
					r.Header.Set("X-Forwarded-Host", tc.forwarded)
					r.Header.Set("X-Forwarded-Proto", "https")
				}
				return r
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, request("/.well-known/oauth-protected-resource"))
			var metadata struct {
				Resource string   `json:"resource"`
				Servers  []string `json:"authorization_servers"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Resource != tc.want {
				t.Errorf("resource=%q want %q", metadata.Resource, tc.want)
			}
			if len(metadata.Servers) != 1 || metadata.Servers[0] != canonical {
				t.Errorf("issuer changed: %v", metadata.Servers)
			}
			w = httptest.NewRecorder()
			s.Handler().ServeHTTP(w, request("/mcp"))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous MCP status=%d", w.Code)
			}
			want := `resource_metadata="` + tc.want + `/.well-known/oauth-protected-resource"`
			if !strings.Contains(w.Header().Get("WWW-Authenticate"), want) {
				t.Errorf("challenge=%q want %q", w.Header().Get("WWW-Authenticate"), want)
			}
			if s.allowedResource("https://attacker.example", request("/mcp")) {
				t.Fatal("unlisted resource admitted")
			}
		})
	}
}
