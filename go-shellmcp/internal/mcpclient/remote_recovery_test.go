package mcpclient

// These loopback protocol regressions prove client recovery and eviction only;
// they do not establish deployment or acceptance of an upstream MCP service.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

func TestStreamableHTTPLateSessionResponsePreservesRecoveredSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lateStatus int
		reuseID    bool
	}{
		{name: "404", lateStatus: http.StatusNotFound},
		{name: "500", lateStatus: http.StatusInternalServerError},
		{name: "500_reused_id", lateStatus: http.StatusInternalServerError, reuseID: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var initializes, deletes atomic.Int32
			earlyStarted := make(chan struct{})
			lateStarted := make(chan struct{})
			recoveredCall := make(chan struct{})
			sessionID := func(generation int32) string {
				if tc.reuseID {
					return "session-reused"
				}
				return fmt.Sprintf("session-%d", generation)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var req rpcRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				reply := func(result map[string]any) {
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				}
				switch req.Method {
				case "initialize":
					// Strict header rejection is covered separately, so this
					// test isolates concurrent recovery even on the old client.
					w.Header().Set("Mcp-Session-Id", sessionID(initializes.Add(1)))
					reply(map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}})
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case "tools/list":
					if got := r.Header.Get("Mcp-Session-Id"); got != sessionID(initializes.Load()) {
						t.Errorf("tools/list session=%q, want current %q", got, sessionID(initializes.Load()))
					}
					reply(map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}})
				case "tools/call":
					params, _ := req.Params.(map[string]any)
					name, _ := params["name"].(string)
					if initializes.Load() == 1 {
						if name == "recover" {
							close(earlyStarted)
							select {
							case <-lateStarted:
							case <-r.Context().Done():
								return
							}
							w.WriteHeader(http.StatusNotFound)
							return
						}
						close(lateStarted)
						select {
						case <-recoveredCall:
						case <-r.Context().Done():
							return
						}
						// A late response must not restore S0 through its header.
						w.Header().Set("Mcp-Session-Id", sessionID(1))
						w.WriteHeader(tc.lateStatus)
						return
					}
					if got := r.Header.Get("Mcp-Session-Id"); got != sessionID(initializes.Load()) {
						t.Errorf("retry session=%q, want current %q", got, sessionID(initializes.Load()))
					}
					reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}})
					if name == "recover" {
						close(recoveredCall)
					}
				default:
					t.Errorf("unexpected method %q", req.Method)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer srv.Close()
			c := New()
			defer c.CloseAll()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			agent := supervisor.Agent{Ref: "late-response", Transport: "streamable-http", URL: srv.URL, Enabled: true}
			if _, err := c.ListTools(ctx, agent); err != nil {
				t.Fatal(err)
			}
			earlyResult := make(chan error, 1)
			go func() {
				_, err := c.CallTool(ctx, agent, "recover", nil)
				earlyResult <- err
			}()
			select {
			case <-earlyStarted:
			case <-ctx.Done():
				t.Fatal("first S0 request did not start")
			}
			lateResult := make(chan error, 1)
			go func() {
				_, err := c.CallTool(ctx, agent, "late", nil)
				lateResult <- err
			}()
			if err := <-earlyResult; err != nil {
				t.Fatalf("first S0 request failed recovery: %v", err)
			}
			if err := <-lateResult; (err == nil) != (tc.lateStatus == http.StatusNotFound) {
				t.Fatalf("late S0 status=%d, err=%v", tc.lateStatus, err)
			}
			if _, err := c.ListTools(ctx, agent); err != nil {
				t.Fatalf("recovered session unusable after late response: %v", err)
			}
			if got := initializes.Load(); got != 2 {
				t.Errorf("initialize requests=%d, want 2; late S0 response invalidated S1", got)
			}
			if got := deletes.Load(); got != 0 {
				t.Errorf("DELETE requests=%d, want 0; late S0 error closed S1", got)
			}
		})
	}
}
