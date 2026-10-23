package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func rootMCPResultForContractTest(t *testing.T, s *Server, r *http.Request, method string, params map[string]any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(payload))).WithContext(r.Context())
	r.Header.Set("Authorization", "Bearer ctl")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", method, w.Code, w.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s response: %v; body=%s", method, err, w.Body.String())
	}
	return mapValue(envelope["result"])
}

func TestRootModernDiscoverUsesAuthenticatedProfileInstructions(t *testing.T) {
	s, caller, _ := instructionFixture(t)
	if err := s.updateInstructionSets(func(sets map[string]InstructionSet) error {
		for id, content := range map[string]string{
			"rules-a": "QA marker profile alpha; route catalog requests through search.",
			"rules-b": "QA marker profile beta; use the beta catalog workflow.",
		} {
			sets[id] = InstructionSet{ID: id, Content: content, Version: instructionSetVersion(content)}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, profile, set, accessMode, want, absent string
	}{
		{"alpha-full", "alpha", "rules-a", accessModeFull, "QA marker profile alpha", "QA marker profile beta"},
		{"beta-readonly", "beta", "rules-b", accessModeReadonly, "QA marker profile beta", "QA marker profile alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := requestWithAccessProfile(caller, AccessProfile{
				ID: tc.profile, InstructionSetID: tc.set, Version: 1,
				AccessMode: tc.accessMode, ApprovalMode: approvalModeReadOnly,
				AllowedTargets: []string{"hub"}, AllowedTools: []string{"search"},
			})
			got := rootMCPResultForContractTest(t, s, r, "server/discover", map[string]any{})
			text := firstString(got, "instructions")
			if !strings.Contains(text, tc.want) || strings.Contains(text, tc.absent) {
				t.Fatalf("modern discover instructions=%q, want marker %q and no cross-profile marker %q", text, tc.want, tc.absent)
			}
			if got["supportedVersions"] == nil || firstString(mapValue(got["serverInfo"]), "name") != "gptadmin-go-hub" || got["cacheScope"] != "private" {
				t.Fatalf("modern discover protocol metadata changed: %v", got)
			}
		})
	}

	legacyRequest := requestWithAccessProfile(caller, AccessProfile{
		ID: "beta", InstructionSetID: "rules-b", Version: 1,
		AccessMode: accessModeReadonly, ApprovalMode: approvalModeReadOnly,
		AllowedTargets: []string{"hub"}, AllowedTools: []string{"search"},
	})
	legacy := rootMCPResultForContractTest(t, s, legacyRequest, "initialize", map[string]any{"protocolVersion": "2025-11-25"})
	if !strings.Contains(firstString(legacy, "instructions"), "QA marker profile beta") {
		t.Fatalf("legacy initialize profile instructions=%v", legacy)
	}

	unbound := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	defaultResult := rootMCPResultForContractTest(t, s, unbound, "server/discover", map[string]any{})
	if !strings.Contains(firstString(defaultResult, "instructions"), "Fixture instructions version one") ||
		strings.Contains(firstString(defaultResult, "instructions"), "QA marker profile") {
		t.Fatalf("unbound request did not receive only default instructions: %v", defaultResult)
	}

	childRequest := requestWithAccessProfile(caller, AccessProfile{
		ID: "alpha", InstructionSetID: "rules-a", Version: 1,
		AccessMode: accessModeFull, AllowedTargets: []string{"*"}, AllowedTools: []string{"*"},
	})
	childResult, childErr, childNoContent := s.agentMCPJSONRPC(childRequest, Agent{
		AgentID: "child:fixture", Kind: "child_mcp",
		Meta: map[string]any{"parent_server_id": "shell:fixture", "child_ref": "GrepMesh"},
	}, map[string]any{"method": "server/discover", "params": map[string]any{}})
	childText := firstString(mapValue(childResult), "instructions")
	if childErr != nil || childNoContent || childText == "" || strings.Contains(childText, "QA marker profile alpha") {
		t.Fatalf("child discovery must not receive trusted root profile instructions: result=%v error=%v no_content=%v", childResult, childErr, childNoContent)
	}
}

func TestToolCatalogDescriptionsSeparateMCPCatalogFromFileSearch(t *testing.T) {
	search := searchToolDefinition()
	searchDescription := strings.ToLower(firstString(search, "description"))
	if firstString(search, "name") != "search" || firstString(search, "title") != "Search MCP tools" ||
		!strings.Contains(searchDescription, "mcp tool") || !strings.Contains(searchDescription, "schema") ||
		!strings.Contains(searchDescription, "setting") || !strings.Contains(searchDescription, "grepmesh") ||
		!strings.Contains(searchDescription, "local rg") || !strings.Contains(searchDescription, "cross-host") ||
		!strings.Contains(searchDescription, "find_paths") || !strings.Contains(searchDescription, "read_text") ||
		strings.Contains(searchDescription, "search_text") {
		t.Fatalf("search must retain its public name and distinguish MCP catalog search from file search: %v", search)
	}

	var schema map[string]any
	for _, tool := range hubTools() {
		if firstString(tool, "name") == "schema" {
			schema = tool
			break
		}
	}
	schemaDescription := strings.ToLower(firstString(schema, "description"))
	if schema == nil || !strings.Contains(schemaDescription, "exact") || !strings.Contains(schemaDescription, "schema") {
		t.Fatalf("schema must explain exact tool schema selection: %v", schema)
	}

	exec := codeModeCatalogTool("exec")
	execDescription := strings.ToLower(firstString(exec, "description"))
	if firstString(exec, "name") != "exec" || firstString(exec, "title") != "Compose MCP calls" || !strings.Contains(execDescription, "javascript") ||
		!strings.Contains(execDescription, "search") || !strings.Contains(execDescription, "grepmesh") ||
		!strings.Contains(execDescription, "local rg") || !strings.Contains(execDescription, "cross-host") ||
		!strings.Contains(execDescription, "find_paths") || !strings.Contains(execDescription, "read_text") ||
		strings.Contains(execDescription, "search_text") {
		t.Fatalf("exec must describe bounded JavaScript MCP composition and direct file search elsewhere: %v", exec)
	}
}
