package hub

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func catalogNamesSet(tools []map[string]any) map[string]bool {
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[firstString(tool, "name")] = true
	}
	return names
}

func TestBoundFullCatalogIncludesAllowedSearchAndExec(t *testing.T) {
	readRoots := []string{}
	writeRoots := []string{
		"/tmp/qa23-write-profile/write-a",
		"/tmp/qa23-write-profile/write-b",
	}
	profile := AccessProfile{
		ID:             "qa23-acl-write-20261023",
		AccessMode:     accessModeFull,
		ApprovalMode:   approvalModeUnrestricted,
		AllowedTargets: []string{"hub", "file:node-44"},
		AllowedTools:   []string{"exec", "search", "file_editor", "system_inspect"},
		ReadRoots:      &readRoots,
		WriteRoots:     &writeRoots,
		ToolRules: []AccessToolRule{
			{Target: "file:node-44", Tool: "file_editor", Actions: []string{"view", "create", "str_replace", "batch_edit", "delete"}},
			{Target: "file:node-44", Tool: "system_inspect", Actions: []string{"read_file", "list_directory"}, ReadOnly: true},
		},
	}
	r := requestWithAuthClaims(
		requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile),
		map[string]any{"sub": profile.ID, "client_id": "qa23-acl-write-client-20261023", "scope": "gptadmin.read gptadmin.exec"},
	)

	for _, full := range []bool{false, true} {
		names := catalogNamesSet(compactFacadeTools(r, full))
		for _, allowed := range []string{"search", "exec"} {
			if !names[allowed] {
				t.Errorf("bound full profile (detail=%t) hides explicitly allowed native facade %q: %v", full, allowed, names)
			}
		}
		for _, forbidden := range []string{"discover", "schema", "ro", "job", "expand", "file_editor", "system_inspect", "settings_set"} {
			if names[forbidden] {
				t.Errorf("bound full profile (detail=%t) exposes non-allowed root or child tool %q: %v", full, forbidden, names)
			}
		}
	}
}

func TestBoundFullCatalogDoesNotAddDeniedNativeFacades(t *testing.T) {
	profile := AccessProfile{
		ID:             "discover-only",
		AccessMode:     accessModeFull,
		AllowedTargets: []string{"hub"},
		AllowedTools:   []string{"discover"},
	}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile)

	for _, full := range []bool{false, true} {
		names := catalogNamesSet(compactFacadeTools(r, full))
		if !names["discover"] {
			t.Fatalf("allowed discover facade is missing (detail=%t): %v", full, names)
		}
		for _, denied := range []string{"search", "exec", "ro"} {
			if names[denied] {
				t.Errorf("profile without %q exposed it in tools/list (detail=%t): %v", denied, full, names)
			}
		}
	}
}

func TestBoundReadOnlyCatalogKeepsOnlyAllowedReadFacades(t *testing.T) {
	profile := AccessProfile{
		ID:             "readonly-search",
		AccessMode:     accessModeReadonly,
		AllowedTargets: []string{"hub"},
		AllowedTools:   []string{"search", "ro"},
	}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile)

	for _, full := range []bool{false, true} {
		names := catalogNamesSet(compactFacadeTools(r, full))
		for _, allowed := range []string{"search", "ro"} {
			if !names[allowed] {
				t.Errorf("read-only profile (detail=%t) hides explicitly allowed facade %q: %v", full, allowed, names)
			}
		}
		if names["exec"] {
			t.Fatalf("read-only profile (detail=%t) advertises write-capable exec: %v", full, names)
		}
	}
}

func TestBoundFullCatalogDoesNotRelaxNestedTargetOrActionGates(t *testing.T) {
	readRoots := []string{}
	writeRoots := []string{"/tmp/qa23-write-profile/write-a"}
	profile := AccessProfile{
		ID:             "write-narrow-target",
		AccessMode:     accessModeFull,
		AllowedTargets: []string{"hub", "file:other"},
		AllowedTools:   []string{"exec", "search", "file_editor", "system_inspect"},
		ReadRoots:      &readRoots,
		WriteRoots:     &writeRoots,
		ToolRules: []AccessToolRule{
			{Target: "file:other", Tool: "file_editor", Actions: []string{"create"}},
			{Target: "file:other", Tool: "system_inspect", Actions: []string{"read_file", "list_directory"}, ReadOnly: true},
		},
	}
	r := requestWithAuthClaims(
		requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile),
		map[string]any{"sub": profile.ID, "client_id": profile.ID, "scope": "gptadmin.read gptadmin.exec"},
	)

	// Listing the allowed routing facade does not grant a nested target.
	names := catalogNamesSet(compactFacadeTools(r, false))
	if !names["exec"] || !names["search"] {
		t.Fatalf("explicitly allowed native facades disappeared: %v", names)
	}
	if err := authorizeFacadeCall(r, "exec", map[string]any{"code": "return 1;"}); err != nil {
		t.Fatalf("profile-authorized exec facade was denied: %v", err)
	}
	if err := authorizeToolAction(r, "file:node-44", "file_editor", map[string]any{"action": "create", "path": "/tmp/qa23-write-profile/write-a/new.txt"}); err == nil || !strings.Contains(err.Error(), "access profile denies") {
		t.Fatalf("nested call escaped the profile target boundary: %v", err)
	}
	if err := authorizeToolAction(r, "file:other", "file_editor", map[string]any{"action": "str_replace", "path": "/tmp/qa23-write-profile/write-a/new.txt"}); err == nil || !strings.Contains(err.Error(), "access profile denies this action") {
		t.Fatalf("nested call escaped the profile action boundary: %v", err)
	}
	readArgs := map[string]any{"action": "read_file", "path": "/tmp/qa23-write-profile/write-a/new.txt"}
	if err := authorizeToolAction(r, "file:other", "system_inspect", readArgs); err != nil {
		t.Fatalf("profile's declared read operation was refused before executor ACL validation: %v", err)
	}
	policy, err := filesystemACLForRequest(r, "file:other", "system_inspect", readArgs)
	if err != nil || policy == nil || policy.ReadRoots == nil || len(*policy.ReadRoots) != 0 || policy.WriteRoots == nil || len(*policy.WriteRoots) != 1 {
		t.Fatalf("nested call lost the exact empty-read/nonempty-write roots: policy=%#v err=%v", policy, err)
	}
}
