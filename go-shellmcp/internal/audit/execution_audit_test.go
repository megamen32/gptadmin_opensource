// LHC:COVERS go-shellmcp/internal/audit/audit.go
package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
