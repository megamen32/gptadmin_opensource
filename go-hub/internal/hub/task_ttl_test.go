package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestTTLIncludesQueueAndPreservesResults(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	now := nowFloat()
	for _, status := range []string{"queued", "running", "completed"} {
		j := &shellJob{ID: status, Server: "ttl-worker", Status: status, CreatedAt: now - 10801, Timeout: 86400, Result: map[string]any{"saved": true}}
		if status == "running" {
			j.StartedAt = now - 1
		}
		if status == "completed" {
			j.DoneAt = now - 1
		}
		s.shellJobs[status] = j
	}
	s.shellJobs["fresh"] = &shellJob{ID: "fresh", Server: "ttl-worker", Status: "queued", CreatedAt: now - 60}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.maintainTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"queued", "running"} {
		j := s.shellJobs[id]
		if j.Status != "cancelled" || firstString(mapValue(j.Error), "code") != "request_ttl_expired" {
			t.Fatalf("%s not expired: %+v", id, j)
		}
		if j.CreatedAt != now-10801 {
			t.Fatal("TTL clock reset")
		}
	}
	if s.shellJobs["fresh"].Status != "queued" || s.shellJobs["completed"].Status != "completed" || s.shellJobs["completed"].Result == nil {
		t.Fatal("fresh/history modified")
	}
	if len(s.shellControls["ttl-worker"]) != 1 {
		t.Fatal("running cancellation not dispatched exactly once")
	}
	stored, err := s.readTaskRecords("queued")
	if err != nil || stored.Shell["queued"].Status != "cancelled" {
		t.Fatalf("expiration not durable: %v", err)
	}
}

func TestRequestTTLPreventsExpiredQueueDispatch(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	for _, id := range []string{"expired", "fresh"} {
		created := nowFloat()
		if id == "expired" {
			created -= 10801
		}
		s.shellJobs[id] = &shellJob{ID: id, Server: "host", Status: "queued", CreatedAt: created, ToolName: "mcp_manage", Arguments: map[string]any{"action": "list"}}
	}
	s.shellQueues["host"] = []string{"expired", "fresh"}
	if err := s.saveTaskStateLocked(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.pollShellQueue(w, httptest.NewRequest(http.MethodGet, "/queue/host?timeout=1", nil), "host")
	var envelope map[string]any
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope["id"] != "fresh" {
		t.Fatalf("wrong dispatch: %d %s", w.Code, w.Body.String())
	}
	if s.shellJobs["expired"].Status != "cancelled" || s.shellJobs["expired"].StartedAt != 0 {
		t.Fatal("expired request executed")
	}
}
