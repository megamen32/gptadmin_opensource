package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func legacySchemaFixture(t *testing.T) (*Server, Agent) {
	t.Helper()
	s, child := childSchemaFixture(t, t.TempDir())
	s.mu.Lock()
	s.agents["shell:fixture"].Transport = "long_poll"
	s.agents["shell:fixture"].LastSeen = nowFloat()
	s.mu.Unlock()
	return s, child
}

func legacySchemaRequest(ctx context.Context, caller string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
	return requestWithAuthClaims(r, map[string]any{"client_id": caller, "sub": caller, "jti": caller, "scope": "gptadmin.read gptadmin.exec"})
}

func TestLegacyChildSchemaCompletesThroughSameIDTransport(t *testing.T) {
	s, child := legacySchemaFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan map[string]any, 1)
	go func() { done <- s.directChildSchemaForRequest(legacySchemaRequest(ctx, "a"), child) }()
	var id string
	for id == "" {
		s.mu.Lock()
		for key, job := range s.shellJobs {
			if job.ToolName == "mcp_tools" && job.Server == "fixture" && job.Arguments["ref"] == "fixture-child" {
				id = key
			}
		}
		s.mu.Unlock()
		if id != "" {
			break
		}
		select {
		case got := <-done:
			t.Fatalf("legacy schema failed before native dispatch: %#v", got)
		case <-ctx.Done():
			t.Fatal("native schema task never appeared")
		case <-time.After(time.Millisecond):
		}
	}
	finishChildSchema(t, s, id, map[string]any{"ref": "fixture-child", "tools": []any{map[string]any{"name": "list_sessions", "inputSchema": map[string]any{"type": "object"}}}})
	got := <-done
	if got["status"] != "completed" || got["server_id"] != child.AgentID || len(searchChildToolMaps(mapValue(got["response"])["tools"])) != 1 {
		t.Fatalf("same-ID schema result lost: %#v", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 1 || len(s.relayJobs) != 1 || s.relayJobs[id] == nil {
		t.Fatal("schema transport did not preserve one durable ID")
	}
}

func TestLegacyChildSchemaTimeoutDeduplicatesConcurrentCaller(t *testing.T) {
	s, child := legacySchemaFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			got := s.directChildSchemaForRequest(legacySchemaRequest(ctx, "a"), child)
			if got["status"] == "completed" {
				t.Error("unanswered task reported complete")
			}
		}()
	}
	wg.Wait()
	for _, caller := range []string{"a", "b"} {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		start := time.Now()
		s.directChildSchemaForRequest(legacySchemaRequest(ctx, caller), child)
		cancel()
		if time.Since(start) > time.Second {
			t.Fatal("caller deadline ignored")
		}
	}
	s.mu.Lock()
	if len(s.shellJobs) != 2 || len(s.relayJobs) != 2 || len(s.shellQueues["fixture"]) != 2 {
		s.mu.Unlock()
		t.Fatalf("cold retries duplicated jobs or shared caller receipt: shell=%d relay=%d queue=%d", len(s.shellJobs), len(s.relayJobs), len(s.shellQueues["fixture"]))
	}
	var ownedID string
	for id, job := range s.shellJobs {
		if job.Caller.ClientID == "a" {
			ownedID = id
		}
	}
	s.mu.Unlock()
	finishChildSchema(t, s, ownedID, map[string]any{"ref": "fixture-child", "tools": []any{map[string]any{"name": "list_sessions"}}})
	got := s.directChildSchemaForRequest(legacySchemaRequest(context.Background(), "a"), child)
	if got["status"] != "completed" || got["job_id"] != ownedID {
		t.Fatalf("late completion did not reuse original receipt: %#v", got)
	}
}

func TestLegacyChildSchemaRoutedFailureNeverQueues(t *testing.T) {
	s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "unavailable"})
	})
	s.mu.Lock()
	s.agents["shell:peer"].Transport = "long_poll"
	s.agents["shell:peer"].LastSeen = nowFloat()
	s.mu.Unlock()
	r := legacySchemaRequest(context.Background(), "a")
	r.Header.Set("Authorization", "Bearer schema-test-token")
	got := s.directChildSchemaForRequest(r, child)
	s.mu.Lock()
	defer s.mu.Unlock()
	if got["status"] != "failed" || calls.Load() != 1 || len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatalf("failed direct route fell back to queue: %#v", got)
	}
}

func TestLegacyChildSchemaUnavailableNeverQueues(t *testing.T) {
	for _, state := range []string{"offline", "unapproved", "nonlegacy", "stale", "denied"} {
		t.Run(state, func(t *testing.T) {
			s, child := legacySchemaFixture(t)
			r := legacySchemaRequest(context.Background(), "a")
			s.mu.Lock()
			parent := s.agents["shell:fixture"]
			switch state {
			case "offline":
				parent.Status = "offline"
			case "unapproved":
				parent.Meta["approved"] = false
			case "nonlegacy":
				parent.Transport = "node"
			case "stale":
				parent.LastSeen = nowFloat() - 3600
			case "denied":
				r = requestWithAccessProfile(r, AccessProfile{ID: "denied", AllowedTargets: []string{"shell:other"}})
			}
			s.mu.Unlock()
			got := s.directChildSchemaForRequest(r, child)
			if got["status"] != "failed" {
				t.Fatalf("unavailable schema accepted: %#v", got)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
				t.Fatal("unavailable or denied parent queued work")
			}
		})
	}
}
