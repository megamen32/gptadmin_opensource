package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
)

func TestQueueExecutesGenericMCPToolAndPostsResult(t *testing.T) {
	resultCh := make(chan hub.TaskResult, 1)
	var polls atomic.Int32
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/queue/queue-agent"):
			w.Header().Set("Content-Type", "application/json")
			if polls.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "generic-1", "trace_id": "trace-shell-789", "traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "tool_name": "system_info", "arguments": map[string]any{}})
				return
			}
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPost && r.URL.Path == "/queue/queue-agent/result":
			var result hub.TaskResult
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Errorf("decode result: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			resultCh <- result
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer hubServer.Close()

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	s := New(Config{Name: "queue-agent", HubURL: hubServer.URL, QueueEnabled: true, QueueTimeout: 1, IdentityDir: t.TempDir(), SpillDir: t.TempDir(), OutboxDir: t.TempDir(), AuditLog: auditPath})
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.queueLoop(ctx)
	select {
	case result := <-resultCh:
		if result.ID != "generic-1" || result.TraceID != "trace-shell-789" || result.TraceParent != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" || !strings.Contains(strings.ToLower(toJSON(result.Result)), "capability_registry") {
			t.Fatalf("unexpected generic result: %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("generic MCP queue result was not posted")
	}
	auditData, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(auditData), `"trace_id":"trace-shell-789"`) {
		t.Fatalf("ShellMCP audit lost queue trace: %s", auditData)
	}
}

func TestQueueLoopStopsOnContextCancellation(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted <- struct{}{}
		<-r.Context().Done()
	}))
	defer hubServer.Close()

	s := New(Config{Name: "queue-agent", HubURL: hubServer.URL, QueueEnabled: true, QueueTimeout: 60, IdentityDir: t.TempDir(), SpillDir: t.TempDir(), OutboxDir: t.TempDir()})
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.queueLoop(ctx)
		close(done)
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("queue poll did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("queue loop did not stop after context cancellation")
	}
}

func toJSON(value any) string {
	b, _ := json.Marshal(value)
	return string(b)
}
