package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerOwnedDisposableDataSharesOneBudget(t *testing.T) {
	root := t.TempDir()
	spill := filepath.Join(root, "spill")
	outbox := filepath.Join(root, "outbox")
	auditPath := filepath.Join(root, "logs", "audit.jsonl")
	home := filepath.Join(root, "home")
	backup := filepath.Join(home, ".gptadmin", "file-backups", "old", "artifact")
	paths := []string{filepath.Join(spill, "old.out"), filepath.Join(outbox, "job.json"), auditPath, backup}
	for i, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 40), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(i+1), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(root, "logs", "keep.log")
	if err := os.WriteFile(unrelated, make([]byte, 80), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(Config{SpillDir: spill, OutboxDir: outbox, AuditLog: auditPath, DefaultHome: home, StorageLimitBytes: 100})
	defer s.Close()
	if err := s.enforceStorage(nil); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			total += info.Size()
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if total > 100 {
		t.Fatalf("ShellMCP-owned bytes=%d want <=100", total)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated sibling log was removed: %v", err)
	}
}
