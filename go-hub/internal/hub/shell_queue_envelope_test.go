package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Fast units: prevent a durable running lease preceding a truncated envelope.
// Expected <1s, maximum 10s; no runtime acceptance claim.
func TestShellQueueEnvelopeLegacyRefusesBeforeLease(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	job := &shellJob{ID: "large", Server: "host", Status: "queued", CreatedAt: nowFloat(), ToolName: "mcp_call", Arguments: map[string]any{"payload": strings.Repeat("x", 1748195)}}
	s.shellJobs[job.ID] = job
	s.shellQueues["host"] = []string{job.ID}
	if err := s.saveTaskStateLocked(job.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/host?timeout=1", nil), "host")
	if w.Code != http.StatusRequestEntityTooLarge || job.Status != "queued" || job.StartedAt != 0 || len(s.shellQueues["host"]) != 1 {
		t.Fatalf("legacy lease admitted: code=%d status=%s start=%v queue=%v", w.Code, job.Status, job.StartedAt, s.shellQueues["host"])
	}
	state, err := s.readTaskState()
	if err != nil || state.Shell[job.ID].Status != "queued" {
		t.Fatalf("durable state changed: %v", err)
	}
	// Same job, same arguments; a compatible agent can accept it without replay.
	w = httptest.NewRecorder()
	s.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/host?timeout=1&max_response_bytes=67108864", nil), "host")
	var envelope map[string]any
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope["id"] != job.ID || job.Status != "running" {
		t.Fatalf("compatible delivery failed: %d", w.Code)
	}
	if envelope["arguments"].(map[string]any)["payload"] != job.Arguments["payload"] {
		t.Fatal("arguments were truncated")
	}
	state, err = s.readTaskState()
	if err != nil || state.Shell[job.ID].Status != "running" {
		t.Fatalf("lease not durable: %v", err)
	}
}

func TestShellQueueEnvelopeEncodingFailureBeforeLease(t *testing.T) {
	s := New(Config{})
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	job := &shellJob{ID: "bad", Server: "host", Status: "queued", Arguments: map[string]any{"bad": make(chan int)}}
	s.shellJobs[job.ID] = job
	s.shellQueues["host"] = []string{job.ID}
	w := httptest.NewRecorder()
	s.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/host?timeout=1", nil), "host")
	if w.Code != http.StatusInternalServerError || job.Status != "queued" || job.StartedAt != 0 || len(s.shellQueues["host"]) != 1 {
		t.Fatal("encoding failure admitted a lease")
	}
}

func TestShellQueueEnvelopeBounds(t *testing.T) {
	for query, want := range map[string]int{"": 1 << 20, "0": 1 << 20, "-1": 1 << 20, "bad": 1 << 20, "1748195": 1748195, "67108864": 64 << 20, "67108865": 64 << 20} {
		if got := shellQueueResponseLimit(httptest.NewRequest(http.MethodGet, "/queue/host?max_response_bytes="+query, nil)); got != want {
			t.Fatalf("%q: %d != %d", query, got, want)
		}
	}
}
