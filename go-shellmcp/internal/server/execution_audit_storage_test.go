// LHC:COVERS go-shellmcp/internal/server/server.go go-shellmcp/internal/server/execution_audit.go go-shellmcp/internal/audit/audit.go
package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
)

func TestExecutionAuditMandatoryDuringRuntimeBudgetCleanup(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed_default", true: "explicit_path"}[explicit], func(t *testing.T) {
			root := t.TempDir()
			identity := filepath.Join(root, "identity")
			spill := filepath.Join(root, "spill")
			cfg := Config{IdentityDir: identity, SpillDir: spill, OutboxDir: filepath.Join(spill, "outbox"), StorageLimitBytes: 3 << 20, DisableSelfUpdate: true}
			if explicit {
				cfg.AuditLog = filepath.Join(root, "logs", "explicit.jsonl")
			}
			s := New(cfg)
			defer s.Close()
			if err := os.MkdirAll(spill, 0700); err != nil {
				t.Fatal(err)
			}
			pending := filepath.Join(spill, "pending.stdout")
			if err := os.WriteFile(pending, make([]byte, 2<<20), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.auditLog.EventDurable("execution_intent", map[string]any{"execution_id": "retained-before-budget"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(s.cfg.AuditLog)
			if err != nil {
				t.Fatal(err)
			}
			// This is the production heartbeat/queue settings path, with real files
			// over budget. Protected spills must not force eviction of the open audit.
			s.applyHubRuntimeSettings(map[string]any{"storage_max_mb": 1})
			after, err := os.Stat(s.cfg.AuditLog)
			if err != nil {
				t.Fatalf("runtime budget cleanup unlinked the audit: %v", err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("cleanup replaced the open audit inode")
			}
			if err := s.auditLog.EventDurable("execution_intent", map[string]any{"execution_id": "retained-after-budget"}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(s.cfg.AuditLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"retained-before-budget", "retained-after-budget"} {
				if !bytes.Contains(data, []byte(id)) {
					t.Fatalf("canonical audit lost %s", id)
				}
			}
			if _, err := os.Stat(pending); err != nil {
				t.Fatal("pending spill was removed", err)
			}
		})
	}
}

func TestExecutionAuditUnlinkedOrReplacedPathPreventsRealCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX open-file unlink and command fixture")
	}
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "unlinked", true: "replaced"}[replace], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "audit.jsonl")
			marker := filepath.Join(root, "must-not-exist")
			s := New(Config{IdentityDir: filepath.Join(root, "identity"), AuditLog: path, SpillDir: filepath.Join(root, "spill"), LogLimit: 8192, DisableSelfUpdate: true})
			defer s.Close()
			if err := s.auditLog.EventDurable("before_unlink", map[string]any{"safe": true}); err != nil {
				t.Fatal(err)
			}
			if replace {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			result := s.runShell(context.Background(), shell.Request{Cmd: "printf ran > '" + marker + "'", RunAsUser: "root", SpillDir: s.cfg.SpillDir})
			if result.ReturnCode != -1 || result.Error == "" {
				t.Errorf("non-addressable audit must reject before execution: %+v", result)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("command executed without an addressable durable intent: %v", err)
			}
			if !replace {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Error("audit silently recreated an unlinked path")
				}
			} else {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "replacement" {
					t.Error("audit silently modified replacement path", err)
				}
			}
		})
	}
}
