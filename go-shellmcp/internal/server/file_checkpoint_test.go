package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkpointStructured(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	v, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent=%#v", result["structuredContent"])
	}
	return v
}

func TestFileCheckpointCASDeduplicatesAndDiffs(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "a.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DefaultHome: home, Name: "test-host"})
	first, err := s.mcpFileCheckpoint(map[string]any{"action": "create", "path": project, "name": "baseline", "ttl_days": 30})
	if err != nil {
		t.Fatal(err)
	}
	firstID := checkpointStructured(t, first)["checkpoint_id"].(string)
	second, err := s.mcpFileCheckpoint(map[string]any{"action": "create", "path": project, "name": "same-content", "ttl_days": 30})
	if err != nil {
		t.Fatal(err)
	}
	if checkpointStructured(t, second)["checkpoint_id"].(string) == firstID {
		t.Fatal("ids must be unique")
	}
	var objects int
	_ = filepath.WalkDir(filepath.Join(s.fileCheckpointRoot(), "objects"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			objects++
		}
		return nil
	})
	if objects != 1 {
		t.Fatalf("CAS objects=%d want 1", objects)
	}
	if err := os.WriteFile(path, []byte("two\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffResult, err := s.mcpFileCheckpoint(map[string]any{"action": "diff", "checkpoint_id": firstID})
	if err != nil {
		t.Fatal(err)
	}
	diff := checkpointStructured(t, diffResult)["diff"].(checkpointDiff)
	if len(diff.Modified) != 1 || diff.Modified[0] != path {
		t.Fatalf("modified=%v", diff.Modified)
	}
	if len(diff.Added) != 1 || !strings.HasSuffix(diff.Added[0], "new.txt") {
		t.Fatalf("added=%v", diff.Added)
	}
}

func TestFileCheckpointRestoreCreatesReversibleSafetyCheckpoint(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	_ = os.MkdirAll(project, 0o755)
	a := filepath.Join(project, "a.txt")
	b := filepath.Join(project, "b.txt")
	_ = os.WriteFile(a, []byte("baseline\n"), 0o640)
	s := New(Config{DefaultHome: home, Name: "test-host"})
	base, err := s.mcpFileCheckpoint(map[string]any{"action": "create", "path": project, "name": "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	baseID := checkpointStructured(t, base)["checkpoint_id"].(string)
	_ = os.WriteFile(a, []byte("live-change\n"), 0o600)
	_ = os.Chmod(a, 0o600)
	_ = os.WriteFile(b, []byte("created-later\n"), 0o644)
	restored, err := s.mcpFileCheckpoint(map[string]any{"action": "restore", "checkpoint_id": baseID})
	if err != nil {
		t.Fatal(err)
	}
	st := checkpointStructured(t, restored)
	safetyID, ok := st["safety_checkpoint_id"].(string)
	if !ok || safetyID == "" {
		t.Fatalf("no safety id: %#v", st)
	}
	got, _ := os.ReadFile(a)
	if string(got) != "baseline\n" {
		t.Fatalf("restore content=%q", got)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatalf("new file survived restore: %v", err)
	}
	info, _ := os.Stat(a)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode=%o", info.Mode().Perm())
	}

	// Restoring the automatic safety checkpoint must recover the exact live state from before restore.
	if _, err := s.mcpFileCheckpoint(map[string]any{"action": "restore", "checkpoint_id": safetyID}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(a)
	if string(got) != "live-change\n" {
		t.Fatalf("safety restore content=%q", got)
	}
	got, _ = os.ReadFile(b)
	if string(got) != "created-later\n" {
		t.Fatalf("safety restore missing later file: %q", got)
	}
	info, _ = os.Stat(a)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("safety restored mode=%o", info.Mode().Perm())
	}
}

func TestFileCheckpointSafetyCanRepresentMissingRoot(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "x.txt")
	_ = os.WriteFile(path, []byte("saved\n"), 0o644)
	s := New(Config{DefaultHome: home})
	cp, err := s.mcpFileCheckpoint(map[string]any{"action": "create", "path": path, "name": "exists"})
	if err != nil {
		t.Fatal(err)
	}
	id := checkpointStructured(t, cp)["checkpoint_id"].(string)
	_ = os.Remove(path)
	restore, err := s.mcpFileCheckpoint(map[string]any{"action": "restore", "checkpoint_id": id})
	if err != nil {
		t.Fatal(err)
	}
	safety := checkpointStructured(t, restore)["safety_checkpoint_id"].(string)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("target not restored: %v", err)
	}
	if _, err := s.mcpFileCheckpoint(map[string]any{"action": "restore", "checkpoint_id": safety}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing-root safety did not delete restored path: %v", err)
	}
}

func TestFileCheckpointDeleteRunsCASGC(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "x.txt")
	_ = os.WriteFile(path, []byte("x\n"), 0o644)
	s := New(Config{DefaultHome: home})
	cp, err := s.mcpFileCheckpoint(map[string]any{"action": "create", "path": path, "name": "temporary"})
	if err != nil {
		t.Fatal(err)
	}
	id := checkpointStructured(t, cp)["checkpoint_id"].(string)
	result, err := s.mcpFileCheckpoint(map[string]any{"action": "delete", "checkpoint_id": id})
	if err != nil {
		t.Fatal(err)
	}
	st := checkpointStructured(t, result)
	if st["gc_removed_objects"].(int) != 1 {
		t.Fatalf("GC result=%#v", st)
	}
}
