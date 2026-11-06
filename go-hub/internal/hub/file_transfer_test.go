package hub

import (
	"net/http/httptest"
	"testing"
)

func TestFileTransferPreservesProfileRootsAndReadOnly(t *testing.T) {
	roots := []string{"/srv/files"}
	for _, action := range []string{"download_begin", "download_chunk", "download_abort", "upload_begin", "upload_chunk", "upload_status", "upload_commit", "upload_abort"} {
		read := fileTransferReadAction(action)
		args := map[string]any{"action": action, "path": "/srv/files/a.bin"}
		profile := AccessProfile{ID: "binary-reader", Version: 7, AccessMode: accessModeReadonly, ReadRoots: &roots}
		r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile)
		if allowed := authorizeFilesystemProfileAction(r, "file:host", "file_transfer", args) == nil; allowed != read {
			t.Fatalf("%s filesystem read=%v", action, allowed)
		}
		if readOnlyFileToolAllowed("file_transfer", args) != read {
			t.Fatal("read-only classification", action)
		}
		if operationIsWrite("file:host", "file_transfer", args) == read {
			t.Fatal("write classification", action)
		}
		policy, err := filesystemACLForRequest(r, "file:host", "file_transfer", args)
		if err != nil || policy == nil || policy.ProfileID != profile.ID || policy.ReadRoots == nil || policy.WriteRoots != nil {
			t.Fatalf("lost ACL binding: %#v %v", policy, err)
		}
		profile.AccessMode = accessModeFull
		profile.ReadRoots = nil
		profile.WriteRoots = &roots
		r = requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile)
		if allowed := authorizeFilesystemProfileAction(r, "file:host", "file_transfer", args) == nil; allowed == read {
			t.Fatalf("%s write-only roots=%v", action, allowed)
		}
	}
	if !fileToolAllowed("file_transfer") {
		t.Fatal("not routed through paired file target")
	}
	found := false
	for _, tool := range fileTools() {
		if tool["name"] == "file_transfer" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing schema")
	}
}

func TestFileTransferCapabilityClearsOnLegacyPoll(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	request := httptest.NewRequest("GET", "/queue/legacy?build_version=194&file_transfer_version=1", nil)
	s.mu.Lock()
	accepted := s.touchShellPollLocked("legacy", request)
	s.mu.Unlock()
	if !accepted || !s.fileTransferCapable("file:legacy") {
		t.Fatal("new capability missing")
	}
	request = httptest.NewRequest("GET", "/queue/legacy?build_version=194", nil)
	s.mu.Lock()
	accepted = s.touchShellPollLocked("legacy", request)
	s.mu.Unlock()
	if !accepted || s.fileTransferCapable("file:legacy") {
		t.Fatal("stale file transfer capability survived executor downgrade")
	}
}

func TestFileTransferHiddenOnLegacyExecutors(t *testing.T) {
	for _, version := range []int{0, 1} {
		found := false
		for _, tool := range fileToolsForAgent(Agent{AgentID: "file:test", Meta: map[string]any{"file_transfer_version": version}}) {
			if tool["name"] == "file_transfer" {
				found = true
			}
		}
		if found != (version == 1) {
			t.Fatal("incorrect legacy capability advertisement", version, found)
		}
	}
}
