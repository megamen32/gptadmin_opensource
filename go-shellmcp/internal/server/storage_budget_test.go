package server

import (
	"errors"
	"fmt"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
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

func TestPendingReceiptAndSpillSurviveAllStorageCleanup(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprint(nested), func(t *testing.T) {
			root := t.TempDir()
			spill := filepath.Join(root, "spill")
			outbox := filepath.Join(root, "outbox")
			if nested {
				outbox = filepath.Join(spill, "outbox")
			}
			s := New(Config{SpillDir: spill, OutboxDir: outbox, StorageLimitBytes: 1, SpoolRetention: time.Hour})
			defer s.Close()
			if err := os.MkdirAll(spill, 0700); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(spill, "pending.stdout")
			if err := os.WriteFile(output, []byte("irreplaceable stdout"), 0600); err != nil {
				t.Fatal(err)
			}
			past := time.Now().Add(-48 * time.Hour)
			os.Chtimes(output, past, past)
			s.spoolOutbox("pending", hub.TaskResult{ID: "pending", Result: map[string]any{"stdout_path": output}}, errors.New("connection refused"))
			s.cleanupSpoolByAge()
			if err := s.enforceStorage(nil); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{output, filepath.Join(outbox, "pending.json")} {
				if _, err := os.Stat(p); err != nil {
					t.Fatalf("mandatory data removed %s: %v", p, err)
				}
			}
		})
	}
}
