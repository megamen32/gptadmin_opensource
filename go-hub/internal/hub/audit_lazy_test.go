package hub

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Focused integration, real private journal/HTTP handler. Expected 2s/max20s.
// Detects eager startup decoding; cold history must remain readable on demand.
func TestAuditHistoryIsLazyAndPreservesRecentEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	row, _ := json.Marshal(auditEvent{Time: "2026-10-14T00:00:00Z", Name: "historical-event", Fields: map[string]any{"detail": "old receipt"}})
	if err := os.WriteFile(path, append(row, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{AuditStateFile: path})
	defer s.Close()
	for _, event := range s.audit {
		if event.Name == "historical-event" {
			t.Fatal("constructor eagerly decoded historical audit; load only on history request")
		}
	}
	s.mu.Lock()
	s.addAuditLocked("new-event", map[string]any{"detail": "new receipt"})
	s.mu.Unlock()
	before, _ := os.ReadFile(path)
	w := httptest.NewRecorder()
	s.adminAudit(w, httptest.NewRequest("GET", "/admin/api/audit", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "historical-event") || strings.Count(w.Body.String(), `"name":"new-event"`) != 1 {
		t.Fatalf("lazy history lost or duplicated receipt: %d %s", w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(path)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("history read modified durable journal")
	}
}

// A broken old prefix must not be read on startup or scanned when requesting
// the recent tail. The incomplete final append is not a complete audit event.
func TestAuditHistoryReadsOnlyBoundedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse old prefix makes a whole-file read fail without allocating a fixture.
	if _, err := f.Seek(8<<20, 0); err != nil {
		t.Fatal(err)
	}
	f.WriteString("\n")
	for i := 0; i < 600; i++ {
		fmt.Fprintf(f, "{\"time\":\"2026-10-14T00:00:00Z\",\"name\":\"tail-%04d\"}\n", i)
	}
	f.WriteString(`{"incomplete":`)
	f.Close()
	s := New(Config{AuditStateFile: path})
	defer s.Close()
	w := httptest.NewRecorder()
	s.adminAudit(w, httptest.NewRequest("GET", "/admin/api/audit?limit=500", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "tail-0599") || strings.Contains(w.Body.String(), "tail-0099") {
		t.Fatalf("did not read bounded latest500 events: %d %.200s", w.Code, w.Body.String())
	}
}

// Fast unit, expected <1s/max10s. Dropped prefix references and oversized
// capacity must be released when the configured window shrinks.
func TestAuditMemoryReleasesDiscardedPrefix(t *testing.T) {
	backing := make([]auditEvent, 2000)
	for i := range backing {
		backing[i] = auditEvent{Name: fmt.Sprint(i), Fields: map[string]any{"payload": strings.Repeat("x", 1024)}}
	}
	s := &Server{audit: backing, settings: hubSettings{Values: map[string]any{"audit_max_events": 100}}}
	s.addAuditLocked("latest", nil)
	if len(s.audit) != 100 || cap(s.audit) > 200 || s.audit[99].Name != "latest" {
		t.Fatalf("unbounded audit window: len=%d cap=%d", len(s.audit), cap(s.audit))
	}
	if backing[0].Fields != nil {
		t.Fatal("discarded audit prefix still retains payload references")
	}
}
