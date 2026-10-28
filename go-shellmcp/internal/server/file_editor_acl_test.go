package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/fsacl"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
)

func TestFilesystemACLChildCallPassesOnlyHubVerifiedGrepMeshReadScope(t *testing.T) {
	roots := []string{"/srv/docs"}
	policy := &hub.FilesystemACL{
		Version: 1, ProfileID: "reader", ProfileVersion: 3, RootsDigest: "roots",
		JobID: "job", Target: "shell:100", Tool: "mcp_call", ArgumentsSHA256: "args",
		ReadRoots: &roots,
	}
	ctx := withFilesystemACL(context.Background(), policy)
	meta, err := filesystemACLMetaForChildCall(ctx, "GrepMesh", "search")
	if err != nil || meta["io.gptadmin/filesystem-acl"] != policy {
		t.Fatalf("verified ACL metadata=%v err=%v", meta, err)
	}
	for _, denied := range [][2]string{{"OtherMCP", "search"}, {"GrepMesh", "find_duplicates"}, {"GrepMesh", "delete"}} {
		if _, err := filesystemACLMetaForChildCall(ctx, denied[0], denied[1]); err == nil {
			t.Fatalf("filesystem ACL reached disallowed child operation %v", denied)
		}
	}
	if meta, err := filesystemACLMetaForChildCall(context.Background(), "GrepMesh", "search"); err != nil || meta != nil {
		t.Fatalf("unscoped call manufactured an ACL: meta=%v err=%v", meta, err)
	}
}

func TestFileEditorFilesystemACLSeparatesReadAndWriteRights(t *testing.T) {
	base := t.TempDir()
	readDir := filepath.Join(base, "read")
	writeDir := filepath.Join(base, "write")
	if err := os.MkdirAll(readDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(writeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readFile := filepath.Join(readDir, "read.txt")
	if err := os.WriteFile(readFile, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRoots := []string{writeDir}
	writeOnly := &fsacl.Policy{WriteRoots: &writeRoots}
	s := New(Config{})

	if _, err := s.mcpFileEditorWithACL(map[string]any{"action": "create", "path": filepath.Join(writeDir, "new", "file.txt"), "content": "created\n"}, writeOnly); err != nil {
		t.Fatalf("write-root create failed: %v", err)
	}
	created, err := os.ReadFile(filepath.Join(writeDir, "new", "file.txt"))
	if err != nil || string(created) != "created\n" {
		t.Fatalf("create readback=%q err=%v", created, err)
	}
	if _, err := s.mcpFileEditorWithACL(map[string]any{"action": "view", "path": filepath.Join(writeDir, "new", "file.txt")}, writeOnly); err == nil {
		t.Fatal("write-only profile revealed file contents")
	}
	if _, err := s.mcpFileEditorWithACL(map[string]any{"action": "str_replace", "path": filepath.Join(writeDir, "new", "file.txt"), "old_text": "created", "new_text": "changed"}, writeOnly); err == nil {
		t.Fatal("write-only profile used a read-dependent edit")
	}

	readRoots := []string{readDir}
	readOnly := &fsacl.Policy{ReadRoots: &readRoots}
	if _, err := s.mcpFileEditorWithACL(map[string]any{"action": "view", "path": readFile}, readOnly); err != nil {
		t.Fatalf("read-root view failed: %v", err)
	}
	if _, err := s.mcpFileEditorWithACL(map[string]any{"action": "delete", "path": readFile}, readOnly); err == nil {
		t.Fatal("read-only profile deleted a file")
	}
}

func TestFileEditorFilesystemACLBlocksSiblingAndSymlinkWrites(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	sibling := filepath.Join(base, "allowed-sibling")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(sibling, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(sentinel, link); err != nil {
		t.Fatal(err)
	}
	roots := []string{allowed}
	policy := &fsacl.Policy{ReadRoots: &roots, WriteRoots: &roots}
	s := New(Config{})
	for _, path := range []string{sentinel, filepath.Join(allowed, "..", "allowed-sibling", "new")} {
		if _, err := s.mcpFileEditorWithACL(map[string]any{"action": "create", "path": path, "content": "escape"}, policy); err == nil {
			t.Fatalf("created outside authorized root at %q", path)
		}
	}
	if _, err := s.mcpFileEditorWithACL(map[string]any{"action": "delete", "path": link}, policy); err == nil {
		t.Fatal("deleted outside sentinel through symlink")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "safe" {
		t.Fatalf("sentinel=%q err=%v", got, err)
	}
}
