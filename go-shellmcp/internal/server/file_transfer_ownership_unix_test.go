//go:build !windows

package server

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileTransferPrivilegedUploadInheritsParentOwnership(t *testing.T) {
	if !processIsPrivileged() {
		t.Skip("requires an isolated privileged filesystem test")
	}
	s, dir := transferTestServer(t)
	if err := os.Chmod(filepath.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	expected := ownershipFromInfo(info)
	path := filepath.Join(dir, "user-readable.bin")
	data := []byte{0, 1, 255, 128, 13, 10}
	id := transferTestBegin(t, s, path, data)
	transferTestChunk(t, s, path, id, 0, data)
	transferTestCall(t, s, context.Background(), map[string]any{
		"action": "upload_commit", "path": path, "transfer_id": id,
	})
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := ownershipFromInfo(info)
	if got != expected {
		t.Fatalf("upload owner=%+v, parent owner=%+v", got, expected)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("upload mode=%o", info.Mode().Perm())
	}
	reader := exec.Command("cat", path)
	reader.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	actual, err := reader.Output()
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("ordinary directory owner cannot read upload: %v", err)
	}

}
