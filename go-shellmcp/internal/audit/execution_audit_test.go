// LHC:COVERS go-shellmcp/internal/audit/audit.go
package audit

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecutionAuditDurableRejectsUnlinkedOrReplacedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX open-file unlink semantics")
	}
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "unlinked", true: "replaced"}[replace], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.jsonl")
			logger, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			if err := logger.EventDurable("before_unlink", nil); err != nil {
				t.Fatal(err)
			}
			before, err := logger.file.Stat()
			if err != nil {
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
			if err := logger.EventDurable("must_not_append", nil); err == nil {
				t.Error("EventDurable claimed success on a stale inode")
			}
			after, err := logger.file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if after.Size() != before.Size() {
				t.Error("rejected event was appended to stale inode")
			}
		})
	}
}

func TestExecutionAuditDurableRecordsSurviveLegacyRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := NewWithLimit(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	fields := map[string]any{"execution_id": "one"}
	if err := logger.EventDurable("execution_intent", fields); err != nil {
		t.Fatal(err)
	}
	logger.Event("legacy", map[string]any{"safe": true})
	if err := logger.EventDurable("execution_started", fields); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "execution_intent") || !strings.Contains(string(raw), "execution_started") {
		t.Fatal("durable records rotated away")
	}
	if _, mutated := fields["type"]; mutated {
		t.Fatal("logger mutated caller fields")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.EventDurable("execution_intent", nil); err == nil {
		t.Fatal("closed logger claimed durability")
	}
	disabled, _ := New("")
	if err := disabled.EventDurable("execution_intent", nil); err == nil {
		t.Fatal("disabled logger claimed durability")
	}
}
