// LHC:COVERS go-hub/internal/hub/auth_snapshot.go go-hub/internal/hub/server.go go-hub/internal/hub/search_catalog.go
package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Observe entry into the real search capacity wait, without a timing guess or
// replacing the search, profile store or generation gate.
type authSearchWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *authSearchWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestAuthAdmissionSourceSearchDoesNotHoldLocks(t *testing.T) {
	writer := authContinuityNode(t, "writer", "")
	for i := 0; i < toolSearchMaxConcurrent; i++ {
		release, admitted := writer.admitToolSearch()
		if !admitted {
			t.Fatal("cannot reserve search capacity")
		}
		defer release()
	}
	base, cancel := context.WithCancel(context.Background())
	ctx := &authSearchWaitContext{Context: base, waiting: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer fixture-owner")
	response := httptest.NewRecorder()
	handler := writer.authSnapshotGate(http.HandlerFunc(writer.requireCtl(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, writer.searchToolsForRequest(r, map[string]any{"query": "shell"}))
	})))
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("search did not stop on cancellation")
		}
	}()
	select {
	case <-ctx.waiting:
	case <-done:
		t.Fatalf("search did not reach capacity wait: %s", response.Body.String())
	case <-time.After(2 * time.Second):
		t.Fatal("search did not reach capacity wait")
	}
	if !writer.authMu.TryLock() {
		t.Fatal("source search capacity wait holds the auth admission gate")
	}
	writer.authMu.Unlock()
	if !writer.mu.TryLock() {
		t.Fatal("source search capacity wait holds the hub mutex")
	}
	writer.mu.Unlock()
}

// Exercise both ordinary poll routes while an exclusive auth writer is held.
// A one-second empty poll proves the same lock boundary as a 55-second poll
// without making the normal regression suite wait for the production timeout.
func TestAuthAdmissionLongPollsBypassGenerationGate(t *testing.T) {
	for _, kind := range []string{"shell", "relay"} {
		t.Run(kind, func(t *testing.T) {
			writer := authContinuityNode(t, "writer", "")
			path := "/queue/writer?timeout=1&server_id=writer-identity&public_key=writer-public&fingerprint=writer-fingerprint"
			credential := strings.Repeat("x", 32)
			if kind == "relay" {
				writer.cfg.RelayAgentToken = "fixture-relay"
				credential = writer.cfg.RelayAgentToken
				path = "/mcp-relay/poll/auth-relay?timeout=1"
				writer.mu.Lock()
				writer.agents["auth-relay"] = &Agent{AgentID: "auth-relay", Status: "online"}
				writer.mu.Unlock()
			}
			writer.authMu.Lock()
			var once sync.Once
			unlock := func() { once.Do(writer.authMu.Unlock) }
			ctx, cancel := context.WithCancel(context.Background())
			request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+credential)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { writer.Handler().ServeHTTP(response, request); close(done) }()
			defer func() {
				unlock()
				cancel()
				writer.mu.Lock()
				writer.cond.Broadcast()
				writer.mu.Unlock()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("empty poll did not stop")
				}
			}()
			deadline := time.Now().Add(2 * time.Second)
			for {
				writer.mu.Lock()
				id := "shell:writer"
				if kind == "relay" {
					id = "auth-relay"
				}
				agent := writer.agents[id]
				started := agent != nil && agent.LastSeen > 0
				if kind == "shell" {
					started = started && truthy(agent.Meta["poll_heartbeat"])
				}
				writer.mu.Unlock()
				if started {
					break
				}
				select {
				case <-done:
					t.Fatalf("poll was rejected: %d %s", response.Code, response.Body.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("poll blocked behind the exclusive auth gate")
				}
				runtime.Gosched()
			}
			select {
			case <-done:
				t.Fatal("poll did not remain in flight")
			default:
			}
			unlock()
			if !writer.authMu.TryLock() {
				t.Fatal("in-flight poll retained an auth gate reader")
			}
			writer.authMu.Unlock()
		})
	}
}
