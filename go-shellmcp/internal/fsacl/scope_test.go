package fsacl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenConfinesToAuthorizedRootsAndRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	sibling := filepath.Join(base, "allowed-sibling")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(sibling, "sentinel")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	roots := []string{allowed}
	policy := Policy{ReadRoots: &roots, WriteRoots: &roots}

	root, rel, err := policy.Open(filepath.Join(allowed, "new", "file"), true)
	if err != nil {
		t.Fatal(err)
	}
	if rel != filepath.Join("new", "file") {
		t.Fatalf("relative=%q", rel)
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(rel, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	root.Close()

	for _, path := range []string{outside, filepath.Join(allowed, "..", "allowed-sibling", "sentinel")} {
		if root, _, err := policy.Open(path, false); err == nil {
			root.Close()
			t.Fatalf("outside path was authorized: %q", path)
		}
	}
	root, rel, err = policy.Open(link, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := RejectSymlinkComponents(root, rel); err == nil {
		t.Fatal("symlink component was accepted")
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "unchanged" {
		t.Fatalf("outside sentinel changed: %q err=%v", got, err)
	}
}

func TestMissingAndEmptyRightsDenyIndependently(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRoots := []string{root}
	policy := Policy{WriteRoots: &writeRoots}
	if _, _, err := policy.Open(path, false); err == nil {
		t.Fatal("write right granted read")
	}
	if opened, _, err := policy.Open(path, true); err != nil {
		t.Fatal(err)
	} else {
		opened.Close()
	}
	empty := []string{}
	if _, _, err := (Policy{ReadRoots: &empty}).Open(path, false); err == nil {
		t.Fatal("explicit empty roots granted read")
	}
}
