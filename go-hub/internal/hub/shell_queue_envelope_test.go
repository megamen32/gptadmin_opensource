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

func TestShellQueueLegacyOversizedHeadDoesNotBlockCompatibleJob(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	t.Cleanup(func() { _ = s.Close() })
	large := &shellJob{ID: "large", Server: "host", Status: "queued", ToolName: "mcp_call", Arguments: map[string]any{"payload": strings.Repeat("x", 1748195)}}
	small := &shellJob{ID: "small", Server: "host", Status: "queued", ToolName: "shell_exec", Cmd: "id -u"}
	s.shellJobs[large.ID], s.shellJobs[small.ID] = large, small
	s.shellQueues["host"] = []string{large.ID, small.ID}
	if err := s.saveTaskStateLocked(large.ID, small.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/host?timeout=1", nil), "host")
	var envelope map[string]any
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope["id"] != small.ID {
		t.Fatalf("compatible job blocked: code=%d", w.Code)
	}
	if large.Status != "queued" || large.StartedAt != 0 || small.Status != "running" || len(s.shellQueues["host"]) != 1 || s.shellQueues["host"][0] != large.ID {
		t.Fatal("original job or compatible lease changed incorrectly")
	}
	state, err := s.readTaskState()
	if err != nil || state.Shell[large.ID].Status != "queued" || state.Shell[small.ID].Status != "running" {
		t.Fatalf("durable state: %v", err)
	}
	// Updating the reader later admits the SAME large job, without recreation.
	w = httptest.NewRecorder()
	s.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/host?timeout=1&max_response_bytes=67108864", nil), "host")
	if w.Code != http.StatusOK || large.Status != "running" || len(s.shellQueues["host"]) != 0 {
		t.Fatal("original large job not retained for update")
	}
}

func TestShellQueueCompatibilityScanIsBoundedAndKeepsBlockedOrder(t *testing.T) {
	s := New(Config{})
	t.Cleanup(func() { _ = s.Close() })
	for i := 0; i < 17; i++ {
		id := strings.Repeat("x", i+1)
		s.shellJobs[id] = &shellJob{ID: id, Server: "host", Status: "queued", Arguments: map[string]any{"payload": strings.Repeat("x", 1048576)}}
		s.shellQueues["host"] = append(s.shellQueues["host"], id)
	}
	s.shellJobs["small"] = &shellJob{ID: "small", Server: "host", Status: "queued"}
	s.shellQueues["host"] = append(s.shellQueues["host"], "small")
	before := append([]string(nil), s.shellQueues["host"]...)
	w := httptest.NewRecorder()
	s.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/host?timeout=1", nil), "host")
	if w.Code != http.StatusRequestEntityTooLarge || len(s.shellQueues["host"]) != len(before) {
		t.Fatal("scan did not hold bounded queue")
	}
	for i, id := range before {
		if s.shellQueues["host"][i] != id || s.shellJobs[id].Status != "queued" || s.shellJobs[id].StartedAt != 0 {
			t.Fatal("blocked ordering or lease changed")
		}
	}
}

func TestShellQueueEnvelopeBounds(t *testing.T) {
	for query, want := range map[string]int{"": 1 << 20, "0": 1 << 20, "-1": 1 << 20, "bad": 1 << 20, "1748195": 1748195, "67108864": 64 << 20, "67108865": 64 << 20} {
		if got := shellQueueResponseLimit(httptest.NewRequest(http.MethodGet, "/queue/host?max_response_bytes="+query, nil)); got != want {
			t.Fatalf("%q: %d != %d", query, got, want)
		}
	}
}
