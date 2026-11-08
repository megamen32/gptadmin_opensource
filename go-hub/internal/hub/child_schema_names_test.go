package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChildSchemaNamesValidatedBeforeProfileFilter(t *testing.T) {
	cases := []struct {
		label   string
		name    any
		missing bool
		valid   bool
	}{
		{label: "missing", missing: true},
		{label: "non_string", name: 42},
		{label: "empty", name: ""},
		{label: "blank", name: " \t\n"},
		{label: "valid", name: "allowed", valid: true},
	}
	for _, path := range []string{"direct", "legacy"} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.label, func(t *testing.T) {
				tool := map[string]any{"inputSchema": map[string]any{"type": "object"}}
				if !tc.missing {
					tool["name"] = tc.name
				}
				upstream := map[string]any{
					"tools":           []map[string]any{tool},
					"schema_complete": true, "schema_completeness": "complete",
				}
				var s *Server
				var child Agent
				if path == "direct" {
					s, child, _ = directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
						writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": upstream})
					})
				} else {
					s, child = childSchemaFixture(t, t.TempDir())
				}
				r := requestWithAccessProfile(httptest.NewRequest(http.MethodPost, "/mcp", nil), AccessProfile{
					ID: "exact-schema-reader", AccessMode: "full",
					AllowedTargets: []string{child.AgentID}, AllowedTools: []string{"allowed"},
				})
				r.Header.Set("Authorization", "Bearer schema-test-token")
				var got map[string]any
				if path == "direct" {
					got = s.directChildSchemaForRequest(r, child)
				} else {
					id := s.enqueueRelay(child.AgentID, "tools/list", nil)
					queued, found := s.childSchemaJobForRequest(r, id)
					if !found || firstString(queued, "status") != "queued" {
						t.Fatalf("legacy fixture was not queued: %#v", queued)
					}
					upstream["ref"] = firstString(child.Meta, "child_ref")
					finishChildSchema(t, s, id, upstream)
					got, found = s.childSchemaJobForRequest(r, id)
					if !found {
						t.Fatal("legacy fixture receipt disappeared")
					}
				}
				response := mapValue(got["response"])
				if tc.valid {
					tools := searchChildToolMaps(response["tools"])
					if firstString(got, "status") != "completed" || !truthy(response["schema_complete"]) || len(tools) != 1 || firstString(tools[0], "name") != "allowed" {
						t.Fatalf("valid authorized definition was rejected: %#v", got)
					}
				} else if firstString(got, "status") != "failed" || truthy(response["schema_complete"]) {
					t.Fatalf("malformed name was hidden by the exact tool profile: status=%v complete=%v tools=%v", got["status"], response["schema_complete"], response["tools"])
				}
			})
		}
	}
}
