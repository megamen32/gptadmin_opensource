package hub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuorumLocalChildSchemaOnlyExactApprovedLocalOwner(t *testing.T) {
	s, child, _ := directSchemaFixture(t, func(http.ResponseWriter, *http.Request) {})
	s.mu.Lock()
	if s.localExecutors == nil {
		s.localExecutors = make(map[string]map[string]string)
	}
	s.localExecutors["shell:peer"] = map[string]string{"server_id": "fixture"}
	s.mu.Unlock()
	body := `{"target":"` + child.AgentID + `","detail":"full"}`
	for _, tc := range []struct {
		name, path, method, body, hop, bearer string
		want                                  bool
	}{
		{"local schema", "/mcp-relay/tools", "POST", body, "1", "Bearer fixture", true},
		{"no auth", "/mcp-relay/tools", "POST", body, "1", "", false},
		{"no hop", "/mcp-relay/tools", "POST", body, "", "Bearer fixture", false},
		{"other owner", "/mcp-relay/tools", "POST", `{"target":"mcp:shell:other:sample"}`, "1", "Bearer fixture", false},
		{"mutation", "/mcp-relay/call", "POST", body, "1", "Bearer fixture", false},
		{"GET", "/mcp-relay/tools", "GET", body, "1", "Bearer fixture", false},
		{"ambiguous", "/mcp-relay/tools", "POST", `{"target":"` + child.AgentID + `","server_id":"shell:other"}`, "1", "Bearer fixture", false},
		{"background", "/mcp-relay/tools", "POST", `{"target":"` + child.AgentID + `","background":true}`, "1", "Bearer fixture", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set(nodePeerHopHeader, tc.hop)
			r.Header.Set("Authorization", tc.bearer)
			if got := s.quorumLocalChildSchemaRequest(r); got != tc.want {
				t.Fatalf("admitted=%v want %v", got, tc.want)
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil || string(raw) != tc.body {
				t.Fatalf("body changed: %v", err)
			}
		})
	}
	s.mu.Lock()
	delete(s.localExecutors, "shell:peer")
	s.mu.Unlock()
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/tools", strings.NewReader(body))
	r.Header.Set(nodePeerHopHeader, "1")
	r.Header.Set("Authorization", "Bearer fixture")
	if s.quorumLocalChildSchemaRequest(r) {
		t.Fatal("nonlocal child admitted")
	}
}
