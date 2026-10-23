package hub

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestFilesystemProfilePreservesAbsentAndExplicitEmptyRoots(t *testing.T) {
	legacy, err := normalizeAccessProfile(AccessProfile{ID: "legacy", AccessMode: accessModeFull, Version: 1})
	if err != nil || filesystemACLActive(legacy) {
		t.Fatalf("absent roots must keep legacy behavior: active=%v err=%v", filesystemACLActive(legacy), err)
	}
	empty := []string{}
	active, err := normalizeAccessProfile(AccessProfile{ID: "active", AccessMode: accessModeFull, Version: 1, ReadRoots: &empty})
	if err != nil || !filesystemACLActive(active) || active.ReadRoots == nil || len(*active.ReadRoots) != 0 || active.WriteRoots != nil {
		t.Fatalf("explicit empty root list was not preserved: %#v err=%v", active, err)
	}
	cloned := cloneAccessProfile(active)
	if cloned.ReadRoots == active.ReadRoots || cloned.ReadRoots == nil || len(*cloned.ReadRoots) != 0 {
		t.Fatal("cloned roots must retain explicit empty and not share the pointer")
	}
	statePath := filepath.Join(t.TempDir(), "profiles.json")
	if err := saveAccessProfilesState(statePath, map[string]AccessProfile{"legacy": legacy, "active": active}); err != nil {
		t.Fatal(err)
	}
	loaded, err := readAccessProfilesState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["legacy"].ReadRoots != nil || loaded["legacy"].WriteRoots != nil {
		t.Fatal("legacy absent roots changed during persistence")
	}
	if loaded["active"].ReadRoots == nil || len(*loaded["active"].ReadRoots) != 0 {
		t.Fatal("explicit empty roots changed during persistence")
	}
}

func TestFilesystemProfileAuthorizationFailsClosedAndKeepsSafeFacade(t *testing.T) {
	read := []string{"/tmp/allowed"}
	profile := AccessProfile{ID: "scoped", AccessMode: accessModeFull, ReadRoots: &read}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile)
	cases := []struct {
		target string
		tool   string
		args   map[string]any
		allow  bool
	}{
		{"hub", "ro", nil, true},
		{"file:host", "system_inspect", map[string]any{"action": "read_file", "path": "/tmp/allowed/a"}, true},
		{"shell:host", "shell_exec", map[string]any{"cmd": "cat /tmp/allowed/a"}, false},
		{"file:host", "file_editor", map[string]any{"action": "view"}, true},
		{"file:host", "file_editor", map[string]any{"action": "create"}, false},
		{"file:host", "file_checkpoint", map[string]any{"action": "restore"}, false},
		{"file:host", "file_backup", map[string]any{"action": "restore"}, false},
		{"child", "mcp_call", map[string]any{"name": "read"}, false},
		{"child", "resources/read", map[string]any{"uri": "file:///tmp/allowed/a"}, false},
		{"shell:host", "system_resource_summary", map[string]any{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.target+"/"+tc.tool, func(t *testing.T) {
			err := authorizeFilesystemProfileAction(r, tc.target, tc.tool, tc.args)
			if (err == nil) != tc.allow {
				t.Fatalf("allow=%v err=%v", tc.allow, err)
			}
		})
	}
}

func TestFilesystemProfileWriteOnlyDoesNotGrantRead(t *testing.T) {
	writes := []string{"/tmp/writable"}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "write-only", AccessMode: accessModeFull, WriteRoots: &writes})
	if err := authorizeFilesystemProfileAction(r, "file:host", "file_editor", map[string]any{"action": "view"}); err == nil {
		t.Fatal("write_roots alone must not grant file reads")
	}
	if err := authorizeFilesystemProfileAction(r, "file:host", "file_editor", map[string]any{"action": "create"}); err != nil {
		t.Fatalf("write_roots should allow blind create to reach path check: %v", err)
	}
	if err := authorizeFilesystemProfileAction(r, "file:host", "file_editor", map[string]any{"action": "str_replace"}); err == nil {
		t.Fatal("read-dependent write was allowed without read_roots")
	}
}

func TestFilesystemACLQueueBindingRejectsChangedArgsAndProfile(t *testing.T) {
	read := []string{"/srv/project"}
	write := []string{"/srv/project/out"}
	profile := AccessProfile{ID: "scoped", Version: 4, ReadRoots: &read, WriteRoots: &write}
	args := map[string]any{"action": "create", "path": "/srv/project/out/result.txt", "content": "safe"}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile)
	policy, err := filesystemACLForRequest(r, "file:node-a", "file_editor", args)
	if err != nil || policy == nil {
		t.Fatalf("policy=%#v err=%v", policy, err)
	}
	policy.JobID = "job-1"
	job := &shellJob{ID: "job-1", Server: "node-a", ToolName: "file_editor", FilesystemACL: policy}
	s := &Server{accessProfiles: map[string]AccessProfile{"scoped": profile}}
	digest, err := filesystemACLArgumentsDigest(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.validateFilesystemACLJobLocked(job, digest); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	if err := s.validateFilesystemACLJobLocked(job, "different"); err == nil {
		t.Fatal("changed args digest accepted")
	}
	changed := profile
	changed.Version++
	s.accessProfiles[profile.ID] = changed
	if err := s.validateFilesystemACLJobLocked(job, digest); err == nil {
		t.Fatal("queued policy survived profile version change")
	}
}

func TestFilesystemResourceSummaryIsReadOnlyForBoundAndReadonlyProfiles(t *testing.T) {
	profile := AccessProfile{ID: "metrics-reader", AccessMode: accessModeReadonly, ApprovalMode: approvalModeReadOnly,
		AllowedTargets: []string{"shell:node-a"}, AllowedTools: []string{"system_resource_summary"}}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile)
	if err := authorizeToolAction(r, "shell:node-a", "system_resource_summary", map[string]any{}); err != nil {
		t.Fatalf("typed resource summary denied: %v", err)
	}
	if operationIsWrite("shell:node-a", "system_resource_summary", map[string]any{}) {
		t.Fatal("typed resource summary was classified as a write")
	}
}
