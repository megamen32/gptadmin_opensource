// LHC:COVERS go-hub/internal/hub/auth_snapshot.go go-hub/internal/hub/node_bearer_admission.go go-hub/internal/hub/task_subscriptions.go
package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exercise the real profile store and both admission snapshots concurrently.
// This is a local synchronization regression, not installed MCP acceptance.
func TestAuthAdmissionConcurrentProfileUpdate(t *testing.T) {
	for _, endpoint := range []string{"snapshot", "node-bearer"} {
		t.Run(endpoint, func(t *testing.T) {
			writer := authContinuityNode(t, "writer", "")
			if _, err := writer.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
				profiles["fixture"] = AccessProfile{ID: "fixture", AccessMode: accessModeFull,
					ApprovalMode: approvalModeUnrestricted, Version: 1}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			writer.mu.Lock()
			writer.oauthClients["fixture-oauth"] = oauthClientMetadata{CreatedAt: time.Now().Unix(), ProfileID: "fixture"}
			err := writer.saveOAuthClientsStateLocked()
			writer.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			token, err := writer.issueOAuthAccessToken("fixture-oauth", writer.cfg.MCPResource, "gptadmin.read gptadmin.exec")
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var workers sync.WaitGroup
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				for i := 0; i < 32; i++ {
					_, err := writer.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
						profile := profiles["fixture"]
						profile.Version++
						profiles["fixture"] = profile
						return nil
					})
					if err != nil {
						t.Errorf("update profile: %v", err)
						return
					}
				}
			}()
			handler := writer.authSnapshotGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				releaseAuthAdmission(r)
				w.WriteHeader(http.StatusNoContent)
			}))
			for i := 0; i < 4; i++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					for j := 0; j < 64; j++ {
						request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
						if endpoint == "node-bearer" {
							_, status := writer.nodeBearerAdmissionResult(request, token)
							if status != http.StatusOK && status != http.StatusServiceUnavailable {
								t.Errorf("bearer admission status = %d", status)
								return
							}
						} else {
							response := httptest.NewRecorder()
							handler.ServeHTTP(response, request)
							if response.Code != http.StatusNoContent && response.Code != http.StatusServiceUnavailable {
								t.Errorf("snapshot admission status = %d", response.Code)
								return
							}
						}
					}
				}()
			}
			close(start)
			workers.Wait()
		})
	}
}

type authAdmissionStreamResponse struct {
	http.ResponseWriter
	flushed chan struct{}
	once    sync.Once
}

func (w *authAdmissionStreamResponse) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
	w.once.Do(func() { close(w.flushed) })
}

// A real long-lived handler without an explicit dispatch release must remain
// unable to pin the generation gate. Restoring a handler-wide RLock to fix the
// unsent-node fallback would regress this ordinary subscription path.
func TestAuthAdmissionSubscriptionDoesNotHoldLocks(t *testing.T) {
	writer := authContinuityNode(t, "writer", "")
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":"auth-lock-check","method":"subscriptions/listen","params":{"notifications":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer fixture-owner")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "subscriptions/listen")
	response := httptest.NewRecorder()
	stream := &authAdmissionStreamResponse{ResponseWriter: response, flushed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		writer.Handler().ServeHTTP(stream, request)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("subscription did not stop on cancellation")
		}
	}()
	select {
	case <-stream.flushed:
	case <-done:
		t.Fatalf("subscription did not open: %d %s", response.Code, response.Body.String())
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not acknowledge admission")
	}
	if !writer.authMu.TryLock() {
		t.Fatal("live subscription holds the auth admission gate")
	}
	writer.authMu.Unlock()
	if !writer.mu.TryLock() {
		t.Fatal("live subscription holds the hub mutex")
	}
	writer.mu.Unlock()
}
