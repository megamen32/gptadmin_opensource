package inspect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileIsBoundedAndRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostic.env")
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZG1pbiJ9.c2lnbmF0dXJl"
	if err := os.WriteFile(path, []byte("STATE=ok\nTOKEN="+jwt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Run(Request{Action: "read_file", Path: path, MaxBytes: 4096, AllowedRoots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "STATE=ok") || strings.Contains(result.Content, jwt) {
		t.Fatalf("unexpected inspection result: %+v", result)
	}
	if !strings.Contains(result.Content, "<redacted:token>") {
		t.Fatalf("missing token marker: %+v", result)
	}
}

func TestListDirectoryDoesNotExposeFileContents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("must-not-be-read"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Run(Request{Action: "list_directory", Path: dir, AllowedRoots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Name != "visible.txt" {
		t.Fatalf("unexpected directory entries: %+v", result)
	}
	if strings.Contains(result.Content, "must-not-be-read") {
		t.Fatalf("directory listing leaked file content: %+v", result)
	}
}

func TestUnsupportedActionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(Request{Action: "run_command", Path: dir, AllowedRoots: []string{dir}}); err == nil {
		t.Fatal("unsupported inspection action was accepted")
	}
}

func TestReadFileRejectsPathsOutsideAllowedRoots(t *testing.T) {
	allowed := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("not-for-model"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Request{Action: "read_file", Path: outside, AllowedRoots: []string{allowed}}); err == nil {
		t.Fatal("inspection escaped its allowed roots")
	}
}

func TestReadFileRejectsSymlinkEscapeAndCredentialDirectories(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("not-for-model"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := Run(Request{Action: "read_file", Path: link, AllowedRoots: []string{root}}); err == nil {
			t.Fatal("inspection followed a symlink outside its allowed roots")
		}
	}
	sshDir := filepath.Join(root, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(sshDir, "id_ed25519")
	if err := os.WriteFile(key, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Request{Action: "read_file", Path: key, AllowedRoots: []string{root}}); err == nil {
		t.Fatal("inspection entered a credential directory")
	}
}
