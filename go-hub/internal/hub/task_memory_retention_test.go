package hub

import (
	"fmt"
	"strings"
	"testing"
)

// Focused integration: real SQLite and task maintenance. Expected 2s, maximum
// 20s. Detects results retained in RAM after the configured storage horizon;
// recent receipts, active work and uncommitted results must remain available.
func TestTaskMaintenanceReleasesExpiredMemory(t *testing.T) {
	for _, withActive := range []bool{false, true} {
		t.Run(fmt.Sprint("active=", withActive), func(t *testing.T) {
			s := New(Config{ConfigDir: t.TempDir()})
			defer s.Close()
			s.mu.Lock()
			defer s.mu.Unlock()
			now := nowFloat()
			retentionHours, recentAgeHours := 24, 1
			if withActive {
				retentionHours, recentAgeHours = 48, 36
			}
			s.settings.Values["completed_job_retention_hours"] = 72
			for i, status := range []string{"completed", "failed", "cancelled"} {
				id := fmt.Sprintf("expired-%d", i)
				s.shellJobs[id] = &shellJob{ID: id, Status: status, DoneAt: now - 60*3600, Result: strings.Repeat(id, 8192)}
				s.relayJobs[id+"-relay"] = &relayJob{ID: id + "-relay", Status: status, DoneAt: now - 60*3600, Result: map[string]any{"text": strings.Repeat(id, 8192)}}
			}
			s.shellJobs["recent"] = &shellJob{ID: "recent", Server: "host", Status: "completed", DoneAt: now - float64(recentAgeHours*3600), Result: "preserved receipt"}
			if withActive {
				s.shellJobs["accepted"] = &shellJob{ID: "accepted", Status: "queued", CreatedAt: now - 96*3600}
			}
			if err := s.saveTaskStateLocked(); err != nil {
				t.Fatal(err)
			}
			s.settings.Values["completed_job_retention_hours"] = retentionHours
			// A failed commit must not turn into silent data loss during cleanup.
			s.shellJobs["uncommitted"] = &shellJob{ID: "uncommitted", Status: "failed", DoneAt: now - 60*3600, Result: "not durable"}
			if _, err := s.maintainTaskStateLocked(); err != nil {
				t.Fatal(err)
			}
			for id := range s.shellJobs {
				if strings.HasPrefix(id, "expired-") {
					t.Errorf("expired shell result still retained in RAM: %s", id)
				}
			}
			if len(s.relayJobs) != 0 {
				t.Errorf("expired relay results still retained in RAM: %d", len(s.relayJobs))
			}
			if s.shellJobs["uncommitted"] == nil || (withActive && s.shellJobs["accepted"] == nil) {
				t.Fatal("maintenance discarded uncommitted or accepted work")
			}
			// Read the actual receipt through the public task implementation.
			s.mu.Unlock()
			got, rpcErr := s.mcpTaskGet("recent", "shell:host")
			s.mu.Lock()
			if rpcErr != nil || !strings.Contains(fmt.Sprint(got), "preserved receipt") {
				t.Fatalf("retained receipt changed: result=%v error=%v", got, rpcErr)
			}
			state, err := s.readTaskRecords("recent")
			if err != nil || state.Shell["recent"].Result != "preserved receipt" {
				t.Fatalf("durable retained result changed: %v %v", state, err)
			}
		})
	}
}
