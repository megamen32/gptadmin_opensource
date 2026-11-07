package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// Real TCP/HTTP requests, with the configured 30s ceiling scaled down.
func TestHTTPExecutionTimeout(t *testing.T) {
	for _, transport := range []string{"streamable-http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			for _, tc := range []struct {
				name         string
				delayMethod  string
				deadline     time.Duration
				flushHeaders bool
				cancel       bool
				wantErr      error
			}{
				{name: "slow_headers", delayMethod: "tools/call", deadline: 2 * time.Second},
				{name: "slow_body", delayMethod: "tools/call", deadline: 2 * time.Second, flushHeaders: true},
				{name: "caller_deadline", delayMethod: "tools/call", deadline: 40 * time.Millisecond, wantErr: context.DeadlineExceeded},
				{name: "extended_deadline_expires", delayMethod: "tools/call", deadline: 140 * time.Millisecond, wantErr: context.DeadlineExceeded},
				{name: "caller_cancel", delayMethod: "tools/call", deadline: 2 * time.Second, cancel: true, wantErr: context.Canceled},
				{name: "handshake_bounded", delayMethod: "initialize", deadline: 2 * time.Second, wantErr: context.DeadlineExceeded},
				{name: "no_deadline_bounded", delayMethod: "tools/call", wantErr: context.DeadlineExceeded},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := context.Background()
					cancel := func() {}
					if tc.deadline > 0 {
						ctx, cancel = context.WithTimeout(ctx, tc.deadline)
					}
					defer cancel()
					events := make(chan []byte, 4)
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if transport == "sse" && r.Method == http.MethodGet {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, "event: endpoint\ndata: /message\n\n")
							w.(http.Flusher).Flush()
							for {
								select {
								case payload := <-events:
									fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
									w.(http.Flusher).Flush()
								case <-r.Context().Done():
									return
								}
							}
						}
						var req rpcRequest
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							t.Error(err)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						if req.Method == tc.delayMethod {
							if tc.flushHeaders {
								w.WriteHeader(http.StatusOK)
								w.(http.Flusher).Flush()
							}
							if tc.cancel {
								cancel()
							}
							select {
							case <-time.After(200 * time.Millisecond):
							case <-r.Context().Done():
								return
							}
						}
						if req.Method == "notifications/initialized" {
							w.WriteHeader(http.StatusAccepted)
							return
						}
						result := map[string]any{"protocolVersion": protocolVersion}
						if req.Method == "tools/call" {
							result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "slow-result"}}}
						}
						payload := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
						if transport == "sse" {
							encoded, _ := json.Marshal(payload)
							events <- encoded
						} else {
							_ = json.NewEncoder(w).Encode(payload)
						}
					}))
					defer srv.Close()
					c := New()
					c.HTTP.Timeout = 80 * time.Millisecond
					defer c.CloseAll()
					agent := supervisor.Agent{Ref: "slow", Transport: transport, URL: srv.URL, Enabled: true}
					result, err := c.CallTool(ctx, agent, "slow", nil)
					if tc.wantErr != nil {
						if !errors.Is(err, tc.wantErr) {
							t.Fatalf("want %v, got %v", tc.wantErr, err)
						}
					} else if err != nil || !strings.Contains(fmt.Sprint(result["content"]), "slow-result") {
						t.Fatalf("result=%v err=%v", result, err)
					}
					if c.HTTP.Timeout != 80*time.Millisecond {
						t.Fatal("shared HTTP timeout mutated")
					}
				})
			}
		})
	}
}
